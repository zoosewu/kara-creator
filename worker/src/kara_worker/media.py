"""ffmpeg / ffprobe：從 PATH 找（Windows 的 worker.ps1 會把 tools\\ffmpeg\\bin 加進 PATH）。"""
from __future__ import annotations

import shutil
import subprocess
from collections.abc import Callable

FFMPEG = shutil.which("ffmpeg") or "ffmpeg"
FFPROBE = shutil.which("ffprobe") or "ffprobe"


class Cancelled(Exception):
    """任務被取消（NAS 要求、改派或 worker 關閉）；一路往上拋，不當成失敗。"""


def run_cancellable(cmd: list[str], should_stop: Callable[[], bool] | None = None, **popen_kwargs) -> tuple[int, str]:
    """執行外部指令並回傳 (returncode, stderr)；should_stop() 為真時終止子程序並拋出 Cancelled。"""
    proc = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
                            text=True, encoding="utf-8", errors="replace", **popen_kwargs)
    try:
        while True:
            try:
                _, err = proc.communicate(timeout=0.5)
                return proc.returncode, err
            except subprocess.TimeoutExpired:
                if should_stop and should_stop():
                    raise Cancelled() from None
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait()
