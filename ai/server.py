#!/usr/bin/env python3
"""AI 伺服器：去人聲（Demucs）、對時（Whisper + CTC）、對時檢查。給歌曲伺服器（ui/server.py）呼叫。

預設只監聽 127.0.0.1:8770（和歌曲伺服器在同一台電腦）；在另一台有顯示卡的電腦執行時加 --lan，
歌曲伺服器啟動時用 --ai http://這台的位址:8770 指過來。

    python ai/server.py                    # 本機
    python ai/server.py --lan --token 密碼  # 開放區域網路（歌曲伺服器要用同一個 --ai-token）

一次只處理一件工作（避免模型搶顯示卡記憶體），Whisper / MMS 模型留在這個行程裡重複使用。
上傳的音訊以內容的 sha1 存在快取資料夾（預設 output/ai-cache，SONG_AI_CACHE 可改），一天沒用到就刪掉。

協定（/v1，歌曲伺服器的 songtool/ai.py 是唯一的使用者）：
    GET    /v1/health                 版本、顯示卡、佇列
    HEAD   /v1/blobs/{sha1}           快取裡有沒有這個檔案
    PUT    /v1/blobs/{sha1}           上傳檔案（內容的 sha1 要相符）
    POST   /v1/tasks                  排入工作 {kind, params, inputs: {名稱: sha1}}
    GET    /v1/tasks/{id}?offset=N    狀態、進度、第 N 行之後的紀錄、結果
    GET    /v1/tasks/{id}/files/{名稱} 工作產生的檔案（去人聲的分軌）
    POST   /v1/tasks/{id}/cancel      取消
    DELETE /v1/tasks/{id}             取完結果後刪除
"""
from __future__ import annotations

import argparse
import hashlib
import os
import queue
import re
import shutil
import sys
import threading
import time
import uuid
from contextlib import asynccontextmanager
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from fastapi import Depends, FastAPI, HTTPException, Request  # noqa: E402
from fastapi.responses import FileResponse, Response  # noqa: E402
from pydantic import BaseModel  # noqa: E402

from songtool import ai, config, net  # noqa: E402
from songtool.media import Cancelled  # noqa: E402

CACHE_DIR = Path(os.environ.get("SONG_AI_CACHE") or config.OUTPUT_DIR / "ai-cache")
BLOBS = CACHE_DIR / "blobs"
WORK = CACHE_DIR / "tasks"
BLOB_TTL = 24 * 3600        # 上傳的音訊多久沒用到就刪掉（秒）
TASK_TTL = 3600             # 結束的工作（含產生的檔案）保留多久（秒）
MAX_UPLOAD = 4 << 30
_SHA1 = re.compile(r"[0-9a-f]{40}")

TOKEN: str | None = None
DEVICE = {"device": None, "gpu": None}


class Task:
    def __init__(self, kind: str, params: dict, inputs: dict[str, str]):
        self.id = uuid.uuid4().hex[:12]
        self.kind, self.params, self.inputs = kind, params, inputs
        self.status = "queued"            # queued / running / done / failed / cancelled
        self.progress: float | None = None
        self.logs: list[str] = []
        self.result: dict | None = None
        self.error: str | None = None
        self.cancel = False
        self.created, self.finished = time.time(), None
        self.dir = WORK / self.id

    def log(self, msg: str) -> None:
        self.logs.append(msg)
        print(f"[{self.id} {self.kind}] {msg}", flush=True)


