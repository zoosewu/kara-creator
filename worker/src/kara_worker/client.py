"""和 NAS 之間的 worker 協定（docs/worker-protocol.md）：打招呼、領任務、心跳、下載輸入檔、上傳結果。

開兩條迴圈：heavy（去人聲、對時、檢查、燒錄；GPU，一次一件）與 interactive（假名；CPU，可以同時跑）。
模型留在這個行程裡重複使用。輸入檔以 sha256 存在本機快取（7 天沒用到就刪），字型檔另外快取。
關閉時把手上的任務交還 NAS（bye），NAS 立刻改派。
"""
from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import tempfile
import threading
import time
import traceback
import urllib.error
import urllib.request
import uuid
from pathlib import Path

from . import tasks
from .media import FFMPEG, Cancelled
from .versions import current as versions

PREFIX = "/worker/v1"
HEAVY = ("separate", "align", "align_from", "align_line", "qa", "render")
INTERACTIVE = ("reading",)
BLOB_TTL = 7 * 24 * 3600


def default_cache() -> Path:
    if os.name == "nt":
        return Path(os.environ.get("LOCALAPPDATA", Path.home())) / "kara-ai" / "cache"
    return Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "kara-ai"


class Stop(Exception):
    """NAS 不認得這個 worker 了（版本不同、或要求重新連線）。"""


class Gone(Exception):
    """任務已經不是這個 worker 的（取消、改派、NAS 重新啟動）：丟掉結果。"""


