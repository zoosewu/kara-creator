#!/usr/bin/env python3
"""從程式碼產生 REST API 的 OpenAPI（Swagger）規格：docs/openapi.json。

規格的來源是 ui/api_v1.py（端點）與 ui/api_schema.py（資料模型），不要手改 docs/openapi.json。
改了 API 之後執行一次，連同程式碼一起 commit，讓其他服務不必啟動伺服器也能拿到規格。

用法:
    python scripts/openapi.py           # 寫出 docs/openapi.json
    python scripts/openapi.py --check   # 只檢查 docs/openapi.json 是否和程式碼一致（不一致回傳 1）
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "ui"))

import server  # noqa: E402

TARGET = ROOT / "docs" / "openapi.json"


def main() -> int:
    parser = argparse.ArgumentParser(description="輸出 OpenAPI 規格到 docs/openapi.json")
    parser.add_argument("--check", action="store_true", help="只檢查是否和程式碼一致")
    args = parser.parse_args()
    text = json.dumps(server.app.openapi(), ensure_ascii=False, indent=2) + "\n"
    if args.check:
        current = TARGET.read_text(encoding="utf-8") if TARGET.exists() else ""
        if current != text:
            print(f"{TARGET.relative_to(ROOT)} 和程式碼不一致，請執行 python scripts/openapi.py")
            return 1
        print(f"{TARGET.relative_to(ROOT)} 是最新的")
        return 0
    TARGET.parent.mkdir(parents=True, exist_ok=True)
    TARGET.write_text(text, encoding="utf-8", newline="\n")
    spec = server.app.openapi()
    print(f"已寫出 {TARGET.relative_to(ROOT)}：{spec['info']['title']} {spec['info']['version']}，"
          f"{sum(len(v) for v in spec['paths'].values())} 個端點、{len(spec['components']['schemas'])} 個資料模型")
    return 0


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass
    sys.exit(main())
