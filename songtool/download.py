"""用 yt-dlp 下載到 output/downloads/，每個項目一個資料夾。

資料夾內的 download.json 記錄來源網站與影片 id。下載前會先比對，
已下載過的項目直接略過；中途失敗的項目沒有 download.json，下次會重新下載。
"""
from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Iterable, Iterator

import yt_dlp

from . import config, manifest
from .media import MEDIA_EXTS

# 優先 H.264 + AAC，相容性最好；沒有時才退而求其次。
VIDEO_FORMAT = "bv*[vcodec^=avc1]+ba[ext=m4a]/bv*[ext=mp4]+ba[ext=m4a]/bv*+ba/b"
# 音訊直接取原始串流，不轉檔，保留原音質。
AUDIO_FORMAT = "ba[ext=m4a]/ba/b"
# download.json 內歌曲資訊欄位的版本；舊版紀錄會在 refresh_metadata() 時補抓。
METADATA_VERSION = 1

# 資料夾名稱帶 id，確保同名歌曲不會互相覆蓋；檔名則只用標題，比較好讀。
FOLDER_TEMPLATE = "%(title).80B (%(id)s)"
FILE_TEMPLATE = "%(title).80B.%(ext)s"


@dataclass
class Download:
    """一個已完成的下載項目：output/downloads/<name>/。"""
    folder: Path
    info: dict

    @property
    def name(self) -> str:
        return self.folder.name

    @property
    def title(self) -> str:
        return self.info.get("title") or self.folder.name

    @property
    def file(self) -> Path:
        return self.folder / self.info["file"]

    @property
    def key(self) -> tuple[str | None, str | None]:
        return self.info.get("extractor"), self.info.get("id")


@dataclass
class DownloadResult:
    url: str
    status: str  # "downloaded" | "skipped" | "failed"
    title: str = ""
    item: Download | None = None
    error: str | None = None


def list_downloads(root: Path | None = None) -> list[Download]:
    """列出 root 底下所有完整的下載項目（有紀錄檔且媒體檔還在）。"""
    root = root or config.DOWNLOADS_DIR
    if not root.is_dir():
        return []
    items = []
    for folder in sorted(root.iterdir()):
        info = manifest.read(folder / manifest.DOWNLOAD)
        if info and info.get("file") and (folder / info["file"]).is_file():
            items.append(Download(folder, info))
    return items


def download(
    urls: Iterable[str],
    *,
    audio_only: bool = False,
    playlist: bool = False,
    force: bool = False,
    progress_hooks: list[Callable[[dict], None]] | None = None,
    log: Callable[[str], None] | None = None,
) -> Iterator[DownloadResult]:
    """逐一下載，每處理完一個項目就 yield 一個結果。

    playlist=False 時，網址就算帶有 list= 參數也只下載該支影片。
    progress_hooks 直接交給 yt-dlp，UI 可用來顯示下載進度。
    log 有給時，yt-dlp 的訊息改送到 log（不再印到終端機，也不印進度條）。
    """
    config.DOWNLOADS_DIR.mkdir(parents=True, exist_ok=True)
    existing = {item.key: item for item in list_downloads()}

    with yt_dlp.YoutubeDL(_options(audio_only, playlist, progress_hooks, log)) as ydl:
        for url in urls:
            try:
                info = ydl.extract_info(url, download=False)
            except Exception as exc:
                yield DownloadResult(url, "failed", error=_message(exc))
                continue
            entries = info.get("entries") if info.get("_type") == "playlist" else [info]
            for entry in entries or []:
                if entry:
                    yield _download_entry(ydl, entry, existing, audio_only, force)


def _options(audio_only: bool, playlist: bool, progress_hooks, log=None) -> dict:
    opts = {
        "outtmpl": {"default": str(config.DOWNLOADS_DIR / FOLDER_TEMPLATE / FILE_TEMPLATE)},
        "windowsfilenames": True,
        "format": AUDIO_FORMAT if audio_only else VIDEO_FORMAT,
        "noplaylist": not playlist,
        # 播放清單只先取得各項目的 id，已下載過的就不必再解析完整資訊。
        "extract_flat": "in_playlist",
        "progress_hooks": list(progress_hooks or []),
    }
    if not audio_only:
        opts["merge_output_format"] = "mp4"
    if config.FFMPEG_BIN.is_dir():
        opts["ffmpeg_location"] = str(config.FFMPEG_BIN)
    if log:
        opts["logger"] = _Logger(log)
        opts["noprogress"] = True
    return opts


