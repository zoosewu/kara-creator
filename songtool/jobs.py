"""背景工作佇列，分成兩條：

    下載佇列  只用網路，不必等 AI；最多同時下載 DOWNLOAD_WORKERS 首。
    處理佇列  去人聲、對時、檢查對時等用到 GPU 的步驟（燒錄也在這裡），一次只做一首。
              用到 AI 模型的部分交給 AI 伺服器（songtool/ai.py），模型留在那邊的行程裡，不必重新載入。

同一個工作如果包含下載以外的步驟，會先在下載佇列下載，完成後自動移到處理佇列排隊。

一個工作由幾個步驟組成，可以只做其中一步，也可以一路做到底：
    download  下載網址（新歌放進 options["folder"] 指定的曲庫資料夾）
    separate  去人聲（人聲 / 伴奏分離）
    karaoke   製作伴唱帶（對時、字幕、燒錄；最後會自動做對時檢查）
    check     對時檢查（單獨重新檢查已經做好的伴唱帶）
    retime    以某句目前的開頭為準 AI 重新對時（options["line"]；options["mode"] = from / line）
options 另可帶 force（全部重做，包含覆蓋手動修改的字幕）與 realign（只重新對時）。

取消：排隊中的工作直接移出佇列；執行中的工作在下一個安全點停下
（下載中斷、Demucs 與 ffmpeg 子程序直接終止；對時無法中途打斷，這邊不等，AI 伺服器做完後丟掉結果）。
每個工作結束（含取消與失敗）後都會同步 output/export/。
"""
from __future__ import annotations

import queue
import threading
import time
import uuid
from dataclasses import dataclass, field
from typing import Callable

from . import catalog, export, karaoke, lyrics
from .download import Download, download, list_downloads
from .media import Cancelled
from .separate import SeparateOptions, separate_file

STEPS = ("download", "separate", "karaoke", "check", "retime")
STEP_NAMES = {"download": "下載", "separate": "去人聲", "karaoke": "製作伴唱帶", "check": "檢查對時",
              "retime": "AI 重新對時", "wrapup": "收尾"}
ACTIVE = ("queued", "running")
LANES = ("download", "process")
DOWNLOAD_WORKERS = 2


@dataclass
class Job:
    id: str
    steps: list[str]
    url: str | None = None
    item: str | None = None            # 下載資料夾名稱；下載步驟完成後才會知道
    title: str = ""
    options: dict = field(default_factory=dict)
    lyrics_text: str | None = None     # 一起送來的歌詞，下載完成後存檔
    status: str = "queued"             # queued / running / done / failed / cancelled
    lane: str = "process"              # 目前在哪條佇列：download / process；收尾工作（備份、hook）為 system
    stage: str = ""
    progress: float | None = None
    error: str | None = None
    cancel_requested: bool = False
    logs: list[str] = field(default_factory=list)
    created: float = field(default_factory=time.time)
    started: float | None = None
    finished: float | None = None

    def log(self, msg: str) -> None:
        self.logs.append(f"{time.strftime('%H:%M:%S')}  {msg}")

    def summary(self) -> dict:
        return {
            "id": self.id, "steps": self.steps, "url": self.url, "item": self.item, "lane": self.lane,
            "title": self.title, "status": self.status, "stage": self.stage,
            "progress": self.progress, "error": self.error, "log_size": len(self.logs),
            "cancelling": self.cancel_requested and self.status == "running",
            "force": bool(self.options.get("force")), "realign": bool(self.options.get("realign")),
            "created": self.created, "started": self.started, "finished": self.finished,
        }


class JobError(Exception):
    pass


