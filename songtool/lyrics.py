"""歌詞檔的尋找、解析與寫回。

位置：lyrics/<影片id>.txt（建議），也接受 lyrics/<下載資料夾名稱>.txt，
或下載資料夾內的任一 .txt。

格式（UTF-8，一行就是畫面上的一句，空行會忽略）：

    # title: 春の約束              歌曲資訊，前奏時顯示成標題畫面
    # artist: 歌手名
    # 副歌                         其他 # 開頭的行是註解，可用來標段落
    [女] 窓の外に花が咲いた         行首 [男] / [女] / [合] 標註由誰唱
    君の声は私{わたし}の灯り        {讀音}：指定前面連續漢字（或英數字）的讀音
    {本気|マジ}で                   {原字|讀音}：明確指定要標注的範圍

讀音會用於對時，也是日後在影片上顯示假名的依據。
"""
from __future__ import annotations

import hashlib
import json
import re
from dataclasses import asdict, dataclass, field
from pathlib import Path

from . import config
from .download import Download

SINGERS = ("男", "女", "合")

_META = re.compile(r"^#\s*(title|artist)\s*[:：]\s*(.*)$", re.IGNORECASE)
_SINGER = re.compile(r"^\[(男|女|合)\]\s*")
_BASE = re.compile(r"[一-鿿㐀-䶿々〆ヶA-Za-z0-9]")
_HAN = re.compile(r"[一-鿿㐀-䶿]")
# 羅馬字讀音（台羅、粵拼、拼音等，可含聲調符號與數字）；用空白或連字號分隔音節。
_LATIN_READING = re.compile(r"[A-Za-z0-9À-ɏḀ-ỿ̀-ͯ͘ⁿ'\s\-]+")


# 日文歌詞常把特殊念法寫在括號裡：運命(さだめ)、本気（マジ）。
# 只認「漢字緊接著括號、括號裡全是假名」；前面已經有 {讀音} 的話一併取代。
_PAREN_READING = re.compile(r"([一-鿿㐀-䶿々〆ヶ]+)(?:\{[^{}|]*\})?[(（]([぀-ヿー・]+)[)）]")


def paren_readings(text: str) -> list[str]:
    """歌詞文字裡寫在括號中的讀音（例如「運命(さだめ)」），依出現順序、不重複。"""
    found: list[str] = []
    for line in text.splitlines():
        if line.lstrip().startswith("#"):
            continue
        for m in _PAREN_READING.finditer(line):
            if m.group(0) not in found:
                found.append(m.group(0))
    return found


def paren_to_ruby(text: str) -> str:
    """把括號讀音轉成讀音標註：運命(さだめ) -> {運命|さだめ}（註解行不動）。"""
    return "\n".join(line if line.lstrip().startswith("#")
                     else _PAREN_READING.sub(lambda m: f"{{{m.group(1)}|{m.group(2)}}}", line)
                     for line in text.split("\n"))


def _syllables(reading: str) -> list[str] | None:
    """羅馬字讀音拆成音節；不是羅馬字（例如假名）回傳 None。"""
    if not _LATIN_READING.fullmatch(reading):
        return None
    return [s for s in re.split(r"[\s\-]+", reading.strip()) if s]
_KANA = re.compile(r"[぀-ヿ]")
_HANGUL = re.compile(r"[가-힯]")
_HAN = re.compile(r"[一-鿿]")


@dataclass
class Ruby:
    start: int     # 在純文字中的位置 [start, end)
    end: int
    reading: str


@dataclass
class Line:
    kind: str                  # "lyric" | "comment" | "blank"
    text: str = ""             # lyric：去掉標記後的純文字；comment：整行原文
    singer: str | None = None
    rubies: list[Ruby] = field(default_factory=list)
    translation: str = ""      # 中文翻譯（歌詞檔裡下一行的「> 翻譯」）；不參與對時


@dataclass
class Document:
    meta: dict[str, str] = field(default_factory=dict)
    lines: list[Line] = field(default_factory=list)

    @property
    def lyric_lines(self) -> list[Line]:
        return [ln for ln in self.lines if ln.kind == "lyric"]


