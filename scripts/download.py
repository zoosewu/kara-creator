#!/usr/bin/env python3
"""下載影片 / 音訊到 output/downloads/<標題 [id]>/。已下載過的項目會自動略過。

用法:
    python scripts/download.py URL [URL ...]
    python scripts/download.py --batch urls.txt     # 每行一個網址，# 開頭為註解
    python scripts/download.py URL --audio-only     # 只下載音訊（m4a，不轉檔）
    python scripts/download.py PLAYLIST_URL --playlist
    python scripts/download.py --list               # 列出已下載項目與處理狀態
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from songtool.local import import_local  # noqa: E402
from songtool import config, library  # noqa: E402
from songtool.download import download  # noqa: E402


def read_batch(path: str) -> list[str]:
    lines = Path(path).read_text(encoding="utf-8-sig").splitlines()
    return [s for s in (line.strip() for line in lines) if s and not s.startswith("#")]


def main() -> int:
    parser = argparse.ArgumentParser(description="下載影片或音訊到 output/downloads/。")
    parser.add_argument("urls", nargs="*", help="要下載的網址")
    parser.add_argument("--batch", metavar="FILE", help="從文字檔讀取網址，每行一個")
    parser.add_argument("--audio-only", action="store_true", help="只下載音訊")
    parser.add_argument("--playlist", action="store_true",
                        help="網址是播放清單時下載整個清單（預設只下載單支影片）")
    parser.add_argument("--force", action="store_true", help="已下載過也重新下載")
    parser.add_argument("--list", action="store_true", help="列出已下載的項目後結束")
    args = parser.parse_args()
    import_local(log=print)  # 先登記手動放進 output/downloads/ 的影音檔

    if args.list:
        print(f"下載資料夾: {config.DOWNLOADS_DIR}")
        print("\n".join(library.format_lines(library.scan())))
        return 0

    urls = list(args.urls)
    if args.batch:
        urls += read_batch(args.batch)
    if not urls:
        parser.error("請提供網址，或使用 --batch / --list")

    counts = {"downloaded": 0, "skipped": 0, "failed": 0}
    failures = []
    for result in download(urls, audio_only=args.audio_only, playlist=args.playlist,
                           force=args.force):
        counts[result.status] += 1
        if result.status == "downloaded":
            print(f"[v] 下載完成: {result.item.file}", flush=True)
        elif result.status == "skipped":
            print(f"[=] 已下載過，略過: {result.title}", flush=True)
        else:
            failures.append(result)
            print(f"[x] 失敗: {result.title or result.url}\n    {result.error}", flush=True)

    print("-" * 50)
    print(f"下載 {counts['downloaded']}，略過 {counts['skipped']}，失敗 {counts['failed']}")
    for result in failures:
        print(f"  [x] {result.title or result.url}: {result.error}")
    return 1 if failures else 0


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass
    sys.exit(main())