class Runner:
    """一條佇列、一個執行緒：一次只跑一件工作。"""

    def __init__(self):
        self.tasks: dict[str, Task] = {}
        self._queue: queue.Queue[Task] = queue.Queue()
        self._order: list[str] = []         # 排隊中的工作（算排第幾位用）
        threading.Thread(target=self._worker, daemon=True, name="ai-worker").start()

    def submit(self, task: Task) -> Task:
        self.tasks[task.id] = task
        self._order.append(task.id)
        self._queue.put(task)
        return task

    def position(self, task: Task) -> int | None:
        return self._order.index(task.id) + 1 if task.status == "queued" and task.id in self._order else None

    def running(self) -> Task | None:
        return next((t for t in self.tasks.values() if t.status == "running"), None)

    def _worker(self) -> None:
        while True:
            task = self._queue.get()
            if task.id in self._order:
                self._order.remove(task.id)
            if task.cancel:
                task.status, task.finished = "cancelled", time.time()
                continue
            task.status = "running"
            try:
                inputs = {}
                for name, sha1 in task.inputs.items():
                    path = BLOBS / sha1
                    os.utime(path)          # 用到了：延後刪除
                    inputs[name] = path
                out = task.dir / "out"
                out.mkdir(parents=True, exist_ok=True)

                def progress(value: float) -> None:
                    task.progress = max(0.0, min(1.0, value))

                result = ai.execute(task.kind, task.params, inputs, out, log=task.log, progress=progress,
                                    should_stop=lambda: task.cancel)
                if task.cancel:
                    raise Cancelled()       # 對時無法中途打斷：做完才發現被取消，結果不要了
                task.result, task.status = result, "done"
            except Cancelled:
                task.status = "cancelled"
                task.log("已取消")
            except Exception as exc:
                task.error, task.status = str(exc), "failed"
                task.log(f"失敗：{exc}")
            task.finished = time.time()
            if task.id not in self.tasks:   # 處理中就被刪除（歌曲伺服器取消了）：結果不要了
                shutil.rmtree(task.dir, ignore_errors=True)

    def cancel_all(self, timeout: float = 5.0) -> None:
        """關閉前呼叫：取消全部，最多等 timeout 秒讓處理中的停下（Demucs 子程序會被終止）。"""
        for task in self.tasks.values():
            task.cancel = True
        deadline = time.time() + timeout
        while time.time() < deadline and self.running():
            time.sleep(0.1)
        if self.running():
            print("對時中的工作無法立即中斷，直接結束", flush=True)

    def cleanup(self) -> None:
        """刪掉過期的工作與快取。"""
        now = time.time()
        for task in list(self.tasks.values()):
            if task.finished and now - task.finished > TASK_TTL:
                self.remove(task)
        for path in BLOBS.glob("*"):
            try:
                if now - path.stat().st_mtime > BLOB_TTL:
                    path.unlink()
            except OSError:
                pass

    def remove(self, task: Task) -> None:
        task.cancel = True
        self.tasks.pop(task.id, None)
        if task.status != "running":
            shutil.rmtree(task.dir, ignore_errors=True)


runner = Runner()


@asynccontextmanager
async def _lifespan(_app: FastAPI):
    BLOBS.mkdir(parents=True, exist_ok=True)
    shutil.rmtree(WORK, ignore_errors=True)       # 上次留下的工作暫存
    WORK.mkdir(parents=True, exist_ok=True)
    threading.Thread(target=_detect_device, daemon=True).start()
    threading.Thread(target=_cleanup_loop, daemon=True).start()
    yield
    runner.cancel_all()


def _detect_device() -> None:
    try:
        import torch
        if torch.cuda.is_available():
            DEVICE.update(device="cuda", gpu=torch.cuda.get_device_name(0))
        else:
            DEVICE.update(device="cpu")
    except Exception as exc:
        DEVICE.update(device="unavailable", gpu=f"無法載入 PyTorch：{exc}")
    print(f"運算裝置：{DEVICE['gpu'] or DEVICE['device']}", flush=True)


def _cleanup_loop() -> None:
    while True:
        time.sleep(600)
        runner.cleanup()


def _auth(request: Request) -> None:
    if not ai.same_token(request.headers.get("authorization"), TOKEN):
        raise HTTPException(401, "token 不對")


app = FastAPI(title="kara-creator AI", lifespan=_lifespan, dependencies=[Depends(_auth)],
              docs_url=None, redoc_url=None, openapi_url=None)


def _task(task_id: str) -> Task:
    task = runner.tasks.get(task_id)
    if task is None:
        raise HTTPException(404, "找不到工作（可能已經過期被清掉）")
    return task


@app.get("/v1/health")
def health() -> dict:
    running = runner.running()
    return {"name": "kara-creator AI", "versions": ai.versions(), **DEVICE,
            "queued": sum(1 for t in runner.tasks.values() if t.status == "queued"),
            "running": running.kind if running else None}


def _blob(sha1: str) -> Path:
    if not _SHA1.fullmatch(sha1):
        raise HTTPException(400, "sha1 格式不對")
    return BLOBS / sha1