class Worker:
    def __init__(self, nas: str, name: str, token: str | None, cache: Path, channels: list[str], device: str):
        self.nas = nas.rstrip("/")
        self.name = name
        self.token = token
        self.cache = cache
        self.channels = channels
        self.device = device
        self.instance = uuid.uuid4().hex[:16]
        self.heartbeat = 10.0
        self.stopping = threading.Event()
        self.fresh: list[str] = []              # 上次 lease 之後新增到快取的 sha256
        self.lock = threading.Lock()
        self.current: dict[str, str] = {}       # 通道 → 任務 id
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))  # NAS 在區域網路：不走 proxy
        (cache / "blobs").mkdir(parents=True, exist_ok=True)
        (cache / "fonts").mkdir(parents=True, exist_ok=True)
        (cache / "work").mkdir(parents=True, exist_ok=True)

    # ---- HTTP ------------------------------------------------------------------

    def request(self, method: str, path: str, body=None, *, data=None, headers=None, timeout: float = 30):
        """回傳 (狀態碼, 內容 bytes)。連不上時丟 urllib.error.URLError。"""
        headers = dict(headers or {})
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(self.nas + PREFIX + path, data=data, method=method, headers=headers)
        try:
            with self._opener.open(req, timeout=timeout) as resp:
                return resp.status, resp.read()
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read()

    @staticmethod
    def detail(raw: bytes) -> str:
        try:
            return json.loads(raw).get("detail") or raw.decode(errors="replace")
        except ValueError:
            return raw.decode(errors="replace")

    # ---- 連線 ------------------------------------------------------------------

    def hardware(self) -> dict:
        info = {"nvenc": False}
        if "heavy" in self.channels:
            try:
                import torch
                if torch.cuda.is_available():
                    props = torch.cuda.get_device_properties(0)
                    info["gpu"], info["vram_mb"] = props.name, props.total_memory // (1 << 20)
            except Exception:
                pass
            try:
                out = subprocess.run([FFMPEG, "-hide_banner", "-encoders"], capture_output=True, text=True, timeout=30)
                info["nvenc"] = "h264_nvenc" in out.stdout
            except Exception:
                pass
        return info

    def cached(self, limit: int = 500) -> list[str]:
        files = sorted((self.cache / "blobs").glob("*"), key=lambda p: p.stat().st_mtime, reverse=True)
        return [p.name for p in files[:limit] if len(p.name) == 64]

    def hello(self) -> None:
        kinds = [k for ch in self.channels for k in (HEAVY if ch == "heavy" else INTERACTIVE)]
        hardware = self.hardware()
        if "heavy" in self.channels and "gpu" not in hardware:
            print("[!] 偵測不到 CUDA 顯示卡：去人聲、對時會用 CPU，非常慢。Windows 直接執行請重新執行 setup.ps1"
                  "（會檢查並重裝 CUDA 版的 PyTorch）；container 請確認有 --gpus all", flush=True)
        body = {"name": self.name, "instance": self.instance, "versions": versions(), "hardware": hardware,
                "kinds": kinds, "channels": self.channels, "cached": self.cached()}
        while not self.stopping.is_set():
            try:
                status, raw = self.request("POST", "/hello", body)
            except (urllib.error.URLError, OSError) as exc:
                print(f"連不上 NAS（{self.nas}）：{exc}；5 秒後再試", flush=True)
                self.stopping.wait(5)
                continue
            if status == 409:
                diff = json.loads(raw).get("diff", [])
                raise SystemExit(f"版本和 NAS 不同（{', '.join(diff)}），請更新 worker（container：docker compose pull；Windows：git pull 後重新執行 setup.ps1）後再啟動")
            if status == 401:
                raise SystemExit("token 不對：請確認 --token 和 NAS 的 --worker-token 相同")
            if status != 200:
                print(f"NAS 回應 {status}：{self.detail(raw)}；5 秒後再試", flush=True)
                self.stopping.wait(5)
                continue
            self.heartbeat = min(float(json.loads(raw).get("heartbeat_seconds", 10)), 10.0)
            print(f"已連上 NAS {self.nas}（{self.name}，通道：{', '.join(self.channels)}）", flush=True)
            return

    def bye(self) -> None:
        try:
            self.request("POST", "/bye", {"instance": self.instance}, timeout=5)
        except Exception:
            pass

    # ---- 迴圈 ------------------------------------------------------------------

    def loop(self, channel: str) -> None:
        while not self.stopping.is_set():
            with self.lock:
                fresh, self.fresh = self.fresh, []
            try:
                status, raw = self.request("POST", "/lease", {"instance": self.instance, "channel": channel,
                                                               "cached": fresh}, timeout=40)
            except (urllib.error.URLError, OSError, TimeoutError) as exc:
                if self.stopping.is_set():
                    return
                print(f"[{channel}] 和 NAS 的連線中斷：{exc}；5 秒後再試", flush=True)
                self.stopping.wait(5)
                continue
            if status == 204:
                continue
            if status == 404:   # NAS 重新啟動過，不認得這個 instance：重新打招呼
                self.hello()
                continue
            if status != 200:
                print(f"[{channel}] 領任務失敗（{status}）：{self.detail(raw)}", flush=True)
                if status == 409:
                    self.stopping.set()
                    return
                self.stopping.wait(5)
                continue
            self.run(channel, json.loads(raw))

    def run(self, channel: str, task: dict) -> None:
        tid, kind = task["id"], task["kind"]
        print(f"[{channel}] 開始 {kind}（{task.get('song')}）", flush=True)
        with self.lock:
            self.current[channel] = tid
        state = {"logs": [], "progress": None, "cancel": False, "gone": False}
        state_lock = threading.Lock()
        done = threading.Event()

        def log(line: str) -> None:
            print(f"  {line}", flush=True)
            with state_lock:
                state["logs"].append(line)

        def progress(value: float) -> None:
            with state_lock:
                state["progress"] = max(0.0, min(1.0, float(value)))

        def should_stop() -> bool:
            return state["cancel"] or state["gone"] or self.stopping.is_set()

        def beat() -> None:
            # 有新紀錄時每秒送一次（畫面才看得到即時進度），否則每 heartbeat 秒送一次心跳
            last = time.monotonic()
            while not done.wait(1.0):
                with state_lock:
                    pending = bool(state["logs"])
                if pending or time.monotonic() - last >= self.heartbeat:
                    self.progress(tid, state, state_lock)
                    last = time.monotonic()

        hb = threading.Thread(target=beat, daemon=True)
        hb.start()
        work = Path(tempfile.mkdtemp(prefix=f"{kind}-", dir=self.cache / "work"))
        try:
            inputs = {name: self.blob(blob) for name, blob in (task.get("inputs") or {}).items()}
            ctx = tasks.Context(inputs=inputs, out_dir=work, log=log, progress=progress, should_stop=should_stop,
                                font=self.font, device=self.device)
            result = tasks.run(kind, task.get("params") or {}, ctx)
            if should_stop():
                raise Cancelled()
            self.progress(tid, state, state_lock)   # 送出最後的紀錄
            files = {name: self.upload(tid, work / name) for name in ctx.files}
            status, raw = self.request("POST", f"/tasks/{tid}/complete",
                                       {"instance": self.instance, "result": result, "files": files}, timeout=60)
            if status >= 300:
                print(f"[{channel}] 回報完成被拒絕（{status}）：{self.detail(raw)}", flush=True)
            else:
                print(f"[{channel}] 完成 {kind}", flush=True)
        except (Cancelled, Gone):
            print(f"[{channel}] {kind} 已取消或改派，丟掉結果", flush=True)
        except tasks.TaskError as exc:
            self.fail(tid, str(exc), exc.retryable)
        except Exception as exc:
            traceback.print_exc()
            message = str(exc) or exc.__class__.__name__
            retryable = "out of memory" in message.lower() or exc.__class__.__name__ == "OutOfMemoryError"
            if retryable:
                message = "顯示卡記憶體不足"
            self.fail(tid, message if retryable else f"AI 伺服器處理失敗：{message}", retryable)
        finally:
            done.set()
            shutil.rmtree(work, ignore_errors=True)
            with self.lock:
                self.current.pop(channel, None)
            if kind != "reading":
                try:
                    import torch
                    if torch.cuda.is_available():
                        torch.cuda.empty_cache()
                except Exception:
                    pass

    def progress(self, tid: str, state: dict, lock: threading.Lock) -> None:
        with lock:
            logs, state["logs"] = state["logs"], []
            body = {"instance": self.instance, "progress": state["progress"], "logs": logs}
        try:
            status, raw = self.request("POST", f"/tasks/{tid}/progress", body, timeout=15)
        except (urllib.error.URLError, OSError, TimeoutError):
            return   # 暫時連不上：下一次心跳再送（租約 60 秒）
        if status in (404, 409):
            state["gone"] = True
        elif status == 200 and json.loads(raw).get("cancel"):
            state["cancel"] = True

    def fail(self, tid: str, message: str, retryable: bool) -> None:
        print(f"  [!] 失敗：{message}", flush=True)
        try:
            self.request("POST", f"/tasks/{tid}/fail", {"instance": self.instance, "error": message,
                                                         "retryable": retryable}, timeout=30)
        except Exception:
            pass

    # ---- 檔案 ------------------------------------------------------------------

    def _download(self, path: str, sha: str, dest: Path, what: str) -> Path:
        """下載到 dest（支援續傳），驗證 sha256。"""
        if dest.exists():
            os.utime(dest)   # 記下最近用過
            return dest
        part = dest.with_suffix(".part")
        for _ in range(3):
            offset = part.stat().st_size if part.exists() else 0
            headers = {"Range": f"bytes={offset}-"} if offset else {}
            req = urllib.request.Request(self.nas + PREFIX + path, headers={
                **headers, **({"Authorization": f"Bearer {self.token}"} if self.token else {})})
            try:
                with self._opener.open(req, timeout=60) as resp, part.open("ab" if resp.status == 206 else "wb") as f:
                    shutil.copyfileobj(resp, f, 1 << 20)
            except urllib.error.HTTPError as exc:
                if exc.code == 404:
                    raise Gone() from exc
                raise tasks.TaskError(f"下載{what}失敗：HTTP {exc.code}", True) from exc
            except (urllib.error.URLError, OSError, TimeoutError):
                continue   # 斷線：續傳
            h = hashlib.sha256()
            with part.open("rb") as f:
                for chunk in iter(lambda: f.read(1 << 20), b""):
                    h.update(chunk)
            if h.hexdigest() == sha:
                part.replace(dest)
                with self.lock:
                    self.fresh.append(sha)
                return dest
            part.unlink(missing_ok=True)
        raise tasks.TaskError(f"下載{what}失敗（內容和 sha256 不符或一直斷線）", True)

    def blob(self, blob: dict) -> Path:
        sha = blob["sha256"]
        return self._download(f"/blobs/{sha}", sha, self.cache / "blobs" / sha, "輸入檔")

    def font(self, font: dict) -> Path:
        sha = font["sha256"]
        path = self.cache / "fonts" / sha
        self._download(f"/fonts/{sha}", sha, path, "字型")
        with path.open("rb") as f:
            magic = f.read(4)
        ext = {b"ttcf": ".ttc", b"OTTO": ".otf"}.get(magic, ".ttf")
        # libass 依副檔名判斷字型種類：用有副檔名的連結
        named = self.cache / "fonts" / f"{sha}{ext}"
        if not named.exists():
            try:
                os.link(path, named)
            except OSError:
                shutil.copyfile(path, named)
        return named

    def upload(self, tid: str, path: Path) -> str:
        size = path.stat().st_size
        with path.open("rb") as f:
            status, raw = self.request("PUT", f"/tasks/{tid}/files/{path.name}?instance={self.instance}", data=f,
                                       headers={"Content-Length": str(size), "Content-Type": "application/octet-stream"},
                                       timeout=600)
        if status in (404, 409):
            raise Gone()
        if status != 200:
            raise tasks.TaskError(f"上傳 {path.name} 失敗：{self.detail(raw)}", True)
        return json.loads(raw)["sha256"]

    def cleanup(self) -> None:
        """刪掉 7 天沒用到的輸入檔與上次留下的暫存資料夾。"""
        now = time.time()
        for p in (self.cache / "blobs").glob("*"):
            if now - p.stat().st_mtime > BLOB_TTL:
                p.unlink(missing_ok=True)
        for p in (self.cache / "work").glob("*"):
            shutil.rmtree(p, ignore_errors=True)

