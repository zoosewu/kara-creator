#!/usr/bin/env python3
"""製作 KTV 字幕影片，輸出到 output/karaoke/<名稱>/。已做過且沒有變動的會自動略過。

歌詞放在 lyrics/<影片id>.txt。還沒分離的項目會自動先做人聲 / 伴奏分離。

用法:
    python scripts/karaoke.py                   # 處理所有有歌詞的下載項目
    python scripts/karaoke.py v-WcMQbXbKY       # 用影片 id 或標題關鍵字指定
    python scripts/karaoke.py --target both     # 伴奏版與原曲版都做
    python scripts/karaoke.py --list            # 列出狀態與歌詞應放的位置
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from songtool.local import import_local  # noqa: E402
from songtool import config, karaoke, lyrics  # noqa: E402
from songtool.download import Download, list_downloads  # noqa: E402

STATUS_TEXT = {
    "no_lyrics": "缺歌詞",
    "pending": "未製作",
    "done": "已完成",
    "outdated": "需更新",
}


def select(items: list[Download], queries: list[str]) -> list[Download]:
    if not queries:
        return items
    picked = []
    for q in queries:
        q_low = q.lower()
        matches = [it for it in items
                   if it.info.get("id") == q or q_low in it.title.lower() or q_low in it.name.lower()]
        if not matches:
            print(f"[!] 找不到符合「{q}」的下載項目")
        picked += [m for m in matches if m not in picked]
    return picked


def print_list(items: list[Download]) -> None:
    print(f"歌詞資料夾: {config.LYRICS_DIR}")
    print(f"輸出資料夾: {config.KARAOKE_DIR}")
    if not items:
        print("（下載資料夾目前是空的）")
        return
    for it in items:
        state = karaoke.status(it)
        print(f"  [{STATUS_TEXT[state]}] {it.title}")
        if state == "no_lyrics":
            print(f"      歌詞請放在: {lyrics.candidates(it)[0]}")
        else:
            print(f"      歌詞: {lyrics.find(it)}")


def main() -> int:
    parser = argparse.ArgumentParser(description="製作 KTV 逐字變色字幕影片。")
    parser.add_argument("queries", nargs="*", help="影片 id 或標題關鍵字；省略時處理全部")
    parser.add_argument("--target", choices=["instrumental", "original", "both"],
                        default="instrumental",
                        help="字幕燒在伴奏版（預設）、原曲版或兩者")
    parser.add_argument("--model", default="large-v3", help="對時用的 Whisper 模型")
    parser.add_argument("--language", default=None, help="歌詞語言，例如 ja、zh；預設由歌詞判斷")
    parser.add_argument("--font", default=None, help="字型名稱；預設依語言挑選")
    parser.add_argument("--device", default="auto", choices=["auto", "cuda", "cpu"])
    parser.add_argument("--realign", action="store_true", help="強制重新對時")
    parser.add_argument("--force", action="store_true",
                        help="全部重做（會覆蓋手動修改過的 .ass）")
    parser.add_argument("--list", action="store_true", help="列出狀態後結束")
    args = parser.parse_args()
    import_local(log=print)  # 先登記手動放進 output/downloads/ 的影音檔

    items = select(list_downloads(), args.queries)
    if args.list:
        print_list(items)
        return 0
    if not items:
        print("沒有可處理的項目。先用 download 下載。")
        return 1

    targets = ("instrumental", "original") if args.target == "both" else (args.target,)
    opts = karaoke.KaraokeOptions(whisper_model=args.model, language=args.language,
                                  targets=targets, font=args.font, device=args.device,
                                  realign=args.realign, force=args.force)
    counts = {"made": 0, "skipped": 0, "no_lyrics": 0, "failed": 0}
    failures = []
    for item in items:
        print(f"\n>> {item.title}", flush=True)
        result = karaoke.make(item, opts, log=lambda msg: print(msg, flush=True))
        counts[result.status] += 1
        if result.status == "made":
            print(f"[v] 完成: {result.out_dir}", flush=True)
        elif result.status == "skipped":
            print("[=] 已是最新，略過（要重做請加 --force）", flush=True)
        elif result.status == "no_lyrics":
            print(f"[-] 缺歌詞，略過。請放在: {lyrics.candidates(item)[0]}", flush=True)
        else:
            failures.append(result)
            print(f"[x] 失敗: {result.error}", flush=True)

    print("\n" + "-" * 50)
    print(f"完成 {counts['made']}，略過 {counts['skipped']}，"
          f"缺歌詞 {counts['no_lyrics']}，失敗 {counts['failed']}")
    for result in failures:
        print(f"  [x] {result.item.title}: {result.error}")
    return 1 if failures else 0


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass
    sys.exit(main())
