"""把完成的伴唱帶依曲庫整理輸出到 output/export/。

    output/export/zoo/周杰倫/周杰倫 - 最後的戰役.mp4

資料夾結構與曲庫相同（只用資料夾名稱），檔名是卡拉 OK 軟體慣用的「歌手 - 歌名」
（沒有歌手就只有歌名）。同一個資料夾裡歌手與歌名都相同時，依曲庫順序在後面的加上
(2)、(3)… 區分：「周杰倫 - 晴天 (2)」。
用硬連結（同一顆磁碟不佔額外空間），不同磁碟時改為複製。
只管理自己放進去的檔案（記在 export/.export.json），不會動到使用者另外放的東西。

sync() 會讓輸出資料夾與目前的曲庫一致：新增完成的歌、改名或換資料夾的歌移到新位置、
重新製作過的歌更新成新檔。
"""
from __future__ import annotations

import os
import re
import shutil
import threading
from dataclasses import dataclass, field
from pathlib import Path

from . import catalog, config, karaoke, lyrics, manifest
from .download import Download, list_downloads

RECORD = ".export.json"
_lock = threading.Lock()
_ILLEGAL = str.maketrans({c: "＿" for c in '<>:"/\\|?*'})


@dataclass
class ExportResult:
    added: list[str] = field(default_factory=list)
    removed: list[str] = field(default_factory=list)
    kept: int = 0
    skipped: list[str] = field(default_factory=list)   # 目的地已有不是我們放的同名檔


def file_name(title: str, artist: str, copy: int = 1) -> str:
    """「歌手 - 歌名.mp4」；同名的第 2 首以後加上「 (2)」。"""
    name = f"{artist.strip()} - {title.strip()}" if artist.strip() else title.strip()
    name = re.sub(r"[\x00-\x1f]", "", name.translate(_ILLEGAL)).rstrip(" .")[:150]
    return f"{name} ({copy}).mp4" if copy > 1 else f"{name}.mp4"


def targets(cat: catalog.Catalog, items: list[Download]) -> dict[str, str]:
    """每首歌在輸出資料夾中的相對路徑 {歌曲 key: 路徑}（不論伴唱帶做好了沒）。"""
    entries = []
    for item in items:
        key = catalog.song_key(item)
        song = cat.songs.get(key)
        if song is None:
            continue
        lyrics_path = lyrics.find(item)
        meta = lyrics.load(lyrics_path).meta if lyrics_path else {}
        title, artist = catalog.display_info(item, song, meta)
        folder = Path(*[f.name.translate(_ILLEGAL).rstrip(" .") or "_" for f in cat.path(song.folder)])
        entries.append((cat.sort_key(song), key, folder, title, artist))
    names: dict[str, str] = {}
    copies: dict[tuple[str, str], int] = {}
    for _, key, folder, title, artist in sorted(entries):   # 曲庫中排前面的先取得原始檔名
        slot = (folder.as_posix(), file_name(title, artist).casefold())
        copies[slot] = copies.get(slot, 0) + 1
        names[key] = (folder / file_name(title, artist, copies[slot])).as_posix()
    return names


def plan(cat: catalog.Catalog, items: list[Download]) -> dict[str, Path]:
    """{輸出的相對路徑: 來源伴唱帶}，只包含已經做好伴唱帶的歌。"""
    names = targets(cat, items)
    wanted: dict[str, Path] = {}
    for item in items:
        record = manifest.read(karaoke.output_dir(item) / karaoke.RECORD) or {}
        video = record.get("videos", {}).get("instrumental")
        src = karaoke.output_dir(item) / video["file"] if video else None
        key = catalog.song_key(item)
        if src and src.is_file() and key in names:
            wanted[names[key]] = src
    return wanted


def sync() -> ExportResult:
    with _lock:
        items = list_downloads()
        wanted = plan(catalog.include(items), items)
        root = config.EXPORT_DIR
        record = manifest.read(root / RECORD) or {}
        managed = set(record.get("files", []))
        result = ExportResult()

        for rel, src in wanted.items():
            dest = root / rel
            if dest.exists():
                if _same(dest, src):
                    result.kept += 1
                    continue
                if rel not in managed:
                    result.skipped.append(rel)
                    continue
                dest.unlink()
            dest.parent.mkdir(parents=True, exist_ok=True)
            _link_or_copy(src, dest)
            result.added.append(rel)

        for rel in sorted(managed - wanted.keys()):
            (root / rel).unlink(missing_ok=True)
            result.removed.append(rel)
            _prune(root / rel, root)

        placed = set(wanted) - set(result.skipped)
        if placed or managed:
            manifest.write(root / RECORD, {"files": sorted(placed)})
        return result


def _same(dest: Path, src: Path) -> bool:
    try:
        if os.path.samefile(dest, src):
            return True
    except OSError:
        return False
    # 複製的檔案：大小與修改時間相同就視為同一份（copy2 會保留修改時間）。
    a, b = dest.stat(), src.stat()
    return a.st_size == b.st_size and int(a.st_mtime) == int(b.st_mtime)


def _link_or_copy(src: Path, dest: Path) -> None:
    try:
        os.link(src, dest)
    except OSError:
        shutil.copy2(src, dest)


def _prune(path: Path, root: Path) -> None:
    """往上刪掉變成空的資料夾，直到 export 根目錄為止。"""
    parent = path.parent
    while parent != root and parent.is_dir() and not any(parent.iterdir()):
        parent.rmdir()
        parent = parent.parent
