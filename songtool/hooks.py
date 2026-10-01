"""收尾：佇列清空（或曲庫有變動、安靜一段時間後）執行使用者的 hook 腳本。

把腳本放在 hooks/on_idle.ps1（不進 git，範例見 hooks/on_idle.example.ps1），例如用 rsync 把
output/export/ 同步到 NAS。腳本執行時會帶這些環境變數：

    SONG_HOOK_REASON    jobs_finished（佇列清空）/ library_changed（改了歌名、資料夾、歌詞或時間）
    SONG_EXPORT_DIR     伴唱帶成品資料夾（output/export）
    SONG_OUTPUT_DIR     輸出根目錄
    SONG_DATA_DIR       資料備份（data/）
    SONG_HOOK_SUMMARY   這一輪的結果 JSON 檔（每件工作的歌名、步驟、狀態）
    SONG_JOBS_DONE / SONG_JOBS_FAILED / SONG_JOBS_CANCELLED   這一輪各狀態的件數

腳本的輸出會記在 UI 處理佇列的「收尾」紀錄裡；超過 HOOK_TIMEOUT 秒就中斷。
"""
from __future__ import annotations

import os
import shutil
import subprocess
import threading
from typing import Callable

from . import config, manifest

HOOK_SCRIPT = "on_idle.ps1"
HOOK_TIMEOUT = 30 * 60
SUMMARY_FILE = ".hook-summary.json"


def run_user_hook(reason: str, jobs: list[dict], log: Callable[[str], None] = print) -> None:
    script = config.HOOKS_DIR / HOOK_SCRIPT
    if not script.is_file():
        return
    counts = {status: sum(1 for j in jobs if j.get("status") == status) for status in ("done", "failed", "cancelled")}
    summary_path = config.OUTPUT_DIR / SUMMARY_FILE
    manifest.write(summary_path, {"reason": reason, "finished_at": manifest.now(), **counts, "jobs": jobs})
    env = dict(os.environ,
               SONG_HOOK_REASON=reason,
               SONG_EXPORT_DIR=str(config.EXPORT_DIR),
               SONG_OUTPUT_DIR=str(config.OUTPUT_DIR),
               SONG_DATA_DIR=str(config.DATA_DIR),
               SONG_HOOK_SUMMARY=str(summary_path),
               SONG_JOBS_DONE=str(counts["done"]),
               SONG_JOBS_FAILED=str(counts["failed"]),
               SONG_JOBS_CANCELLED=str(counts["cancelled"]))
    shell = shutil.which("pwsh") or shutil.which("powershell") or "powershell"
    log(f"  . 執行 hook：{script.name}（{reason}）")
    try:
        result = subprocess.run([shell, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(script)],
                                cwd=config.PROJECT_ROOT, env=env, capture_output=True, text=True,
                                encoding="utf-8", errors="replace", timeout=HOOK_TIMEOUT)
    except subprocess.TimeoutExpired:
        log(f"  [!] hook 超過 {HOOK_TIMEOUT // 60} 分鐘沒有結束，已中斷")
        return
    for line in (result.stdout + result.stderr).splitlines():
        if line.strip():
            log(f"    {line.rstrip()}")
    if result.returncode != 0:
        raise RuntimeError(f"hook 結束代碼 {result.returncode}")
    log("  . hook 完成")


def job_summaries(jobs) -> list[dict]:
    return [{"id": j.id, "title": j.title, "item": j.item, "steps": j.steps, "status": j.status,
             "error": j.error} for j in jobs]


class Debouncer:
    """touch() 之後安靜 delay 秒才呼叫 fn；期間再 touch 就重新計時（連續改名只跑一次）。"""

    def __init__(self, delay: float, fn: Callable[[], None]):
        self.delay, self.fn = delay, fn
        self._timer: threading.Timer | None = None
        self._lock = threading.Lock()

    def touch(self) -> None:
        with self._lock:
            if self._timer:
                self._timer.cancel()
            self._timer = threading.Timer(self.delay, self.fn)
            self._timer.daemon = True
            self._timer.start()

