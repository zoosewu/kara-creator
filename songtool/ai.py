"""用到 AI 模型的處理：去人聲（Demucs）、對時（Whisper + CTC）、對時檢查（Whisper 聽寫）。

這些步驟交給「AI 伺服器」（ai/server.py，ai.ps1 啟動）執行，歌曲伺服器只負責曲庫、下載、字幕與燒錄。
兩邊可以在同一台電腦（預設 http://127.0.0.1:8770），也可以在不同電腦：
歌曲伺服器把需要的音訊上傳過去（以內容的 sha1 快取，同一個檔案只傳一次），取回結果。

後端由 configure() 指定（UI 啟動時依 --ai 或環境變數 SONG_AI_URL）：
    http://主機:port   交給 AI 伺服器
    local              在同一個行程裡直接執行（命令列工具沒指定 SONG_AI_URL 時的預設）

傳給 AI 伺服器的音訊：
    去人聲   44.1kHz 立體聲 wav（和本機執行時 Demucs 的輸入相同），取回各分軌的 wav
    對時等   人聲轉成 16kHz 單聲道 wav：Whisper 與 CTC 本來就只用這個格式，結果和直接給原檔相同，傳輸量小很多
"""
from __future__ import annotations

import hashlib
import hmac
import json
import os
import shutil
import tempfile
import threading
import time
import urllib.error
import urllib.request
from dataclasses import asdict
from pathlib import Path
from types import SimpleNamespace
from typing import Callable

from . import align, qa
from .config import FFMPEG
from .lyrics import Ruby
from .media import Cancelled, run

# AI 伺服器與歌曲伺服器之間的協定版本；兩邊不同時拒絕處理（請兩台都更新程式）。
API_VERSION = 1
DEFAULT_URL = "http://127.0.0.1:8770"
KINDS = ("separate", "align", "align_from", "align_line", "qa")

Log = Callable[[str], None]
Progress = Callable[[float], None]
Stop = Callable[[], bool]


def versions() -> dict:
    """會影響結果的程式版本：兩邊一致才能保證「對時 / 檢查結果沿用與否」的判斷正確。"""
    return {"api": API_VERSION, "align": align.VERSION, "qa": qa.VERSION}


# ---- 實際執行（AI 伺服器上；local 模式時在同一個行程）---------------------------

def execute(kind: str, params: dict, inputs: dict[str, Path], out_dir: Path, *,
            log: Log, progress: Progress, should_stop: Stop) -> dict:
    """執行一件 AI 工作。inputs 是輸入檔（audio），產生的檔案放進 out_dir，回傳 JSON 結果。"""
    audio = inputs["audio"]
    if kind == "separate":
        from .separate import SeparateOptions, _run_demucs

        opts = SeparateOptions(stems=int(params.get("stems", 2)), model=params.get("model", "htdemucs"),
                               device=params.get("device", "auto"), shifts=int(params.get("shifts", 0)),
                               jobs=int(params.get("jobs", 0)))
        stem_dir = _run_demucs(audio, out_dir / "demucs", opts, log, progress, should_stop)
        stems = []
        for f in sorted(stem_dir.glob("*.wav")):
            f.replace(out_dir / f.name)
            stems.append(f.name)
        shutil.rmtree(out_dir / "demucs", ignore_errors=True)
        return {"files": stems}

    texts = params.get("texts", [])
    rubies = [[Ruby(**r) for r in line] for line in params.get("rubies", [])]
    lyr = SimpleNamespace(lines=texts, rubies=rubies)   # align 只用到 lines 與 rubies
    model, language = params.get("model", "large-v3"), params.get("language")
    device = params.get("device", "auto")
    if kind == "align":
        return {"lines": align.align(audio, lyr, model_name=model, language=language, device=device, log=log)}
    if kind == "align_from":
        return {"lines": align.align_from(audio, lyr, int(params["first"]), float(params["anchor"]),
                                          model_name=model, language=language, device=device, log=log)}
    if kind == "align_line":
        return {"line": align.align_line(audio, texts[0], rubies[0], float(params["t0"]), params.get("t1"),
                                         language=language, device=device)}
    if kind == "qa":
        return {"doc": qa.check(params["lines"], texts, rubies, audio, language,
                                model_name=model, device=device, log=log)}
    raise ValueError(f"不支援的工作：{kind}")


