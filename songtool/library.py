"""彙整每個下載項目目前的處理狀態，供 CLI 的 --list 與日後的 UI 使用。"""
from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from . import manifest
from .download import Download, list_downloads
from .separate import is_current, output_dir_for


@dataclass
class LibraryItem:
    download: Download
    separated_dir: Path
    separation: dict | None   # separate.json 的內容，沒有則為 None
    separated: bool           # 分離結果存在且與目前的來源檔一致

    @property
    def title(self) -> str:
        return self.download.title


def scan() -> list[LibraryItem]:
    items = []
    for item in list_downloads():
        out_dir = output_dir_for(item.file)
        record = manifest.read(out_dir / manifest.SEPARATE)
        items.append(LibraryItem(item, out_dir, record, is_current(record, item.file, out_dir)))
    return items


def format_lines(items: list[LibraryItem]) -> list[str]:
    if not items:
        return ["（下載資料夾目前是空的）"]
    lines = []
    for it in items:
        kind = "音訊" if it.download.info.get("mode") == "audio" else "影片"
        if it.separated:
            state = f"已分離 {it.separation['stems']} 軌"
        elif it.separation:
            state = "需重新分離"
        else:
            state = "未分離"
        lines.append(f"  [{kind}] [{state}] {it.title}")
    done = sum(it.separated for it in items)
    lines.append(f"共 {len(items)} 項，已分離 {done} 項，待處理 {len(items) - done} 項")
    return lines
