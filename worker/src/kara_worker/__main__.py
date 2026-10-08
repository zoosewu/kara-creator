"""命令列：kara-worker（或 python -m kara_worker）。

    kara-worker --nas http://mac-mini.local:8765 [--name pc-4070] [--token …] [--cache 資料夾]
    kara-worker --nas … --channels interactive     # 只算假名（不需要顯示卡）
"""
from __future__ import annotations

import argparse
import os
import platform
import signal
import sys
import threading
import time
from pathlib import Path

from .client import Worker, default_cache


def main() -> None:
    parser = argparse.ArgumentParser(description="伴唱帶工作室的 AI worker：向 NAS 領任務。")
    parser.add_argument("--nas", default=os.environ.get("KARA_NAS"), help="NAS 的網址（KARA_NAS）")
    parser.add_argument("--name", default=os.environ.get("KARA_WORKER_NAME") or platform.node(),
                        help="這台的名稱（KARA_WORKER_NAME，預設電腦名稱）")
    parser.add_argument("--token", default=os.environ.get("KARA_WORKER_TOKEN"), help="共用 token（KARA_WORKER_TOKEN）")
    parser.add_argument("--cache", default=os.environ.get("KARA_CACHE"), help="快取資料夾（KARA_CACHE）")
    parser.add_argument("--channels", default=os.environ.get("KARA_CHANNELS", "heavy,interactive"),
                        help="開哪些通道：heavy、interactive")
    parser.add_argument("--device", default=os.environ.get("KARA_DEVICE", "auto"), help="auto / cuda / cpu")
    args = parser.parse_args()
    if not args.nas:
        parser.error("請用 --nas（或環境變數 KARA_NAS）指定 NAS 的網址")
    channels = [c.strip() for c in args.channels.split(",") if c.strip()]
    if not channels or set(channels) - {"heavy", "interactive"}:
        parser.error("--channels 只能是 heavy、interactive")
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass

    w = Worker(args.nas, args.name, args.token, Path(args.cache) if args.cache else default_cache(), channels, args.device)
    w.cleanup()

    def stop(*_):
        if w.stopping.is_set():
            os._exit(1)   # 第二次 Ctrl+C：不等了
        print("關閉中…（手上的任務交還 NAS）", flush=True)
        w.stopping.set()

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    w.hello()
    threads = [threading.Thread(target=w.loop, args=(ch,), daemon=True, name=ch) for ch in channels]
    for t in threads:
        t.start()
    while any(t.is_alive() for t in threads) and not w.stopping.is_set():
        time.sleep(0.5)
    w.bye()


if __name__ == "__main__":
    main()
