"""在終端機直接執行時，沒給的 NAS 網址、名稱、token 用問的；上次用的值記在 worker.json（不進 git），留白就沿用。

container 裡（沒有終端機）不問也不記：照舊只看參數與環境變數。
"""
from __future__ import annotations

import getpass
import json
import os
import platform
from collections.abc import Callable
from pathlib import Path

FIELDS = ("nas", "name", "token")
CLEAR = "-"   # token 欄位輸入這個 = 不用 token（清掉記住的）


def settings_path() -> Path:
    """worker.ps1 指定在 worker 資料夾裡（.gitignore 已排除）；其他情況放在使用者的設定資料夾。"""
    if p := os.environ.get("KARA_WORKER_SETTINGS"):
        return Path(p)
    if os.name == "nt":
        return Path(os.environ.get("LOCALAPPDATA", Path.home())) / "kara-ai" / "worker.json"
    return Path(os.environ.get("XDG_CONFIG_HOME", Path.home() / ".config")) / "kara-ai" / "worker.json"


def load(path: Path) -> dict[str, str]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return {}
    if not isinstance(data, dict):
        return {}
    return {k: v for k in FIELDS if isinstance(v := data.get(k), str)}


def save(path: Path, values: dict[str, str]) -> None:
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps({k: values[k] for k in FIELDS}, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        if os.name != "nt":
            path.chmod(0o600)   # 有 token
    except OSError as exc:
        print(f"[!] 無法記住這次的設定（{path}）：{exc}", flush=True)


def _mask(token: str) -> str:
    return "*" * 4 + token[-2:] if len(token) > 4 else "*" * len(token)


def ask(given: dict[str, str | None], saved: dict[str, str],
        read: Callable[[str], str] = input, read_secret: Callable[[str], str] = getpass.getpass) -> dict[str, str]:
    """given 裡是 None 的欄位才問；括號裡是留白時用的值。"""
    out = {k: v for k, v in given.items() if v is not None}
    if "nas" not in out:
        last = saved.get("nas", "")
        while not (value := read(f"NAS 的網址{f'（{last}）' if last else '（例如 http://mac-mini.local:8765）'}：").strip() or last):
            print("請輸入 NAS 的網址")
        out["nas"] = value
    if "name" not in out:
        last = saved.get("name") or platform.node()
        out["name"] = read(f"這台的名稱（{last}）：").strip() or last
    if "token" not in out:
        last = saved.get("token", "")
        hint = f"（{_mask(last)}，輸入 {CLEAR} 改成不用）" if last else "（留白 = 不用）"
        value = read_secret(f"共用 token{hint}：").strip()
        out["token"] = "" if value == CLEAR else value or last
    return out