@dataclass
class Lyrics:
    """給處理流程用的歌詞：只留要唱的句子。"""
    path: Path
    doc: Document
    sha1: str = ""   # 檔案內容的雜湊（任何修改都會改變）

    @property
    def lines(self) -> list[str]:
        return [ln.text for ln in self.doc.lyric_lines]

    @property
    def rubies(self) -> list[list[Ruby]]:
        return [ln.rubies for ln in self.doc.lyric_lines]

    @property
    def singers(self) -> list[str | None]:
        return [ln.singer for ln in self.doc.lyric_lines]

    @property
    def translations(self) -> list[str]:
        """每句的中文翻譯（沒有翻譯的句子是空字串）。"""
        return [ln.translation for ln in self.doc.lyric_lines]

    @property
    def meta(self) -> dict[str, str]:
        return self.doc.meta

    @property
    def align_sha1(self) -> str:
        """只涵蓋會影響對時的內容（歌詞文字與讀音）；改演唱者或標題不必重新對時。"""
        payload = [[ln.text, [asdict(r) for r in ln.rubies]] for ln in self.doc.lyric_lines]
        return hashlib.sha1(json.dumps(payload, ensure_ascii=False).encode()).hexdigest()

    @property
    def language(self) -> str | None:
        return detect_language(self.lines)


def detect_language(lines: list[str]) -> str | None:
    """由文字判斷語言；判斷不出來時回傳 None。"""
    text = "".join(lines)
    if _KANA.search(text):
        return "ja"
    if _HANGUL.search(text):
        return "ko"
    if _HAN.search(text):
        return "zh"
    # 對時一定要指定語言；以拉丁字母為主的歌詞當成英文（其他語言可用 --language 指定）。
    letters = sum(ch.isascii() and ch.isalpha() for ch in text)
    if letters and letters >= 0.5 * len(text.replace(" ", "")):
        return "en"
    return None


# ---- 位置 ----------------------------------------------------------------

def candidates(item: Download) -> list[Path]:
    """依優先順序列出這個項目可能的歌詞位置，第一個是建議位置。"""
    paths = []
    if item.info.get("id"):
        paths.append(config.LYRICS_DIR / f"{item.info['id']}.txt")
    paths.append(config.LYRICS_DIR / f"{item.name}.txt")
    paths += sorted(item.folder.glob("*.txt"))
    return paths


def find(item: Download) -> Path | None:
    return next((p for p in candidates(item) if p.is_file()), None)


def load(path: Path) -> Lyrics:
    raw = path.read_bytes()
    return Lyrics(path, parse(raw.decode("utf-8-sig")), hashlib.sha1(raw).hexdigest())


def save(item: Download, text: str) -> Path:
    """寫回既有的歌詞檔；還沒有歌詞時寫到建議位置。"""
    path = find(item) or candidates(item)[0]
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text.rstrip() + "\n", encoding="utf-8")
    return path


# ---- 解析與寫回 ------------------------------------------------------------

def parse(text: str) -> Document:
    doc = Document()
    for raw in text.splitlines():
        line = raw.strip()
        if not line:
            doc.lines.append(Line("blank"))
            continue
        if line.startswith("#"):
            m = _META.match(line)
            if m:
                doc.meta[m.group(1).lower()] = m.group(2).strip()
            else:
                doc.lines.append(Line("comment", line))
            continue
        if line.startswith(">"):
            # 「> 翻譯」：上一句歌詞的翻譯；前面不是歌詞的話當成註解保留下來
            if doc.lines and doc.lines[-1].kind == "lyric":
                # 同一句寫了好幾行翻譯就接在一起（顯示時只有一行）
                doc.lines[-1].translation = " ".join((doc.lines[-1].translation + " " + line[1:]).split())
            else:
                doc.lines.append(Line("comment", line))
            continue
        doc.lines.append(_parse_lyric(line))
    # 頭尾的空行沒有意義，去掉以免寫回時越積越多。
    while doc.lines and doc.lines[0].kind == "blank":
        doc.lines.pop(0)
    while doc.lines and doc.lines[-1].kind == "blank":
        doc.lines.pop()
    return doc