class _Logger:
    """yt-dlp 的 logger 介面，把訊息轉給 log。"""

    def __init__(self, log: Callable[[str], None]):
        self._log = log
        self._last_warning = ""

    def debug(self, msg: str) -> None:
        # yt-dlp 一般的畫面訊息也走 debug，只濾掉真正的除錯訊息；
        # 有些警告會同時從 warning 與 debug 各送一次，重複的略過。
        if not msg.startswith("[debug] ") and msg != self._last_warning:
            self._log(f"    {msg}")

    def info(self, msg: str) -> None:
        self._log(f"    {msg}")

    def warning(self, msg: str) -> None:
        self._last_warning = msg
        self._log(f"    WARNING: {msg}")

    def error(self, msg: str) -> None:
        self._log(f"    {msg}")


def _download_entry(ydl, entry: dict, existing: dict, audio_only: bool,
                    force: bool) -> DownloadResult:
    url = entry.get("webpage_url") or entry.get("url")
    key = (entry.get("extractor_key") or entry.get("ie_key"), entry.get("id"))
    title = entry.get("title") or entry.get("id") or url

    old = existing.get(key)
    if old and not force:
        return DownloadResult(url, "skipped", old.title, old)

    try:
        if old:
            # 強制重下時先移除舊檔，避免資料夾內同時留著舊的影片與新的音訊。
            (old.folder / manifest.DOWNLOAD).unlink(missing_ok=True)
            old.file.unlink(missing_ok=True)
        if entry.get("formats"):
            # 單支影片在比對 id 時已完整解析過，直接拿來下載，省掉第二次解析。
            full = ydl.process_ie_result(entry, download=True)
        else:
            full = ydl.extract_info(url, download=True)
        folder = Path(ydl.prepare_filename(full)).parent
        media = _find_media(folder)
        if media is None:
            raise RuntimeError(f"下載完成但找不到媒體檔: {folder}")
        info = {
            "extractor": full.get("extractor_key"),
            "id": full.get("id"),
            "url": full.get("webpage_url") or url,
            "title": full.get("title"),
            "uploader": full.get("uploader"),
            "duration": full.get("duration"),
            **_song_fields(full),
            "mode": "audio" if audio_only else "video",
            "file": media.name,
            "downloaded_at": manifest.now(),
        }
        manifest.write(folder / manifest.DOWNLOAD, info)
    except Exception as exc:
        return DownloadResult(url, "failed", title, error=_message(exc))

    item = Download(folder, info)
    existing[item.key] = item
    return DownloadResult(url, "downloaded", item.title, item)


def _song_fields(full: dict) -> dict:
    """yt-dlp 提供的歌曲資訊，自動辨識歌名與演唱者時使用（見 titles.py）。"""
    artists = full.get("artists") or ([full["artist"]] if full.get("artist") else None)
    return {"track": full.get("track"), "artists": artists, "channel": full.get("channel"),
            "meta_version": METADATA_VERSION}


def refresh_metadata(items: list[Download] | None = None) -> int:
    """替舊版下載紀錄補抓歌曲資訊（不重新下載影片），回傳更新的筆數。"""
    targets = [i for i in (items if items is not None else list_downloads())
               if i.info.get("meta_version") != METADATA_VERSION and i.info.get("url")]
    if not targets:
        return 0
    opts = {"quiet": True, "no_warnings": True, "skip_download": True, "noplaylist": True}
    updated = 0
    with yt_dlp.YoutubeDL(opts) as ydl:
        for item in targets:
            try:
                full = ydl.extract_info(item.info["url"], download=False)
            except Exception:
                continue  # 網路問題或影片下架：下次啟動再試
            manifest.write(item.folder / manifest.DOWNLOAD, {**item.info, **_song_fields(full)})
            updated += 1
    return updated


def _find_media(folder: Path) -> Path | None:
    """取資料夾內最新的媒體檔（合併後的暫存分軌 yt-dlp 會自行刪除）。"""
    candidates = [p for p in folder.iterdir()
                  if p.is_file() and p.suffix.lower() in MEDIA_EXTS]
    return max(candidates, key=lambda p: p.stat().st_mtime, default=None)


def _message(exc: Exception) -> str:
    text = str(exc).strip().splitlines()
    return text[0].removeprefix("ERROR: ") if text else type(exc).__name__
