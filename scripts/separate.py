#!/usr/bin/env python3
"""用 Demucs 分離人聲與伴奏，輸出到 output/separated/<名稱>/。已分離過的會自動略過。

用法:
    python scripts/separate.py                      # 處理所有尚未分離的下載項目
    python scripts/separate.py D:\\music\\song.mp4    # 處理指定檔案或資料夾
    python scripts/separate.py --stems 4            # 拆成鼓/貝斯/人聲/其他
    python scripts/separate.py --list               # 列出下載項目與分離狀態
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from songtool.local import import_local  # noqa: E402
from songtool import config, library  # noqa: E402
from songtool.download import list_downloads  # noqa: E402
from songtool.media import MEDIA_EXTS  # noqa: E402
from songtool.separate import SeparateOptions, separate_file  # noqa: E402


def collect_sources(paths: list[str]) -> list[Path]:
    """沒有指定路徑時取所有下載項目；資料夾則展開其中的下載項目或媒體檔。"""
    if not paths:
        return [item.file for item in list_downloads()]
    files: list[Path] = []
    for raw in paths:
        target = Path(raw)
        if target.is_file():
            files.append(target)
        elif target.is_dir():
            items = list_downloads(target)
            files += [item.file for item in items] or sorted(
                p for p in target.iterdir() if p.is_file() and p.suffix.lower() in MEDIA_EXTS)
        else:
            print(f"[!] 找不到: {target}")
    return files


def main() -> int:
    parser = argparse.ArgumentParser(description="用 Demucs 分離人聲與伴奏。")
    parser.add_argument("inputs", nargs="*",
                        help="檔案或資料夾；省略時處理 output/downloads/ 內所有項目")
    parser.add_argument("--stems", type=int, choices=[2, 4], default=2,
                        help="2 = 人聲/伴奏（預設），4 = 鼓/貝斯/人聲/其他")
    parser.add_argument("--model", default="htdemucs",
                        help="Demucs 模型，例如 htdemucs、htdemucs_ft、mdx_extra")
    parser.add_argument("--device", default="auto", choices=["auto", "cuda", "cpu"])
    parser.add_argument("--format", default=None, help="強制輸出格式，例如 mp3；預設與輸入相同")
    parser.add_argument("--shifts", type=int, default=0, help="隨機位移次數，越大品質略好但越慢")
    parser.add_argument("--jobs", type=int, default=0, help="Demucs 平行工作數")
    parser.add_argument("--force", action="store_true", help="已分離過也重新處理")
    parser.add_argument("--list", action="store_true", help="列出下載項目與分離狀態後結束")
    args = parser.parse_args()
    import_local(log=print)  # 先登記手動放進 output/downloads/ 的影音檔

    if args.list:
        print(f"下載資料夾: {config.DOWNLOADS_DIR}")
        print(f"分離資料夾: {config.SEPARATED_DIR}")
        print("\n".join(library.format_lines(library.scan())))
        return 0

    sources = collect_sources(args.inputs)
    if not sources:
        print("沒有可處理的檔案。先用 download 下載，或直接指定檔案路徑。")
        return 1

    opts = SeparateOptions(stems=args.stems, model=args.model, device=args.device,
                           format=args.format, shifts=args.shifts, jobs=args.jobs)
    counts = {"separated": 0, "skipped": 0, "failed": 0}
    failures = []
    for src in sources:
        print(f"\n>> {src.name}", flush=True)
        result = separate_file(src, opts, force=args.force,
                               log=lambda msg: print(msg, flush=True))
        counts[result.status] += 1
        if result.status == "separated":
            print(f"[v] 完成: {result.out_dir}", flush=True)
        elif result.status == "skipped":
            print("[=] 已分離過，略過（要重做請加 --force）", flush=True)
        else:
            failures.append(result)
            print(f"[x] 失敗: {result.error}", flush=True)

    print("\n" + "-" * 50)
    print(f"分離 {counts['separated']}，略過 {counts['skipped']}，失敗 {counts['failed']}")
    for result in failures:
        print(f"  [x] {result.source.name}: {result.error}")
    return 1 if failures else 0


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass
    sys.exit(main())
