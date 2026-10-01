"""每個項目資料夾內的 JSON 紀錄檔，用來判斷是否已下載 / 已處理。

紀錄檔一律在工作完成後才寫入，所以中途失敗的資料夾沒有紀錄檔，下次會重做。
"""
from __future__ import annotations

import json
import os
from datetime import datetime
from pathlib import Path

DOWNLOAD = "download.json"
SEPARATE = "separate.json"


def read(path: Path) -> dict | None:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    return data if isinstance(data, dict) else None


def write(path: Path, data: dict) -> None:
    """先寫暫存檔再改名，避免中斷時留下半個 JSON。"""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=2), encoding="utf-8")
    os.replace(tmp, path)


def now() -> str:
    return datetime.now().astimezone().isoformat(timespec="seconds")
