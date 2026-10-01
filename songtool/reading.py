"""把一句歌詞拆成 KTV 的變色單位，並附上羅馬字讀音給 CTC 對齊使用。

- 日文：用 MeCab（fugashi + unidic-lite）依上下文判斷漢字讀音；純假名的詞再細分到
  每個音拍（ゃゅょ、ー 併入前一拍，っ 併入後一拍）。含漢字的詞整個當一個單位。
- 中文：每個漢字一個單位，讀音用拼音。
- 台語、粵語：每個漢字一個單位，讀音來自使用者在歌詞裡標的台羅 / 粵拼（沒標的字暫用國語拼音）。
  一段標了好幾個音節（例如 阮毋知影{guán m̄ tsai iánn}）且音節數等於字數時，逐字分配。
- 其他：英文等以單字為單位。
空白與標點沒有讀音（roman 為空字串），對齊時併入相鄰的單位。
所有單位的 text 串起來必定等於原句。
"""
from __future__ import annotations

import re
import unicodedata
from dataclasses import dataclass
from functools import lru_cache


@dataclass
class Unit:
    text: str
    roman: str  # 只含 a-z；空字串代表沒有讀音


_KANA_ONLY = re.compile(r"[぀-ヿー]+")
_HAN_RUN = re.compile(r"[一-鿿㐀-䶿]+")
_WORD = re.compile(r"[A-Za-z']+|\s+|.")
_SMALL = set("ゃゅょぁぃぅぇぉゎャュョァィゥェォヮ")
HAN_LANGUAGES = ("zh", "nan", "yue")   # 一個漢字一個單位
MANUAL_READING = ("nan", "yue")        # 讀音只靠使用者手動標註


def split(line: str, language: str | None, rubies=()) -> list[Unit]:
    """rubies：使用者指定的讀音（lyrics.Ruby），會蓋過自動判斷的讀音。"""
    if language == "ja":
        units = _split_ja(line)
    elif language in HAN_LANGUAGES:
        units = _split_zh(line)
    else:
        units = _split_words(line)
    # 保險：萬一切出來的字和原句對不上，就不要用這個結果。
    if "".join(u.text for u in units) != line:
        return []
    for ruby in sorted(rubies, key=lambda r: r.start):
        units = _override(units, line, ruby)
    return units


def _override(units: list[Unit], line: str, ruby) -> list[Unit]:
    """把和 ruby 範圍重疊的單位合併成一個，讀音改用指定的讀音。"""
    offsets, pos = [], 0
    for u in units:
        offsets.append((pos, pos + len(u.text)))
        pos += len(u.text)
    hit = [i for i, (s, e) in enumerate(offsets) if s < ruby.end and e > ruby.start]
    if not hit:
        return units
    first, last = hit[0], hit[-1]
    s0, e1 = offsets[first][0], offsets[last][1]
    # 一段標了好幾個音節、剛好一字一音（台語、粵語常見）：逐字分配，填色才會一個字一個字走。
    syllables = [syl for syl in re.split(r"[\s\-]+", ruby.reading.strip()) if syl]
    voiced = [u for u in units[first:last + 1] if u.roman]
    if (s0, e1) == (ruby.start, ruby.end) and len(syllables) > 1 and len(syllables) == len(voiced):
        it = iter(syllables)
        spread = [Unit(u.text, _reading_roman(next(it)) if u.roman else "") for u in units[first:last + 1]]
        return units[:first] + spread + units[last + 1:]
    # 合併範圍比 ruby 大時（例如「自惚れ」只標了「自惚」），多出的假名照原樣讀。
    roman = _plain_roman(line[s0:ruby.start]) + _reading_roman(ruby.reading) + _plain_roman(line[ruby.end:e1])
    return units[:first] + [Unit(line[s0:e1], roman)] + units[last + 1:]


def _reading_roman(reading: str) -> str:
    return _kana_roman(reading) if _KANA_ONLY.fullmatch(reading) else _letters(reading)


def _plain_roman(text: str) -> str:
    return _kana_roman(text) if text and _KANA_ONLY.fullmatch(text) else _letters(text)