# ---- 後端 --------------------------------------------------------------------

class Local:
    """在同一個行程裡直接執行（需要安裝 PyTorch 等 AI 套件）。"""

    url = "local"

    def run(self, kind: str, params: dict, inputs: dict[str, Path], out_dir: Path | None = None, *,
            log: Log, progress: Progress | None = None, should_stop: Stop | None = None,
            speech: bool = False) -> dict:
        with tempfile.TemporaryDirectory(prefix="ai_") as tmp:
            return execute(kind, params, inputs, out_dir or Path(tmp), log=log,
                           progress=progress or (lambda _v: None), should_stop=should_stop or (lambda: False))

    def status(self) -> dict:
        return {"url": self.url, "online": True, "local": True}


class AIServerError(RuntimeError):
    pass


class Remote:
    """交給 AI 伺服器執行：上傳音訊（已經有的不重傳）→ 排入工作 → 轉述紀錄與進度 → 取回結果。"""

    POLL = 0.5          # 查詢進度的間隔（秒）
    RETRY = 3.0         # 連不上時多久再試一次（秒）
    LOST = 20.0         # 處理中連線中斷超過這麼久就放棄

    def __init__(self, url: str, token: str | None = None):
        self.url = url.rstrip("/")
        self.token = token or None
        # 不走系統 proxy：AI 伺服器在本機或區域網路
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        self._status: dict = {"url": self.url, "online": False, "checking": True}
        self._checked = 0.0
        self._checking = False
        self._lock = threading.Lock()

    # ---- HTTP

    def _request(self, method: str, path: str, *, body=None, data: bytes | None = None,
                 headers: dict | None = None, timeout: float = 30):
        headers = dict(headers or {})
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(self.url + path, data=data, method=method, headers=headers)
        try:
            return self._opener.open(req, timeout=timeout)
        except urllib.error.HTTPError as exc:
            if exc.code == 401:
                raise AIServerError("AI 伺服器拒絕連線：token 不對（兩邊的 --token / SONG_AI_TOKEN 要相同）") from None
            raise

    def _json(self, method: str, path: str, **kwargs) -> dict:
        try:
            with self._request(method, path, **kwargs) as resp:
                return json.loads(resp.read() or b"{}")
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", "replace")
            try:
                detail = json.loads(detail).get("detail", detail)
            except ValueError:
                pass
            raise AIServerError(f"AI 伺服器回應錯誤（{exc.code}）：{detail}") from None

    # ---- 狀態

    def health(self, timeout: float = 3) -> dict:
        info = self._json("GET", "/v1/health", timeout=timeout)
        mine = versions()
        theirs = info.get("versions", {})
        if theirs != mine:
            diff = "、".join(f"{k} {theirs.get(k)}≠{v}" for k, v in mine.items() if theirs.get(k) != v)
            raise AIServerError(f"AI 伺服器的程式版本和這裡不同（{diff}），請把兩邊的程式更新到同一版")
        return info

    def status(self) -> dict:
        """給 UI 顯示的連線狀態；不會卡住：過期時在背景重新檢查，先回傳上次的結果。"""
        with self._lock:
            stale = time.time() - self._checked > 5
            if stale and not self._checking:
                self._checking = True
                threading.Thread(target=self._refresh, daemon=True, name="ai-health").start()
            return dict(self._status)

    def _refresh(self) -> None:
        try:
            info = self.health()
            status = {"url": self.url, "online": True, "device": info.get("device"), "gpu": info.get("gpu"),
                      "queued": info.get("queued", 0), "running": info.get("running")}
        except AIServerError as exc:
            status = {"url": self.url, "online": False, "error": str(exc)}
        except Exception:
            status = {"url": self.url, "online": False, "error": "連不上（AI 伺服器沒有啟動？）"}
        with self._lock:
            self._status, self._checked, self._checking = status, time.time(), False

    def _wait_online(self, log: Log, should_stop: Stop) -> None:
        told = False
        while True:
            try:
                self.health()
                if told:
                    log("  . AI 伺服器已連上")
                return
            except AIServerError:
                raise
            except Exception:
                if not told:
                    log(f"  . 連不上 AI 伺服器（{self.url}），等它啟動中…（請執行 ai.ps1；可以取消這件工作）")
                    told = True
            deadline = time.time() + self.RETRY
            while time.time() < deadline:
                if should_stop():
                    raise Cancelled()
                time.sleep(0.2)

    # ---- 檔案

    def _upload(self, path: Path, log: Log) -> str:
        sha1 = _sha1(path)
        try:
            with self._request("HEAD", f"/v1/blobs/{sha1}", timeout=10):
                return sha1      # AI 伺服器已經有這個檔案
        except urllib.error.HTTPError as exc:
            if exc.code != 404:
                raise
        size = path.stat().st_size
        log(f"  . 上傳音訊到 AI 伺服器（{size / 1e6:.1f} MB）")
        with path.open("rb") as f:
            with self._request("PUT", f"/v1/blobs/{sha1}", data=f, timeout=300,
                               headers={"Content-Type": "application/octet-stream", "Content-Length": str(size)}):
                pass
        return sha1

    def _download(self, task: str, name: str, dest: Path) -> None:
        with self._request("GET", f"/v1/tasks/{task}/files/{name}", timeout=300) as resp, dest.open("wb") as f:
            shutil.copyfileobj(resp, f, 1 << 20)

    # ---- 執行

    def run(self, kind: str, params: dict, inputs: dict[str, Path], out_dir: Path | None = None, *,
            log: Log, progress: Progress | None = None, should_stop: Stop | None = None,
            speech: bool = False) -> dict:
        """speech=True：輸入是人聲，先轉成 16kHz 單聲道（對時與檢查只需要這個）再上傳。"""
        should_stop = should_stop or (lambda: False)
        progress = progress or (lambda _v: None)
        self._wait_online(log, should_stop)
        with tempfile.TemporaryDirectory(prefix="ai_upload_") as tmp:
            refs = {}
            for name, path in inputs.items():
                if speech:
                    path = _speech_wav(path, Path(tmp) / f"{name}.wav")
                refs[name] = self._upload(path, log)
        task = self._json("POST", "/v1/tasks", body={"kind": kind, "params": params, "inputs": refs})
        tid, offset, lost, said_queued = task["id"], 0, None, False
        try:
            while True:
                if should_stop():
                    # 對時無法中途打斷：AI 伺服器會在這一步做完後丟掉結果，這裡不必等
                    self._cancel(tid)
                    raise Cancelled()
                try:
                    task = self._json("GET", f"/v1/tasks/{tid}?offset={offset}", timeout=10)
                    lost = None
                except AIServerError:
                    raise
                except Exception:
                    lost = lost or time.time()
                    if time.time() - lost > self.LOST:
                        raise AIServerError("和 AI 伺服器的連線中斷了，請確認它還在執行後重新排入")
                    time.sleep(1)
                    continue
                for line in task["logs"]:
                    log(line)
                offset = task["next"]
                if task["status"] == "queued" and task.get("position") and not said_queued:
                    log(f"  . AI 伺服器忙碌中，排在第 {task['position']} 位")
                    said_queued = True
                if task.get("progress") is not None:
                    progress(task["progress"])
                if task["status"] == "done":
                    break
                if task["status"] == "cancelled":
                    raise Cancelled()
                if task["status"] == "failed":
                    raise AIServerError(task.get("error") or "AI 伺服器處理失敗")
                time.sleep(self.POLL)
            result = task["result"]
            if out_dir is not None:
                out_dir.mkdir(parents=True, exist_ok=True)
                for name in result.get("files", []):
                    self._download(tid, name, out_dir / name)
            return result
        finally:
            try:
                self._json("DELETE", f"/v1/tasks/{tid}", timeout=5)
            except Exception:
                pass   # 沒刪到的話 AI 伺服器過一段時間會自己清掉

    def _cancel(self, tid: str) -> None:
        try:
            self._json("POST", f"/v1/tasks/{tid}/cancel", timeout=5)
        except Exception:
            pass


