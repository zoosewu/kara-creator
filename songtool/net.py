"""網路相關的小工具（歌曲伺服器與 AI 伺服器共用）。"""
from __future__ import annotations

import socket


def lan_addresses() -> list[str]:
    """這台電腦在區域網路上的 IPv4 位址（給其他裝置連線用）。"""
    found = []
    try:
        # 不會真的送出封包，只是讓系統挑出對外的網路介面。
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
            sock.connect(("10.255.255.255", 1))
            found.append(sock.getsockname()[0])
    except OSError:
        pass
    try:
        for info in socket.getaddrinfo(socket.gethostname(), None, socket.AF_INET):
            ip = info[4][0]
            if not ip.startswith("127.") and ip not in found:
                found.append(ip)
    except OSError:
        pass
    return found
