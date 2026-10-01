#!/usr/bin/env python3
"""從資料備份（data/）重建曲庫：重新下載影片、放回歌詞與對時，需要時接著製作伴唱帶。

用法:
    python scripts/restore.py              # 建立資料夾與歌曲資訊、下載有連結的影片、放回歌詞與對時
    python scripts/restore.py --make       # 接著去人聲並製作伴唱帶（沿用備份的對時，不重新對時）
    python scripts/restore.py --overwrite  # 已經有的歌詞 / 對時也用備份覆蓋

手動放入的影片沒有連結，會列出原始檔名與大小；把同一個檔案放回 output/downloads/ 後再執行一次即可
（id 由檔名與大小決定，放回同一個檔案就對得上）。
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from songtool import align, backup, catalog, config, karaoke, lyrics, manifest  # noqa: E402
from songtool.catalog import Folder  # noqa: E402
from songtool.download import download, list_downloads  # noqa: E402
from songtool.local import import_local  # noqa: E402
from songtool.separate import separate_file  # noqa: E402


def main() -> int:
    parser = argparse.ArgumentParser(description="從 data/ 的備份重建曲庫。")
    parser.add_argument("--make", action="store_true", help="接著去人聲並製作伴唱帶")
    parser.add_argument("--overwrite", action="store_true", help="已經有的歌詞與對時也用備份覆蓋")
    args = parser.parse_args()

    data_file = config.DATA_DIR / backup.SONGS_FILE
    if not data_file.is_file():
        print(f"找不到備份：{data_file}（先把資料 repo clone 到 data/）")
        return 1
    data = json.loads(data_file.read_text(encoding="utf-8"))
    import_local(log=print)

    # 1. 曲庫的資料夾與歌曲資訊（沿用備份裡的 id，資料夾結構與順序都一樣）
    with catalog.edit() as cat:
        for f in data["folders"]:
            if f["id"] not in cat.folders:
                cat.folders[f["id"]] = Folder(f["id"], f["name"], int(f.get("order") or 1), f.get("parent"))
                cat.dirty = True
        for s in data["songs"]:
            folder = s["folder"] if s["folder"] in cat.folders else None
            song = cat.ensure_song(s["key"], folder=folder)
            song.folder, song.number = folder, int(s.get("order") or song.number)
            song.title, song.artist, song.language = s.get("title", ""), s.get("artist", ""), s.get("language", "")
            cat.dirty = True
    print(f"曲庫：{len(data['folders'])} 個資料夾、{len(data['songs'])} 首歌")

    # 2. 下載有連結、但還沒有影片的歌
    have = {catalog.song_key(item) for item in list_downloads()}
    missing_local = []
    for s in data["songs"]:
        if s["key"] in have:
            continue
        url = s["source"].get("url")
        if not url:
            missing_local.append(s)
            continue
        audio_only = s["source"].get("mode") == "audio"
        for result in download([url], audio_only=audio_only):
            mark = {"downloaded": "[v] 下載完成", "skipped": "[=] 已下載過"}.get(result.status, "[x] 下載失敗")
            print(f"{mark}：{result.title or url}" + (f"（{result.error}）" if result.error else ""), flush=True)

    # 3. 放回歌詞與對時
    items = {catalog.song_key(item): item for item in list_downloads()}
    restored_lyrics = restored_timing = 0
    for s in data["songs"]:
        item = items.get(s["key"])
        if item is None:
            continue
        if s.get("lyrics"):
            target = lyrics.find(item) or lyrics.candidates(item)[0]
            if args.overwrite or not target.exists():
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text((config.DATA_DIR / s["lyrics"]).read_text(encoding="utf-8"), encoding="utf-8")
                restored_lyrics += 1
        if s.get("timing"):
            path = karaoke.output_dir(item) / karaoke.ALIGNMENT
            if args.overwrite or not path.exists():
                timing = json.loads((config.DATA_DIR / s["timing"]).read_text(encoding="utf-8"))
                manifest.write(path, {
                    "restored": {k: timing.get(k) for k in ("lyrics_sha1", "language", "model", "method")},
                    "lines": timing["lines"], "adjustments": timing.get("adjustments", [])})
                restored_timing += 1
                if timing.get("method") != align.VERSION:
                    print(f"  [!] {s['display']['title']}：備份的對時方法較舊，製作時會重新對時")
    print(f"放回歌詞 {restored_lyrics} 首、對時 {restored_timing} 首")

    # 4. 需要的話接著製作伴唱帶
    if args.make:
        for s in data["songs"]:
            item = items.get(s["key"])
            if item is None or not s.get("lyrics"):
                continue
            print(f"== {s['display']['artist']} - {s['display']['title']}", flush=True)
            result = separate_file(item.file, log=print)
            if result.status == "failed":
                print(f"  [x] 去人聲失敗：{result.error}")
                continue
            made = karaoke.make(item, log=print)
            print(f"  [{'v' if made.status in ('made', 'skipped') else 'x'}] {made.status}"
                  + (f"：{made.error}" if made.error else ""), flush=True)

    if missing_local:
        print("-" * 50)
        print(f"以下 {len(missing_local)} 首是手動放入的影片（沒有連結），請把原檔放回 output/downloads/ 後再執行一次：")
        for s in missing_local:
            src = s["source"]
            size = f"{src['size'] / 1024 / 1024:.1f} MB" if src.get("size") else "大小不明"
            print(f"  {s['display']['artist']} - {s['display']['title']}｜{src.get('file')}（{size}）")
    return 0


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass
    sys.exit(main())
