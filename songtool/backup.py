"""可重做的資料備份：把歌單、影片連結、歌詞與對時寫到 data/（另一個私人 git repo）。

    data/songs.json            曲庫資料夾、每首歌的歌名 / 演唱者 / 語言、影片連結與來源資訊
    data/lyrics/<id>.txt       歌詞（含演唱者標籤與手動讀音），和 lyrics/ 裡的檔案相同
    data/timing/<id>.json      對時結果（含手動調整與 AI 重對），還原時直接沿用、不必重新對時

<id> 是影片 id（YouTube 的 11 碼，或手動放入檔案的 local-xxxxxxxx）。
影片、伴奏、伴唱帶都不備份（有版權、也太大），還原時重新下載與製作（scripts/restore.py）。
手動放入的影片沒有連結，songs.json 記下原始檔名與大小，放回同一個檔案就會得到同一個 id。

snapshot() 寫出目前的狀態（內容沒變的檔案不會重寫），publish() 在 data/ 是 git repo 時
commit 並 push；push 失敗只記錄，不影響其他處理。
"""
from __future__ import annotations

import json
import subprocess
from pathlib import Path
from typing import Callable

from . import catalog, config, karaoke, lyrics as lyrics_mod, manifest
from .download import Download, list_downloads

SONGS_FILE = "songs.json"
LYRICS = "lyrics"
TIMING = "timing"


def song_id(item: Download) -> str:
    """備份用的歌曲 id：影片 id（手動放入的檔案是 local-xxxxxxxx）。"""
    return item.info.get("id") or item.name


def snapshot(log: Callable[[str], None] = print) -> dict:
    """把目前的歌單、歌詞與對時寫到 data/，回傳 {"songs", "lyrics", "timing", "changed"} 統計。"""
    data = config.DATA_DIR
    (data / LYRICS).mkdir(parents=True, exist_ok=True)
    (data / TIMING).mkdir(parents=True, exist_ok=True)
    items = list_downloads()
    cat = catalog.include(items)
    songs, wanted, changed = [], set(), 0

    for item in items:
        sid = song_id(item)
        song = cat.songs.get(catalog.song_key(item))
        lyrics_path = lyrics_mod.find(item)
        lyr = lyrics_mod.load(lyrics_path) if lyrics_path else None
        title, artist = catalog.display_info(item, song, lyr.meta if lyr else None)
        entry = {
            "id": sid,
            "key": catalog.song_key(item),
            "folder": song.folder if song else None,
            "order": song.number if song else 0,
            # 手動設定的欄位（空字串 = 自動）；display 是實際使用的歌名與演唱者，方便人看
            "title": song.title if song else "",
            "artist": song.artist if song else "",
            "language": song.language if song else "",
            "display": {"title": title, "artist": artist},
            "source": _source(item),
            "lyrics": None,
            "timing": None,
        }
        if lyrics_path:
            rel = f"{LYRICS}/{sid}.txt"
            changed += _write(data / rel, lyrics_path.read_text(encoding="utf-8-sig"))
            entry["lyrics"] = rel
            wanted.add(rel)
        alignment = manifest.read(karaoke.output_dir(item) / karaoke.ALIGNMENT)
        if alignment and alignment.get("lines"):
            rel = f"{TIMING}/{sid}.json"
            key = alignment.get("key", {})
            timing = {
                "lyrics_sha1": key.get("lyrics_sha1"),   # 對時當下的歌詞；還原時歌詞一樣才沿用
                "language": key.get("language"),
                "model": key.get("model"),
                "method": key.get("method"),
                "lines": alignment["lines"],
                "adjustments": alignment.get("adjustments", []),
            }
            changed += _write(data / rel, _dumps(timing))
            entry["timing"] = rel
            wanted.add(rel)
        songs.append(entry)

    folders = [{"id": f.id, "name": f.name, "parent": f.parent, "order": f.number}
               for f in sorted(cat.folders.values(), key=lambda f: f.id)]
    songs.sort(key=lambda s: s["id"])
    changed += _write(data / SONGS_FILE, _dumps({"version": 1, "folders": folders, "songs": songs}))

    # 歌被刪掉、歌詞或對時不見了：備份裡也拿掉（只動 lyrics/ 與 timing/ 裡我們管理的檔案）。
    for sub in (LYRICS, TIMING):
        for path in (data / sub).iterdir():
            if path.is_file() and f"{sub}/{path.name}" not in wanted:
                path.unlink()
                changed += 1
    stats = {"songs": len(songs), "lyrics": sum(1 for s in songs if s["lyrics"]),
             "timing": sum(1 for s in songs if s["timing"]), "changed": changed}
    log(f"  . 資料備份：{stats['songs']} 首、歌詞 {stats['lyrics']}、對時 {stats['timing']}"
        f"（{'有' if changed else '沒有'}變動）")
    return stats


def publish(message: str, log: Callable[[str], None] = print) -> bool:
    """data/ 是 git repo 時 commit 並 push。回傳是否有新的 commit。"""
    data = config.DATA_DIR
    if not (data / ".git").exists():
        log("  . data/ 不是 git repo，略過 commit（見 README「資料備份」）")
        return False
    _git(["add", "-A"], data)
    if not _git(["status", "--porcelain"], data).stdout.strip():
        return False
    result = _git(["commit", "-q", "-m", message], data, check=False)
    if result.returncode != 0:
        log(f"  [!] 資料備份 commit 失敗：{(result.stderr or result.stdout).strip()}")
        return False
    log(f"  . 資料備份已 commit：{message}")
    if _git(["remote"], data).stdout.strip():
        pushed = _git(["push", "-q", "origin", "HEAD"], data, check=False, timeout=180)
        if pushed.returncode == 0:
            log("  . 資料備份已 push")
        else:
            log(f"  [!] 資料備份 push 失敗（下次會再試）：{(pushed.stderr or pushed.stdout).strip()}")
    return True


def _source(item: Download) -> dict:
    info = item.info
    source = {
        "url": info.get("url") or info.get("webpage_url"),
        "extractor": info.get("extractor"),
        "video_id": info.get("id"),
        "title": item.title,
        "uploader": info.get("uploader"),
        "duration": info.get("duration"),
        "mode": info.get("mode", "video"),
    }
    if not source["url"]:
        # 手動放入的檔案沒有連結：記下檔名與大小，還原時放回同一個檔案就對得上。
        source["file"] = item.file.name
        source["size"] = item.file.stat().st_size if item.file.exists() else None
    return source


def _dumps(obj) -> str:
    return json.dumps(obj, ensure_ascii=False, indent=1) + "\n"


def _write(path: Path, text: str) -> int:
    """內容不同才寫入，回傳 1 / 0（有沒有變動）。"""
    if path.is_file() and path.read_text(encoding="utf-8") == text:
        return 0
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8", newline="\n")
    return 1


def _git(args: list[str], cwd: Path, *, check: bool = True, timeout: float = 60):
    return subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, encoding="utf-8",
                          errors="replace", check=check, timeout=timeout)
