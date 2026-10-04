"""v2 的指紋（docs/v2/data.md「指紋」）。Go 版在 nas/internal/fingerprint，兩邊必須算出相同的值（黃金測試）。

搬遷工具（v1 → v2）用它替搬過去的紀錄寫上新的指紋，搬家後才不會整首重新對時。
"""
from __future__ import annotations

import hashlib
import math
from typing import Iterable, Sequence


def _sha256(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def ms(t: float) -> int:
    """秒 → 整數毫秒（四捨五入，.5 進位）。"""
    return math.floor(t * 1000 + 0.5)


def _value(v: object) -> str:
    # 值裡不能有換行（每個欄位一行）；歌名、備註等使用者輸入的文字先跳脫。
    return str(v).replace("\\", "\\\\").replace("\n", "\\n")


def h(*fields: tuple[str, object]) -> str:
    return _sha256("".join(f"{name}={_value(value)}\n" for name, value in fields))


def lyrics(texts: Sequence[str], rubies: Sequence[Iterable]) -> str:
    """歌詞指紋：要唱的句子文字 + 手動讀音（演唱者、翻譯、註解、標題不算）。
    rubies 每項是 (start, end, reading) 或有 start / end / reading 屬性或 key 的物件。"""
    parts = []
    for i, text in enumerate(texts):
        rs = sorted((_ruby(r) for r in (rubies[i] if i < len(rubies) else ())), key=lambda r: r[0])
        parts.append(text + "".join(f"\x1f{s}\x1f{e}\x1f{rd}" for s, e, rd in rs))
    return _sha256("\x1e".join(parts))


def _ruby(r) -> tuple[int, int, str]:
    if isinstance(r, dict):
        return int(r["start"]), int(r["end"]), str(r["reading"])
    if isinstance(r, (tuple, list)):
        return int(r[0]), int(r[1]), str(r[2])
    return int(r.start), int(r.end), str(r.reading)


def alignment(lines: Sequence[dict]) -> str:
    """對時內容指紋：每句與每個字的時間（毫秒）與字。"""
    parts = []
    for line in lines:
        head = f"{ms(line['start'])}\x1f{ms(line['end'])}"
        parts.append(head + "".join(f"\x1d{w['text']}\x1f{ms(w['start'])}\x1f{ms(w['end'])}" for w in line["words"]))
    return _sha256("\x1e".join(parts))


def items(values: Iterable[str | None]) -> str:
    """清單：各項以 \\x1f 串接，None 當成空字串。"""
    return "\x1f".join(v or "" for v in values)


def separate(*, source: str, model: str, stems: int, version: int) -> str:
    return h(("stage", "separate"), ("source", source), ("model", model), ("stems", stems), ("version", version))


def align(*, lyrics: str, vocals: str, model: str, language: str, version: int) -> str:
    return h(("stage", "align"), ("lyrics", lyrics), ("vocals", vocals), ("model", model),
             ("language", language), ("version", version))


def render(*, target: str, media: str, alignment: str, lyrics: str, singers: str, translations: str,
           title: str, artist: str, note: str, scale: float, font: str, size: str, version: int,
           reading: int, ass: str | None = None) -> str:
    """ass：使用者手動改過的 ASS 的 sha256。有的話畫面完全由那份 ASS 決定，取代對時、歌詞、標題、樣式、字型等欄位。
    reading：versions.reading（日文的假名由 worker 燒錄時自己算，讀音規則改了成品也會變）。"""
    if ass:
        return h(("stage", "render"), ("target", target), ("media", media), ("ass", ass), ("size", size),
                 ("version", version))
    return h(("stage", "render"), ("target", target), ("media", media), ("alignment", alignment), ("lyrics", lyrics),
             ("singers", singers), ("translations", translations), ("title", title), ("artist", artist),
             ("note", note), ("scale", f"{scale:.2f}"), ("font", font), ("size", size), ("version", version),
             ("reading", reading))


def qa(*, alignment: str, lyrics: str, vocals: str, language: str, model: str, version: int) -> str:
    return h(("stage", "qa"), ("alignment", alignment), ("lyrics", lyrics), ("vocals", vocals),
             ("language", language), ("model", model), ("version", version))


def approve(*, lyrics: str, language: str, method: int, run: str) -> str:
    """「已確認」：歌詞指紋、語言、對時方法、整首對時的編號（只需重燒的更新不影響確認）。"""
    return h(("stage", "approve"), ("lyrics", lyrics), ("language", language), ("method", method), ("run", run))