def furigana(line: str, language: str | None, rubies=()) -> list[dict]:
    """給歌詞編輯器顯示用：把一句切成片段，含漢字的片段附上讀音（平假名）。

    回傳 [{"text", "start", "end", "ruby", "manual"}]，片段串起來等於原句。
    日文會自動判斷讀音並去掉送假名（「隠れ」只在「隠」上標「かく」）；
    rubies 是使用者指定的讀音，優先於自動判斷。
    """
    spans = [] if language != "ja" else _auto_furigana(line)
    slots = language in MANUAL_READING   # 台語、粵語：每個漢字都可以點來標讀音
    manual = [(r.start, r.end, r.reading) for r in rubies]
    spans = [s for s in spans if all(s[1] <= m[0] or s[0] >= m[1] for m in manual)]
    marked = sorted([(s, e, rd, False) for s, e, rd in spans] + [(s, e, rd, True) for s, e, rd in manual])

    segments, pos = [], 0

    def plain(a: int, b: int) -> None:
        if not slots:
            segments.append({"text": line[a:b], "start": a, "end": b, "ruby": None, "manual": False})
            return
        # 每個漢字自成一段（slot），讓編輯器可以逐字標讀音。
        for k in range(a, b):
            segments.append({"text": line[k], "start": k, "end": k + 1, "ruby": None, "manual": False,
                             "slot": bool(_HAN_RUN.fullmatch(line[k]))})

    for s, e, rd, is_manual in marked:
        if s > pos:
            plain(pos, s)
        segments.append({"text": line[s:e], "start": s, "end": e, "ruby": rd, "manual": is_manual})
        pos = e
    if pos < len(line):
        plain(pos, len(line))
    return segments


def _auto_furigana(line: str) -> list[tuple[int, int, str]]:
    spans, pos = [], 0
    for word in _tagger()(line):
        pos += len(word.white_space)
        surface = word.surface
        start, pos = pos, pos + len(surface)
        kana = getattr(word.feature, "kana", None)
        if not kana or kana == "*" or not _HAN_RUN.search(surface):
            continue
        core, reading = surface, _to_hiragana(kana)
        # 去掉頭尾和讀音相同的假名（送假名），只在漢字上標注。
        while core and reading and _KANA_ONLY.fullmatch(core[-1]) and _to_hiragana(core[-1]) == reading[-1]:
            core, reading = core[:-1], reading[:-1]
        lead = 0
        while core and reading and _KANA_ONLY.fullmatch(core[0]) and _to_hiragana(core[0]) == reading[0]:
            core, reading, lead = core[1:], reading[1:], lead + 1
        if core and reading:
            spans.append((start + lead, start + lead + len(core), reading))
    return spans


def _to_hiragana(text: str) -> str:
    return "".join(chr(ord(c) - 0x60) if "ァ" <= c <= "ヶ" else c for c in text)


def _letters(text: str) -> str:
    # 先拆掉聲調符號（台羅的 á、m̄、o͘，粵拼不受影響），聲調數字與連字號也一併去掉。
    text = "".join(c for c in unicodedata.normalize("NFKD", text) if not unicodedata.combining(c))
    return re.sub(r"[^a-z]", "", text.lower())


@lru_cache(maxsize=1)
def _kakasi():
    import pykakasi
    return pykakasi.kakasi()


@lru_cache(maxsize=1)
def _tagger():
    import fugashi
    return fugashi.Tagger()


def _kana_roman(kana: str) -> str:
    return _letters("".join(item["hepburn"] for item in _kakasi().convert(kana)))


def _morae(kana: str) -> list[str]:
    out: list[str] = []
    pending = ""
    for ch in kana:
        if ch in _SMALL or ch == "ー":
            if out:
                out[-1] += ch
            else:
                pending += ch
        elif ch in "っッ":
            pending += ch
        else:
            out.append(pending + ch)
            pending = ""
    if pending:
        if out:
            out[-1] += pending
        else:
            out.append(pending)
    return out


def _split_ja(line: str) -> list[Unit]:
    units: list[Unit] = []
    for word in _tagger()(line):
        if word.white_space:
            units.append(Unit(word.white_space, ""))
        surface = word.surface
        morae = _morae(surface) if _KANA_ONLY.fullmatch(surface) else []
        if len(morae) > 1:
            units += [Unit(m, _kana_roman(m)) for m in morae]
            continue
        # 用發音欄位（助詞「は」讀 wa、「を」讀 o），沒有時退回讀音、再退回原字。
        reading = getattr(word.feature, "pron", None) or getattr(word.feature, "kana", None)
        if reading and reading != "*" and _KANA_ONLY.fullmatch(reading):
            units.append(Unit(surface, _kana_roman(reading)))
        else:
            units += _split_words(surface)
    return units


def _split_zh(line: str) -> list[Unit]:
    from pypinyin import lazy_pinyin

    units: list[Unit] = []
    pos = 0
    for m in _HAN_RUN.finditer(line):
        units += _split_words(line[pos:m.start()])
        run = m.group()
        for ch, py in zip(run, lazy_pinyin(run)):  # 整段一起轉，破音字才判斷得準
            units.append(Unit(ch, _letters(py.replace("v", "u").replace("ü", "u"))))
        pos = m.end()
    units += _split_words(line[pos:])
    return units


def _split_words(text: str) -> list[Unit]:
    return [Unit(tok, _letters(tok) if tok.strip() else "") for tok in _WORD.findall(text)]
