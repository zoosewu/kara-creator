"""versions.json 是 NAS 與 worker 共用的版本：worker 讀到的要和 repo 根目錄的那份一樣。"""
import json
from pathlib import Path

from kara_worker import versions

ROOT = Path(__file__).resolve().parents[2]


def test_versions_json():
    expected = json.loads((ROOT / "versions.json").read_text(encoding="utf-8"))
    assert set(expected) == {"protocol", "separate", "align", "qa", "reading", "render"}
    assert versions.current() == expected