def _parse_lyric(line: str) -> Line:
    m = _SINGER.match(line)
    singer = m.group(1) if m else None
    body = line[m.end():] if m else line
    body = " ".join(body.split())  # 全形空白也一併收斂成單一半形空白

    text = ""
    rubies: list[Ruby] = []
    i = 0
    while i < len(body):
        if body[i] != "{":
            text += body[i]
            i += 1
            continue
        j = body.find("}", i)
        if j == -1:  # 沒有收尾的大括號當成一般文字
            text += body[i:]
            break
        inner = body[i + 1:j]
        if "|" in inner:
            base, reading = inner.split("|", 1)
            start = len(text)
            text += base
        else:
            reading = inner
            start = len(text)
            # 簡寫標在前面連續的漢字上，但不越過前一個標註。
            floor = max((r.end for r in rubies), default=0)
            while start > floor and _BASE.match(text[start - 1]):
                start -= 1
            # 中文（台語、粵語）一字一音：羅馬字讀音有幾個音節，就只標最後幾個漢字（你佇{tī} 只標「佇」）。
            syllables = _syllables(reading)
            if syllables and _HAN.match(text[start:start + 1] or " ") and len(syllables) < len(text) - start:
                start = len(text) - len(syllables)
        if reading.strip() and start < len(text):
            ruby = Ruby(start, len(text), reading.strip())
            rubies = [r for r in rubies if r.end <= ruby.start or r.start >= ruby.end]
            rubies.append(ruby)
        i = j + 1
    return Line("lyric", text, singer, sorted(rubies, key=lambda r: r.start))


def serialize(doc: Document) -> str:
    out = [f"# {key}: {doc.meta[key]}" for key in ("title", "artist") if doc.meta.get(key)]
    for line in doc.lines:
        if line.kind == "blank":
            out.append("")
        elif line.kind == "comment":
            out.append(line.text)
        else:
            out.append(_serialize_lyric(line))
            if line.translation:
                out.append(f"> {line.translation}")
    return "\n".join(out) + "\n"


def _serialize_lyric(line: Line) -> str:
    parts, pos = [], 0
    for r in sorted(line.rubies, key=lambda r: r.start):
        base = line.text[r.start:r.end]
        before = line.text[:r.start]
        # 能用簡寫（前面連續漢字剛好就是要標注的範圍）就用簡寫，比較好讀。
        short = all(_BASE.match(c) for c in base) and not (before and _BASE.match(before[-1]))
        # 漢字配羅馬字、音節數等於字數時，簡寫也不會標錯範圍（見 _parse_lyric）。
        syllables = _syllables(r.reading)
        if syllables and all(_HAN.match(c) for c in base) and len(syllables) == len(base):
            short = True
        parts.append(line.text[pos:r.start])
        parts.append(f"{base}{{{r.reading}}}" if short else f"{{{base}|{r.reading}}}")
        pos = r.end
    parts.append(line.text[pos:])
    prefix = f"[{line.singer}] " if line.singer else ""
    return prefix + "".join(parts)


def to_dict(doc: Document) -> dict:
    return asdict(doc)


def from_dict(data: dict) -> Document:
    lines = []
    for ln in data.get("lines", []):
        rubies = [Ruby(int(r["start"]), int(r["end"]), str(r["reading"]))
                  for r in ln.get("rubies", []) if str(r.get("reading", "")).strip()]
        singer = ln.get("singer") if ln.get("singer") in SINGERS else None
        lines.append(Line(ln.get("kind", "lyric"), ln.get("text", ""), singer, rubies,
                          " ".join(str(ln.get("translation") or "").split())))
    meta = {k: v for k, v in (data.get("meta") or {}).items() if v}
    return Document(meta, lines)
