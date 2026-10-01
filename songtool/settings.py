"""全域設定（output/settings.json），套用到所有歌。

    subtitle_scale   字幕大小（相對預設大小的倍數，1.0 = 預設）；歌詞、假名、翻譯、標題畫面一起縮放

改了會影響成品的設定（例如字幕大小）之後，已經做好的伴唱帶會顯示需更新，
重新製作時只重產字幕並燒錄、不會重新對時。設定會跟著資料備份（data/settings.json）。
"""
from __future__ import annotations

import threading

from . import config, manifest

SETTINGS_FILE = "settings.json"
DEFAULTS = {"subtitle_scale": 1.0}
SCALE_RANGE = (0.6, 1.6)
_lock = threading.Lock()


def path():
    return config.OUTPUT_DIR / SETTINGS_FILE


def load() -> dict:
    data = manifest.read(path()) or {}
    return {**DEFAULTS, **{k: v for k, v in data.items() if k in DEFAULTS}}


def update(**changes) -> dict:
    """修改設定（只改有給的欄位），回傳修改後的完整設定。不合理的值丟 ValueError。"""
    with _lock:
        current = load()
        if changes.get("subtitle_scale") is not None:
            scale = round(float(changes["subtitle_scale"]), 2)
            low, high = SCALE_RANGE
            if not low <= scale <= high:
                raise ValueError(f"字幕大小要在 {low:.0%}–{high:.0%} 之間")
            current["subtitle_scale"] = scale
        manifest.write(path(), current)
        return current