_backend: Local | Remote = Local()


def configure(url: str | None, token: str | None = None) -> Local | Remote:
    """指定後端：AI 伺服器的網址，或 "local"。"""
    global _backend
    url = (url or "").strip()
    if not url or url == "local":
        _backend = Local()
    else:
        _backend = Remote(url if "://" in url else "http://" + url, token)
    return _backend


def backend() -> Local | Remote:
    return _backend


def status() -> dict:
    return _backend.status()


# 命令列工具：有設 SONG_AI_URL 就交給 AI 伺服器，否則在同一個行程執行。
configure(os.environ.get("SONG_AI_URL"), os.environ.get("SONG_AI_TOKEN"))


# ---- 給處理流程用的函式 ----------------------------------------------------------

def separate(wav: Path, workdir: Path, *, stems: int, model: str, device: str, shifts: int, jobs: int,
             log: Log, progress: Progress | None = None, should_stop: Stop | None = None) -> Path:
    """Demucs 分離 wav，各分軌（vocals.wav、no_vocals.wav…）放進 workdir，回傳 workdir。"""
    _backend.run("separate", {"stems": stems, "model": model, "device": device, "shifts": shifts, "jobs": jobs},
                 {"audio": wav}, workdir, log=log, progress=progress, should_stop=should_stop)
    return workdir


