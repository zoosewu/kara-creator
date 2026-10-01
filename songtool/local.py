"""把手動放進 output/downloads/ 的影音檔登記成曲庫項目。

支援兩種放法：
- 直接放在 downloads/ 底下的檔案：建立「檔名 (local-xxxxxxxx)」資料夾並把檔案移進去
- 放在自己建的子資料夾裡（還沒有 download.json）：就地登記資料夾裡的第一個影音檔
登記後就和下載的歌一樣可以去人聲、製作伴唱帶；歌名與歌手從檔名自動辨識
（「周杰倫 - 晴天.mp4」這類檔名最準），也可以在「資訊」修改。

為了不匯入還沒寫完的檔案：檔案大小要連續 5 秒沒變、而且沒有被其他程式佔用（複製中的檔案
Windows 會鎖住）；yt-dlp 下載中的暫存檔（.part、.ytdl、.f137.mp4 這類分段檔）一律略過。
"""
from __future__ import annotations

import hashlib
import os
import re
import time
from pathlib import Path
from typing import Callable

from . import config, manifest
from .download import METADATA_VERSION, Download, list_downloads
from .media import MEDIA_EXTS, duration, has_video_stream

_STABLE_SECONDS = 5.0
_PARTIAL = re.compile(r"\.(part|ytdl|temp)$|\.f\d+\.\w+$", re.IGNORECASE)

_seen: dict[str, tuple[int, float]] = {}   # 路徑 -> (大小, 第一次看到這個大小的時間)
_warned: set[str] = set()


def import_local(log: Callable[[str], None] | None = None) -> list[Download]:
    """掃描下載資料夾，登記手動放入的檔案，回傳這次新登記的項目。"""
    root = config.DOWNLOADS_DIR
    if not root.is_dir():
        return []
    known = {(d.info.get("extractor"), d.info.get("id")) for d in list_downloads()}
    added = []
    for entry in sorted(root.iterdir()):
        if entry.is_file() and _is_media(entry):
            item = _register(entry, root, known, move=True, log=log)
        elif entry.is_dir() and not (entry / manifest.DOWNLOAD).exists():
            files = list(entry.iterdir())
            if any(_PARTIAL.search(p.name) for p in files):
                continue  # yt-dlp 還在下載或合併
            media = sorted(p for p in files if p.is_file() and _is_media(p))
            item = _register(media[0], root, known, move=False, log=log) if media else None
        else:
            continue
        if item:
            added.append(item)
            known.add((item.info["extractor"], item.info["id"]))
    return added


def _is_media(path: Path) -> bool:
    return path.suffix.lower() in MEDIA_EXTS and not _PARTIAL.search(path.name)


def _ready(path: Path) -> bool:
    """檔案寫完了嗎：大小持續不變一段時間，而且沒有被其他程式佔用。"""
    now = time.time()
    st = path.stat()
    key = str(path)
    prev = _seen.get(key)
    if prev is None or prev[0] != st.st_size:
        _seen[key] = (st.st_size, now)
        # 第一次看到、但檔案早就放好了（例如 UI 沒開的時候放進來的）也算穩定。
        if now - st.st_mtime < 30:
            return False
    elif now - prev[1] < _STABLE_SECONDS:
        return False
    try:
        with open(path, "r+b"):
            pass
    except OSError:
        return False
    return True


def _register(path: Path, root: Path, known: set, *, move: bool,
              log: Callable[[str], None] | None) -> Download | None:
    if not _ready(path):
        return None
    size = path.stat().st_size
    ident = "local-" + hashlib.sha1(f"{path.name}:{size}".encode()).hexdigest()[:8]
    if ("local", ident) in known:
        if str(path) not in _warned and log:
            log(f"略過重複的檔案（曲庫裡已經有同一個檔案）：{path}")
        _warned.add(str(path))
        return None

    if move:
        folder = root / f"{path.stem} ({ident})"
        folder.mkdir(exist_ok=True)
        target = folder / path.name
        try:
            os.replace(path, target)
        except OSError:
            return None  # 被佔用：下次掃描再試
    else:
        folder, target = path.parent, path

    info = {
        "extractor": "local",
        "id": ident,
        "url": None,
        "title": target.stem,
        "uploader": None,
        "channel": None,
        "track": None,
        "artists": None,
        "duration": duration(target),
        "mode": "video" if has_video_stream(target) else "audio",
        "file": target.name,
        "source": "local",
        "downloaded_at": manifest.now(),
        "meta_version": METADATA_VERSION,
    }
    manifest.write(folder / manifest.DOWNLOAD, info)
    _seen.pop(str(path), None)
    if log:
        log(f"匯入手動放入的檔案：{target}")
    return Download(folder, info)
