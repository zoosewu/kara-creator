"""任務參數裡的歌詞：每句的文字與使用者標的讀音（歌詞檔的解析在 NAS 做）。"""
from __future__ import annotations

from dataclasses import dataclass


@dataclass
class Ruby:
    """一段讀音：句子文字的 [start, end)（code point 位置）讀作 reading。"""
    start: int
    end: int
    reading: str


@dataclass
class Lyrics:
    """要唱的句子（對時的輸入）。"""
    lines: list[str]
    rubies: list[list[Ruby]]

    @classmethod
    def from_params(cls, params: dict) -> Lyrics:
        return cls(list(params.get("texts", [])), [[Ruby(**r) for r in line] for line in params.get("rubies", [])])