class JobManager:
    def __init__(self, echo: Callable[[str], None] | None = None,
                 on_idle: Callable[[list[Job]], None] | None = None):
        """on_idle(jobs)：兩條佇列都清空（沒有排隊、也沒有處理中的工作）時呼叫一次，
        jobs 是這一輪結束的工作。在另一個執行緒呼叫，不會擋住新排入的工作。"""
        self._jobs: dict[str, Job] = {}
        self._on_idle = on_idle
        self._round: list[Job] = []
        self._idle_lock = threading.Lock()
        self._queues: dict[str, queue.Queue[Job]] = {lane: queue.Queue() for lane in LANES}
        self._echo = echo
        for n in range(DOWNLOAD_WORKERS):
            threading.Thread(target=self._worker, args=("download",), daemon=True,
                             name=f"song-download-{n}").start()
        threading.Thread(target=self._worker, args=("process",), daemon=True, name="song-process").start()

    def submit(self, steps: list[str], *, url: str | None = None, item: str | None = None,
               options: dict | None = None, lyrics_text: str | None = None) -> Job:
        steps = [s for s in STEPS if s in steps]
        if not steps:
            raise ValueError("至少要有一個步驟")
        if "download" in steps and not url:
            raise ValueError("下載需要網址")
        if "download" not in steps and not item:
            raise ValueError("需要指定歌曲")
        title = url or ""
        if item:
            found = _find(item)
            title = found.title if found else item
        job = Job(uuid.uuid4().hex[:8], steps, url, item, title, options or {}, lyrics_text)
        job.lane = "download" if "download" in steps else "process"
        extra = "（強制重做）" if job.options.get("force") else "（重新對時）" if job.options.get("realign") else ""
        where = "下載佇列" if job.lane == "download" else "處理佇列"
        job.log(f"排入{where}：{' → '.join(STEP_NAMES[s] for s in steps)}{extra}")
        self._jobs[job.id] = job
        self._queues[job.lane].put(job)
        return job

    def cancel(self, job_id: str) -> Job:
        job = self._jobs.get(job_id)
        if job is None:
            raise KeyError("找不到工作")
        if job.status == "queued":
            # 還沒開始：標記後 worker 取出時會直接略過。
            job.status, job.finished = "cancelled", time.time()
            job.log("已取消（尚未開始）")
            self._finished(job)
        elif job.status == "running" and not job.cancel_requested:
            job.cancel_requested = True
            job.log("取消中：會在下一個安全點停下…")
        return job

    def shutdown(self, timeout: float = 5.0) -> list[Job]:
        """關閉程式前呼叫：取消所有排隊中與處理中的工作，最多等 timeout 秒讓處理中的停下
        （Demucs / ffmpeg 子程序會被終止，不會留在背景）。回傳等不到停下的工作（例如正在對時）。"""
        active = [job for job in self._jobs.values() if job.status in ACTIVE]
        for job in active:
            self.cancel(job.id)
        deadline = time.time() + timeout
        while time.time() < deadline and any(job.status == "running" for job in active):
            time.sleep(0.1)
        return [job for job in active if job.status == "running"]

    def get(self, job_id: str) -> Job | None:
        return self._jobs.get(job_id)

    def list(self) -> list[dict]:
        return [job.summary() for job in sorted(self._jobs.values(), key=lambda j: -j.created)]

    def busy_items(self) -> dict[str, dict]:
        """尚未結束的工作所對應的歌曲：{資料夾名稱: 工作摘要}。"""
        return {job.item: job.summary() for job in self._jobs.values()
                if job.item and job.status in ACTIVE}

    def _worker(self, lane: str) -> None:
        while True:
            job = self._queues[lane].get()
            if job.status == "cancelled":
                continue
            job.status, job.started = "running", job.started or time.time()
            outcome, error = "done", None
            try:
                if lane == "download":
                    self._download(job)
                    if job.cancel_requested:
                        raise Cancelled()
                    if any(step != "download" for step in job.steps):
                        # 還有 AI 處理：換到處理佇列排隊，這條下載線可以接著下載下一首。
                        job.lane, job.status, job.stage, job.progress = "process", "queued", "", None
                        job.log("下載完成，移到處理佇列排隊")
                        self._queues["process"].put(job)
                        continue
                else:
                    self._process(job)
            except Cancelled:
                outcome = "cancelled"
            except Exception as exc:
                outcome, error = "failed", str(exc)
            # 先同步輸出資料夾再標記結束，讓「完成」時輸出檔已經就位。只下載不會產生新的輸出。
            if lane == "process":
                job.stage, job.progress = "輸出", None
                self._export(job)
            if outcome == "failed":
                job.error = error
                job.log(f"失敗：{error}")
            else:
                job.log("已取消" if outcome == "cancelled" else "完成")
            job.finished, job.progress, job.stage = time.time(), None, ""
            job.status = outcome
            self._finished(job)

    def idle(self) -> bool:
        """兩條佇列都沒有排隊或處理中的工作（收尾工作不算）。"""
        return not any(job.status in ACTIVE and job.lane != "system" for job in self._jobs.values())

    def _finished(self, job: Job) -> None:
        """一件工作結束；這是最後一件的話（佇列清空）通知 on_idle。"""
        with self._idle_lock:
            self._round.append(job)
            if not self.idle():
                return
            finished, self._round = self._round, []
        if self._on_idle:
            threading.Thread(target=self._on_idle, args=(finished,), daemon=True, name="song-idle").start()

    def run_system(self, title: str, fn: Callable[[Callable[[str], None]], None]) -> Job:
        """收尾工作（備份、使用者的 hook）：在自己的執行緒執行，紀錄一樣顯示在處理佇列裡，
        不佔用下載 / AI 處理佇列，也不算進「佇列清空」的判斷。"""
        job = Job(uuid.uuid4().hex[:8], ["wrapup"], title=title)
        job.lane, job.status, job.started, job.stage = "system", "running", time.time(), STEP_NAMES["wrapup"]
        self._jobs[job.id] = job
        log, _, _ = self._context(job)

        def run() -> None:
            try:
                fn(log)
                job.status = "done"
            except Exception as exc:
                job.error, job.status = str(exc), "failed"
                log(f"失敗：{exc}")
            job.finished, job.stage = time.time(), ""

        threading.Thread(target=run, daemon=True, name="song-wrapup").start()
        return job

    def _context(self, job: Job):
        def log(msg: str) -> None:
            job.log(msg)
            if self._echo:
                self._echo(f"[{job.id}] {msg}")

        def progress(value: float) -> None:
            job.progress = max(0.0, min(1.0, value))

        def should_stop() -> bool:
            return job.cancel_requested

        return log, progress, should_stop

    def _download(self, job: Job) -> None:
        log, progress, should_stop = self._context(job)
        opts = job.options
        item: Download | None = None
        job.stage, job.progress = STEP_NAMES["download"], 0.0
        log(f"下載 {job.url}")
        for result in download([job.url], audio_only=bool(opts.get("audio_only")),
                               progress_hooks=[_download_hook(progress, should_stop)], log=log):
            if result.status == "failed":
                if should_stop():
                    raise Cancelled()  # 取消造成的下載中斷不算失敗
                raise JobError(f"下載失敗：{result.error}")
            item = result.item
            log("已下載過，略過下載" if result.status == "skipped" else f"下載完成：{item.file.name}")
        if item is None:
            raise JobError("網址沒有可下載的內容")
        job.item, job.title = item.name, item.title
        with catalog.edit() as cat:
            cat.ensure_song(catalog.song_key(item), folder=opts.get("folder"))
        self._save_lyrics(job, item, log)

    @staticmethod
    def _save_lyrics(job: Job, item: Download, log: Callable[[str], None]) -> None:
        if job.lyrics_text and job.lyrics_text.strip():
            path = lyrics.save(item, job.lyrics_text)
            log(f"歌詞已存到 {path}")
        job.lyrics_text = None

    def _process(self, job: Job) -> None:
        log, progress, should_stop = self._context(job)

        def check() -> None:
            if job.cancel_requested:
                raise Cancelled()

        opts = job.options
        force = bool(opts.get("force"))
        item = _find(job.item)
        if item is None:
            raise JobError(f"找不到歌曲：{job.item}")
        self._save_lyrics(job, item, log)

        if "separate" in job.steps:
            check()
            job.stage, job.progress = STEP_NAMES["separate"], 0.0
            log("去人聲（人聲 / 伴奏分離）" + ("，強制重做" if force else ""))
            stems = int(opts.get("stems", 2))
            result = separate_file(item.file, SeparateOptions(stems=stems), force=force, log=log,
                                   progress=progress, should_stop=should_stop)
            if result.status == "failed":
                raise JobError(f"去人聲失敗：{result.error}")
            log("已去過人聲，略過" if result.status == "skipped" else "去人聲完成")

        if "karaoke" in job.steps:
            check()
            job.stage, job.progress = STEP_NAMES["karaoke"], None
            if lyrics.find(item) is None:
                log(f"缺歌詞，暫不製作伴唱帶。請先輸入歌詞（會存到 {lyrics.candidates(item)[0]}）")
                return
            log("製作伴唱帶")
            target = opts.get("target", "instrumental")
            targets = ("instrumental", "original") if target == "both" else (target,)
            result = karaoke.make(item, karaoke.KaraokeOptions(
                targets=targets, realign=bool(opts.get("realign")), force=force),
                log=log, should_stop=should_stop)
            if result.status == "failed":
                raise JobError(f"製作伴唱帶失敗：{result.error}")
            log("伴唱帶已是最新，略過" if result.status == "skipped" else "伴唱帶完成")

        if "check" in job.steps:
            check()
            job.stage, job.progress = STEP_NAMES["check"], None
            log("對時檢查（本機獨立聽寫比對）")
            karaoke.check_timing(item, log=log, should_stop=should_stop, force=force)

        if "retime" in job.steps:
            check()
            job.stage, job.progress = STEP_NAMES["retime"], None
            karaoke.retime(item, int(opts.get("line", 0)), opts.get("mode", "from"),
                           log=log, should_stop=should_stop)

    def _export(self, job: Job) -> None:
        try:
            result = export.sync()
        except Exception as exc:  # 輸出失敗不影響工作本身的結果
            job.log(f"同步輸出資料夾失敗：{exc}")
            return
        for rel in result.added:
            job.log(f"輸出：export/{rel}")
        for rel in result.skipped:
            job.log(f"輸出略過（已有同名檔案）：export/{rel}")


def _find(name: str | None) -> Download | None:
    return next((d for d in list_downloads() if d.name == name), None)


def _download_hook(progress: Callable[[float], None],
                   should_stop: Callable[[], bool]) -> Callable[[dict], None]:
    def hook(d: dict) -> None:
        if should_stop():
            raise Cancelled()  # yt-dlp 會中斷下載；.part 暫存檔留著，下次可以續傳
        total = d.get("total_bytes") or d.get("total_bytes_estimate")
        if d.get("status") == "downloading" and total:
            progress(d.get("downloaded_bytes", 0) / total)
    return hook
