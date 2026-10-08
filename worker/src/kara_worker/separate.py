"""去人聲：用 Demucs 把 44.1kHz 立體聲 wav 分成各分軌的 wav。

Demucs 在子程序裡跑（kara_worker.demucs_run）：取消時直接結束子程序，顯示卡記憶體一定會釋放。
"""
from __future__ import annotations

import os
import re
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path

from .media import Cancelled

_PERCENT = re.compile(r"(\d+)%\|")
_SRC = Path(__file__).resolve().parent.parent   # 沒有安裝套件、直接從原始碼執行時，子程序靠它找到 kara_worker


def resolve_device(requested: str) -> str:
    if requested != "auto":
        return requested
    try:
        import torch
        return "cuda" if torch.cuda.is_available() else "cpu"
    except Exception:
        return "cpu"


def run_demucs(wav: Path, workdir: Path, *, model: str, stems: int, device: str,
               log: Callable[[str], None], progress: Callable[[float], None],
               should_stop: Callable[[], bool]) -> list[Path]:
    """分離 wav，回傳各分軌的 wav（stems=2 時是 vocals.wav、no_vocals.wav）。"""
    device = resolve_device(device)
    cmd = [sys.executable, "-m", "kara_worker.demucs_run", "-n", model, "-o", str(workdir), "--device", device]
    if stems == 2:
        cmd += ["--two-stems", "vocals"]
    cmd.append(str(wav))
    env = dict(os.environ, PYTHONPATH=os.pathsep.join(filter(None, [str(_SRC), os.environ.get("PYTHONPATH")])))

    log(f"  . Demucs 分離中（model={model}, device={device}, stems={stems}）...")
    # Demucs 的輸出轉給紀錄；進度條只每 10% 記一次，避免洗版。文字模式會把 tqdm 覆寫同一行用的 \r 也當成換行。
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, env=env,
                            text=True, encoding="utf-8", errors="replace")
    reported = -10
    try:
        for raw in proc.stdout:
            if should_stop():
                raise Cancelled()
            line = raw.strip()
            if not line:
                continue
            m = _PERCENT.search(line)
            if not m:
                log(f"    {line}")
                continue
            pct = int(m.group(1))
            progress(pct / 100)
            if pct >= reported + 10:
                log(f"  . 分離進度 {pct}%")
                reported = pct
    finally:
        if proc.poll() is None:   # 取消或出錯時不要留下還在跑的 Demucs（它會一直佔著顯示卡）
            proc.kill()
            proc.wait()
    if proc.wait() != 0:
        raise RuntimeError(f"Demucs 執行失敗，returncode={proc.returncode}")
    stems_out = sorted((workdir / model).glob("*.wav"))
    if not stems_out:
        raise RuntimeError("Demucs 沒有產生任何輸出")
    return stems_out
