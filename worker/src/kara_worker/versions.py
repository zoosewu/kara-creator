"""演算法與協定的版本（repo 根目錄的 versions.json，和 NAS 共用；不同時 NAS 會拒絕這台 worker）。

安裝成套件時 versions.json 會打包進來（pyproject.toml 的 force-include）；
直接從原始碼執行時（開發、Windows 的 editable 安裝）往上層資料夾找。
"""
from __future__ import annotations

import json
from functools import cache
from pathlib import Path

_HERE = Path(__file__).resolve().parent


@cache
def current() -> dict[str, int]:
    for folder in (_HERE, *_HERE.parents):
        path = folder / "versions.json"
        if path.is_file():
            return json.loads(path.read_text(encoding="utf-8"))
    raise FileNotFoundError("找不到 versions.json")
