#!/usr/bin/env python3
"""從資料備份（data/）重建曲庫：重新下載影片、放回歌詞與對時，需要時接著製作伴唱帶。

用法:
    python scripts/restore.py              # 建立資料夾與歌曲資訊、下載有連結的影片、放回歌詞與對時
    python scripts/restore.py --make       # 接著去人聲並製作伴唱帶（沿用備份的對時，不重新對時）
    python scripts/restore.py --overwrite  # 已經有的歌詞 / 對時也用備份覆蓋
    python scripts/restore.py --replace ID=URL [--replace ID=URL ...]
                                           # 原本的連結失效時，指定替代影片（ID 是備份裡的歌曲 id）

影片來源：
- 用網址下載的歌：用原本的連結重新下載，時間直接沿用備份
- 手動放入、但在「資訊」補了連結的歌：用補上的連結下載
- 手動放入、沒有連結的歌：列出原始檔名與大小，把同一個檔案放回 output/downloads/ 後再執行一次
  （id 由檔名與大小決定，放回同一個檔案就對得上）
- 換了來源（補上的連結、--replace 的替代影片）時，影片的前奏長度可能不同，所以不沿用備份的時間，
  製作時會重新對時；歌詞、資料夾、歌名、演唱者、語言都會接到新影片上。
連結失效（下載失敗）的歌會列出歌名、演唱者、原始影片標題與長度，方便找替代影片。
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from songtool import align, backup, catalog, config, karaoke, lyrics, manifest  # noqa: E402
from songtool.catalog import Folder  # noqa: E402
from songtool.download import Download, download, list_downloads  # noqa: E402
from songtool.local import import_local  # noqa: E402
from songtool.separate import separate_file  # noqa: E402


def apply_info(song: catalog.Song, entry: dict, folders: dict) -> None:
    """把備份裡的歌曲資訊（資料夾、順序、歌名、演唱者、語言、連結、確認）套到曲庫的歌上。"""
    song.folder = entry["folder"] if entry["folder"] in folders else None
    song.number = int(entry.get("order") or song.number)
    song.title, song.artist = entry.get("title", ""), entry.get("artist", "")
    song.language, song.link = entry.get("language", ""), entry.get("link", "")
    # 確認紀錄是字幕的指紋：還原重做出來的字幕一樣，確認就仍然有效
    song.approved, song.approved_at = entry.get("approved", ""), entry.get("approved_at", "")


def describe(entry: dict) -> str:
    """給人找替代影片用：歌名、演唱者、原始影片標題、長度。"""
    src = entry["source"]
    name = " - ".join(x for x in (entry["display"].get("artist"), entry["display"].get("title")) if x)
    length = f"{int(src['duration']) // 60}:{int(src['duration']) % 60:02d}" if src.get("duration") else "長度不明"
    return f"{name}｜原始標題：{src.get('title') or '不明'}｜{length}"


def main() -> int:
    parser = argparse.ArgumentParser(description="從 data/ 的備份重建曲庫。")
    parser.add_argument("--make", action="store_true", help="接著去人聲並製作伴唱帶")
    parser.add_argument("--overwrite", action="store_true", help="已經有的歌詞與對時也用備份覆蓋")
    parser.add_argument("--replace", action="append", default=[], metavar="ID=URL",
                        help="原本的連結失效時，指定替代影片的網址（可重複）")
    args = parser.parse_args()
    replace = {}
    for pair in args.replace:
        song_id, sep, url = pair.partition("=")
        if not sep or not url.startswith(("http://", "https://")):
            parser.error(f"--replace 的格式是 ID=URL：{pair}")
        replace[song_id.strip()] = url.strip()

    data_file = config.DATA_DIR / backup.SONGS_FILE
    if not data_file.is_file():
        print(f"找不到備份：{data_file}（先把資料 repo clone 到 data/）")
        return 1
    data = json.loads(data_file.read_text(encoding="utf-8"))
    unknown = set(replace) - {s["id"] for s in data["songs"]}
    if unknown:
        parser.error(f"備份裡沒有這些歌曲 id：{', '.join(sorted(unknown))}")
    import_local(log=print)

    # 1. 曲庫的資料夾（沿用備份裡的 id，結構與順序都一樣）
    with catalog.edit() as cat:
        for f in data["folders"]:
            if f["id"] not in cat.folders:
                cat.folders[f["id"]] = Folder(f["id"], f["name"], int(f.get("order") or 1), f.get("parent"))
                cat.dirty = True
    print(f"曲庫：{len(data['folders'])} 個資料夾、{len(data['songs'])} 首歌")

    # 2. 找出每首歌對應的影片：已經有的直接用，沒有的用連結下載（--replace > 原本的連結 > 補上的連結）
    have = {catalog.song_key(item): item for item in list_downloads()}
    target: dict[str, Download] = {}     # 備份的歌曲 key -> 影片
    substituted: set[str] = set()        # 換了來源的歌（不沿用時間）
    missing_local, failed = [], []
    for entry in data["songs"]:
        key = entry["key"]
        if key in have and entry["id"] not in replace:
            target[key] = have[key]
            continue
        url = replace.get(entry["id"]) or entry["source"].get("url") or entry.get("link")
        if not url:
            missing_local.append(entry)
            continue
        item = None
        for result in download([url], audio_only=entry["source"].get("mode") == "audio"):
            if result.status in ("downloaded", "skipped"):
                item = result.item
                print(f"[v] {'下載完成' if result.status == 'downloaded' else '已下載過'}：{result.title or url}", flush=True)
            else:
                print(f"[x] 下載失敗：{url}（{result.error}）", flush=True)
        if item is None:
            failed.append((entry, url))
            continue
        target[key] = item
        if catalog.song_key(item) != key:
            substituted.add(key)

    # 3. 歌曲資訊套到對應的影片上（換了來源的歌，曲庫裡的資料接到新影片、舊的那筆拿掉）
    with catalog.edit() as cat:
        for entry in data["songs"]:
            item = target.get(entry["key"])
            new_key = catalog.song_key(item) if item else entry["key"]
            song = cat.ensure_song(new_key)
            apply_info(song, entry, cat.folders)
            if item and new_key != entry["key"]:
                song.link = ""                    # 新影片本身就有下載連結
                song.approved = song.approved_at = ""
                cat.songs.pop(entry["key"], None)
            cat.dirty = True

    # 4. 放回歌詞與對時（換了來源的歌不沿用時間）
    restored_lyrics = restored_timing = 0
    for entry in data["songs"]:
        item = target.get(entry["key"])
        if item is None:
            continue
        if entry.get("lyrics"):
            path = lyrics.find(item) or lyrics.candidates(item)[0]
            if args.overwrite or not path.exists():
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text((config.DATA_DIR / entry["lyrics"]).read_text(encoding="utf-8"), encoding="utf-8")
                restored_lyrics += 1
        if entry.get("timing") and entry["key"] in substituted:
            print(f"  . {entry['display']['title']}：影片來源換過，製作時會重新對時（不沿用備份的時間）")
        elif entry.get("timing"):
            path = karaoke.output_dir(item) / karaoke.ALIGNMENT
            if args.overwrite or not path.exists():
                timing = json.loads((config.DATA_DIR / entry["timing"]).read_text(encoding="utf-8"))
                manifest.write(path, {
                    "restored": {k: timing.get(k) for k in ("lyrics_sha1", "language", "model", "method")},
                    "lines": timing["lines"], "adjustments": timing.get("adjustments", [])})
                restored_timing += 1
                if timing.get("method") != align.VERSION:
                    print(f"  [!] {entry['display']['title']}：備份的對時方法較舊，製作時會重新對時")
    print(f"放回歌詞 {restored_lyrics} 首、對時 {restored_timing} 首")

    # 5. 需要的話接著製作伴唱帶
    if args.make:
        for entry in data["songs"]:
            item = target.get(entry["key"])
            if item is None or not entry.get("lyrics"):
                continue
            print(f"== {entry['display']['artist']} - {entry['display']['title']}", flush=True)
            result = separate_file(item.file, log=print)
            if result.status == "failed":
                print(f"  [x] 去人聲失敗：{result.error}")
                continue
            made = karaoke.make(item, log=print)
            print(f"  [{'v' if made.status in ('made', 'skipped') else 'x'}] {made.status}"
                  + (f"：{made.error}" if made.error else ""), flush=True)

    if failed:
        print("-" * 50)
        print(f"以下 {len(failed)} 首下載失敗（連結可能失效）。找到替代影片後用 --replace 歌曲id=網址 再執行一次：")
        for entry, url in failed:
            print(f"  [{entry['id']}] {describe(entry)}")
            print(f"      原本的連結：{url}")
    if missing_local:
        print("-" * 50)
        print(f"以下 {len(missing_local)} 首是手動放入的影片（沒有連結）。把原檔放回 output/downloads/ 後再執行一次，"
              f"或用 --replace 歌曲id=網址 改用網路上的影片：")
        for entry in missing_local:
            src = entry["source"]
            size = f"{src['size'] / 1024 / 1024:.1f} MB" if src.get("size") else "大小不明"
            print(f"  [{entry['id']}] {describe(entry)}")
            print(f"      原始檔案：{src.get('file')}（{size}）")
    return 0


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass
    sys.exit(main())
