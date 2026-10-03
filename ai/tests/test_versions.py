"""versions.json 是 NAS 與 AI worker 共用的版本；align、qa 必須和 v1 的 VERSION 一致（調了會整首重新對時）。"""
import ast
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def _module_version(path: Path) -> int:
    # 只讀原始碼，不 import（align.py 會載入 numpy / torch，CI 不必裝）。
    for node in ast.parse(path.read_text(encoding="utf-8")).body:
        if isinstance(node, ast.Assign) and [t.id for t in node.targets if isinstance(t, ast.Name)] == ["VERSION"]:
            return ast.literal_eval(node.value)
    raise AssertionError(f"{path} 沒有 VERSION")


def test_versions_json_matches_v1():
    versions = json.loads((ROOT / "versions.json").read_text(encoding="utf-8"))
    assert set(versions) == {"protocol", "separate", "align", "qa", "reading", "render"}
    assert versions["align"] == _module_version(ROOT / "songtool" / "align.py")
    assert versions["qa"] == _module_version(ROOT / "songtool" / "qa.py")