def _lyrics_params(texts: list[str], rubies: list) -> dict:
    return {"texts": list(texts), "rubies": [[asdict(r) for r in line] for line in rubies]}


def align_song(vocals: Path, texts: list[str], rubies: list, *, model_name: str, language: str | None,
               device: str = "auto", log: Log, should_stop: Stop | None = None) -> list[dict]:
    """整首對時（align.align）。"""
    params = {**_lyrics_params(texts, rubies), "model": model_name, "language": language, "device": device}
    return _backend.run("align", params, {"audio": vocals}, log=log, should_stop=should_stop, speech=True)["lines"]


def align_from(vocals: Path, texts: list[str], rubies: list, first: int, anchor: float, *, model_name: str,
               language: str | None, device: str = "auto", log: Log, should_stop: Stop | None = None) -> list[dict]:
    """從第 first 句開始重新對時（align.align_from）。"""
    params = {**_lyrics_params(texts, rubies), "first": first, "anchor": anchor,
              "model": model_name, "language": language, "device": device}
    return _backend.run("align_from", params, {"audio": vocals}, log=log, should_stop=should_stop,
                        speech=True)["lines"]


def align_line(vocals: Path, text: str, rubies: list, t0: float, t1: float | None, *, language: str | None,
               device: str = "auto", log: Log, should_stop: Stop | None = None) -> dict:
    """只重對一句（align.align_line）。"""
    params = {**_lyrics_params([text], [rubies]), "t0": t0, "t1": t1, "language": language, "device": device}
    return _backend.run("align_line", params, {"audio": vocals}, log=log, should_stop=should_stop,
                        speech=True)["line"]


def check(lines: list[dict], texts: list[str], rubies: list, vocals: Path, language: str | None, *,
          model_name: str, device: str = "auto", log: Log, should_stop: Stop | None = None) -> dict:
    """對時檢查（qa.check）。"""
    params = {**_lyrics_params(texts, rubies), "lines": lines, "model": model_name,
              "language": language, "device": device}
    return _backend.run("qa", params, {"audio": vocals}, log=log, should_stop=should_stop, speech=True)["doc"]


# ---- 小工具 ------------------------------------------------------------------

def _sha1(path: Path) -> str:
    h = hashlib.sha1()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def _speech_wav(src: Path, dest: Path) -> Path:
    """人聲轉成 16kHz 單聲道 16-bit wav（和 Whisper / CTC 讀檔時的轉換相同）。
    -bitexact：同一個檔案每次轉出來完全一樣，AI 伺服器的快取才認得。"""
    run([FFMPEG, "-y", "-v", "error", "-i", str(src), "-vn", "-map", "0:a:0", "-ac", "1", "-ar", "16000",
         "-c:a", "pcm_s16le", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact",
         str(dest)])
    return dest


def same_token(given: str | None, expected: str | None) -> bool:
    """AI 伺服器檢查 Authorization 用（固定時間比較）。"""
    return not expected or hmac.compare_digest((given or "").encode(), f"Bearer {expected}".encode())
