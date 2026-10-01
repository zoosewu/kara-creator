#!/usr/bin/env python3
"""立刻把歌單、影片連結、歌詞與對時備份到 data/，並 commit、push（data/ 是 git repo 時）。

平常 UI 會在佇列清空、或曲庫有變動安靜 30 秒後自動備份；這個指令是手動補一次用。

用法:
    python scripts/backup.py
    python scripts/backup.py --no-push    # 只寫檔案，不 commit / push
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from songtool import backup, config  # noqa: E402
from songtool.local import import_local  # noqa: E402


def main() -> int:
    parser = argparse.ArgumentParser(description="備份歌單、歌詞與對時到 data/。")
    parser.add_argument("--no-push", action="store_true", help="只寫檔案，不 commit / push")
    args = parser.parse_args()
    import_local(log=print)
    print(f"資料備份：{config.DATA_DIR}")
    backup.snapshot(print)
    if not args.no_push:
        backup.publish("手動備份", print)
    return 0


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass
    sys.exit(main())