@app.head("/v1/blobs/{sha1}")
def has_blob(sha1: str) -> Response:
    path = _blob(sha1)
    if not path.is_file():
        raise HTTPException(404)
    os.utime(path)
    return Response()


@app.put("/v1/blobs/{sha1}")
async def put_blob(sha1: str, request: Request) -> dict:
    path = _blob(sha1)
    tmp = path.with_name(f"{sha1}.{uuid.uuid4().hex[:6]}.part")
    h, size = hashlib.sha1(), 0
    try:
        with tmp.open("wb") as f:
            async for chunk in request.stream():
                size += len(chunk)
                if size > MAX_UPLOAD:
                    raise HTTPException(413, "檔案太大")
                h.update(chunk)
                f.write(chunk)
        if h.hexdigest() != sha1:
            raise HTTPException(400, "內容和 sha1 不符（傳輸中斷？）")
        tmp.replace(path)
    finally:
        tmp.unlink(missing_ok=True)
    return {"sha1": sha1, "size": size}


class TaskBody(BaseModel):
    kind: str
    params: dict = {}
    inputs: dict[str, str] = {}


@app.post("/v1/tasks")
def create_task(body: TaskBody) -> dict:
    if body.kind not in ai.KINDS:
        raise HTTPException(400, f"不支援的工作：{body.kind}")
    for sha1 in body.inputs.values():
        if not _blob(sha1).is_file():
            raise HTTPException(409, "輸入檔還沒上傳")
    task = runner.submit(Task(body.kind, body.params, body.inputs))
    return {"id": task.id, "status": task.status}


@app.get("/v1/tasks/{task_id}")
def get_task(task_id: str, offset: int = 0) -> dict:
    task = _task(task_id)
    return {"id": task.id, "kind": task.kind, "status": task.status, "progress": task.progress,
            "position": runner.position(task), "logs": task.logs[offset:], "next": len(task.logs),
            "result": task.result if task.status == "done" else None, "error": task.error}


@app.get("/v1/tasks/{task_id}/files/{name}")
def task_file(task_id: str, name: str) -> FileResponse:
    task = _task(task_id)
    if task.status != "done" or name not in (task.result or {}).get("files", []):
        raise HTTPException(404)
    return FileResponse(task.dir / "out" / name)


@app.post("/v1/tasks/{task_id}/cancel")
def cancel_task(task_id: str) -> dict:
    task = _task(task_id)
    task.cancel = True
    return {"id": task.id, "status": task.status}


@app.delete("/v1/tasks/{task_id}")
def delete_task(task_id: str) -> dict:
    runner.remove(_task(task_id))
    return {"deleted": task_id}


def main() -> None:
    import uvicorn

    global TOKEN
    parser = argparse.ArgumentParser(description="kara-creator AI 伺服器")
    parser.add_argument("--port", type=int, default=8770)
    parser.add_argument("--lan", action="store_true", help="開放區域網路裡的其他電腦連線（預設只有本機）")
    parser.add_argument("--token", default=os.environ.get("SONG_AI_TOKEN"),
                        help="連線密碼；歌曲伺服器要用同一個 --ai-token（也可以用環境變數 SONG_AI_TOKEN）")
    args = parser.parse_args()
    TOKEN = args.token or None

    print(f"AI 伺服器：http://127.0.0.1:{args.port}/（Ctrl+C 結束）", flush=True)
    if args.lan:
        for ip in net.lan_addresses():
            print(f"  區域網路：http://{ip}:{args.port}/（歌曲伺服器：.\\ui.ps1 --ai http://{ip}:{args.port}）",
                  flush=True)
        if not TOKEN:
            print("  注意：沒有設定 --token，同一個網路裡的任何人都能使用這台的顯示卡；"
                  "第一次開放時 Windows 防火牆會詢問，請只允許「私人網路」。", flush=True)
    print(f"  快取：{CACHE_DIR}", flush=True)
    host = "0.0.0.0" if args.lan else "127.0.0.1"
    server = uvicorn.Server(uvicorn.Config(app, host=host, port=args.port, log_level="warning",
                                           timeout_graceful_shutdown=1))
    try:
        server.run()
    except KeyboardInterrupt:
        pass
    print("AI 伺服器已關閉", flush=True)


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass
    main()
