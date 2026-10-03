"""把逐字時間軸轉成 KTV 風格的 ASS 字幕。

版面比照 KTV：畫面下方兩行交替，上行靠左、下行靠右。一句唱完後，
同一位置立刻換上再下一句，讓歌詞永遠提前一句出現。句與句之間隔太久
（間奏）時視為新的一段，下一段的前兩句會在開唱前一起出現。
逐字變色使用 \\kf（由左到右漸變填色）。

一句太長、放不下畫面寬度時拆成兩句（照樣上下交替），不縮小字級。
斷點優先選空白處（日文歌詞的空白通常是樂句分界），其次選演唱時停頓較久的地方，
並盡量讓兩半一樣長；拆完還放不下就再拆。只有單一個詞就放不下時才縮小字級。

合唱：句子帶 "singer"（男 / 女 / 合）時，唱過的顏色依演唱者改變。
假名：句子帶 "rubies"（[(開始字位置, 結束字位置, 讀音)]）時，在漢字正上方另外畫一行
小字，跟著下方漢字同步變色。位置用實際字型量出來（Pillow），量不到時退回估算。
"""
from __future__ import annotations

import os
import struct
from dataclasses import asdict, dataclass
from pathlib import Path

# 依語言挑選 Windows 內建、字形正確的字型。
DEFAULT_FONTS = {"ja": "Yu Gothic", "zh": "Microsoft JhengHei", "nan": "Microsoft JhengHei",
                 "yue": "Microsoft JhengHei", "ko": "Malgun Gothic", "en": "Segoe UI"}
# 字幕使用粗體；量字寬時要用同一個字型檔（檔名, TTC 內的索引）。
FONT_FILES = {
    "Yu Gothic": ("YuGothB.ttc", 0),
    "Microsoft JhengHei": ("msjhbd.ttc", 0),
    "Segoe UI": ("segoeuib.ttf", 0),
    "Malgun Gothic": ("malgunbd.ttf", 0),
}


@dataclass
class Style:
    font: str = "Yu Gothic"
    sung: str = "#1E90FF"       # 唱過的字
    unsung: str = "#FFFFFF"     # 還沒唱的字
    outline: str = "#101010"
    size_ratio: float = 0.075   # 字高 / 畫面高
    lead: float = 2.5           # 每段開頭提前幾秒出現
    tail: float = 0.6           # 每段最後一句唱完後停留幾秒
    break_gap: float = 6.0      # 句間超過幾秒視為間奏
    long_lines: str = "split"   # 太長的句子拆成兩句（新增此欄也讓舊字幕重新產生）
    # 合唱：依演唱者換「唱過」的顏色（KTV 慣例男藍、女紅、合綠）；沒標註的句子用 sung。
    male: str = "#1E90FF"
    female: str = "#FF4F9A"
    duet: str = "#2ECC71"
    furigana: bool = True       # 有讀音資料時在漢字上方顯示假名
    ruby_ratio: float = 0.42    # 假名字高 / 歌詞字高

    def key(self) -> dict:
        return asdict(self)

    def singer_color(self, singer: str | None) -> str | None:
        return {"男": self.male, "女": self.female, "合": self.duet}.get(singer or "")


class _Measure:
    """量出文字在字幕中實際畫出來的寬度（像素）。

    ASS 的字級不是字型的 em 大小：libass 會把字型縮放到「usWinAscent + usWinDescent = 字級」，
    所以實際寬度 = 以字級量出的寬度 × unitsPerEm / (usWinAscent + usWinDescent)
    （Yu Gothic 約 0.777，實測 libass 畫出來是 0.77 倍）。
    找不到字型或沒有 Pillow 時用字數估算。
    """

    def __init__(self, font: str, path: Path | None = None, index: int = 0):
        """path：直接指定字型檔（v2 的 AI worker 用 NAS 給的字型檔）；沒給時依字型名稱找 Windows 的字型。"""
        if path is not None:
            self._file, self._path = (Path(path).name, index), Path(path)
        else:
            self._file = FONT_FILES.get(font)
            self._path = (Path(os.environ.get("WINDIR", "C:/Windows")) / "Fonts" / self._file[0]
                          if self._file else None)
        self._fonts: dict[int, object] = {}
        self._scale = _libass_scale(self._path, self._file[1]) if self._path else None

    def width(self, text: str, size: int) -> float:
        font = self._font(size)
        if font and self._scale:
            return font.getlength(text) * self._scale
        return _units(text) * size * 0.77

    def _font(self, size: int):
        if self._path and size not in self._fonts:
            try:
                from PIL import ImageFont
                self._fonts[size] = ImageFont.truetype(str(self._path), size, index=self._file[1])
            except Exception:
                self._fonts[size] = None
        return self._fonts.get(size)


def _libass_scale(path: Path, index: int) -> float | None:
    """讀字型檔的 head.unitsPerEm 與 OS/2.usWinAscent / usWinDescent（支援 .ttc）。"""
    try:
        with open(path, "rb") as f:
            offset = 0
            if f.read(4) == b"ttcf":
                f.seek(12 + 4 * index)
                (offset,) = struct.unpack(">I", f.read(4))
            f.seek(offset + 4)
            (count,) = struct.unpack(">H", f.read(2))
            f.seek(offset + 12)
            tables = {}
            for _ in range(count):
                tag, _, table_offset, _ = struct.unpack(">4sIII", f.read(16))
                tables[tag] = table_offset
            f.seek(tables[b"head"] + 18)
            (units_per_em,) = struct.unpack(">H", f.read(2))
            f.seek(tables[b"OS/2"] + 74)
            win_ascent, win_descent = struct.unpack(">HH", f.read(4))
        return units_per_em / (win_ascent + win_descent)
    except (OSError, KeyError, struct.error):
        return None


# 中文翻譯：只顯示正在唱的那一句，放在畫面中央上方，不跟著變色。
# 刻意不放進 Style（Style 的內容會用來判斷伴唱帶要不要更新）；位置改了要調 TRANSLATION_LAYOUT，
# 已經燒上翻譯的伴唱帶才會顯示需更新。
TRANSLATION_COLOR = "#FFE08A"
TRANSLATION_RATIO = 0.6      # 相對歌詞字級
TRANSLATION_TOP = 0.05       # 上緣離畫面頂端的距離 / 畫面高
TRANSLATION_LAYOUT = "top-center"


def build(lines: list[dict], width: int, height: int, style: Style,
          title: str | None = None, artist: str | None = None, note: str | None = None,
          translations: list[str] | None = None, font_file: Path | None = None, font_index: int = 0) -> str:
    """lines: [{"text", "start", "end", "words": [{"text", "start", "end"}],
                "singer"?, "rubies"?: [(start, end, reading)]}]
    font_file / font_index：量字寬用的字型檔（沒給時依 style.font 找 Windows 的字型）。"""
    measure = _Measure(style.font, font_file, font_index)
    fs = round(height * style.size_ratio)
    margin_x = round(width * 0.06)
    avail = width - 2 * margin_x
    if not style.furigana:
        lines = [{k: v for k, v in ln.items() if k != "rubies"} for ln in lines]
    # 翻譯以「原本的一句」為單位（長句被拆成兩行也只顯示一次），所以在拆句之前記下來。
    originals = [(ln, tr) for ln, tr in zip(lines, translations or []) if ln.get("words")]
    lines = [part for ln in lines if ln.get("words") for part in _split_long(ln, fs, avail, measure)]

    has_ruby = any(ln.get("rubies") for ln in lines)
    bottom = round(height * 0.07)
    lower_y = height - bottom
    # 有假名時兩行之間多留一行假名的高度，避免下行的假名壓到上行。
    upper_y = lower_y - round(fs * 1.35) - (round(fs * style.ruby_ratio * 1.2) if has_ruby else 0)
    outline_w = max(2, round(fs * 0.07))
    shadow = max(1, round(fs * 0.03))

    events = []
    # 翻譯：從這句開始唱到下一句開始（最多唱完後 style.tail 秒），位置在畫面中央上方。
    trans_y = round(height * TRANSLATION_TOP)
    for k, (line, text) in enumerate(originals):
        if not text:
            continue
        end = line["end"] + style.tail
        if k + 1 < len(originals):
            end = min(end, originals[k + 1][0]["start"] - 0.05)
        size = round(fs * TRANSLATION_RATIO)
        while size > 12 and measure.width(text, size) > avail:
            size -= 1
        events.append((max(0.0, line["start"] - 0.15), max(end, line["start"] + 0.5), "Trans",
                       "{" + f"\\an8\\pos({width // 2},{trans_y})\\fs{size}\\fad(150,150)" + "}" + _escape(text)))
    appear, slots = _schedule(lines, style)
    for i, line in enumerate(lines):
        # 同一位置的下一句出現前要先消失。
        nxt = next((j for j in range(i + 1, len(lines)) if slots[j] == slots[i]), None)
        end = line["end"] + style.tail
        if nxt is not None and appear[nxt] < end + 0.05:
            end = appear[nxt] - 0.05
        size = _fit_size(line["text"], fs, avail, measure)
        y = upper_y if slots[i] == 0 else lower_y
        if slots[i] == 0:
            left = margin_x
            pos = f"\\an1\\pos({margin_x},{y})"
        else:
            left = width - margin_x - measure.width(line["text"], size)
            pos = f"\\an3\\pos({width - margin_x},{y})"
        color = style.singer_color(line.get("singer"))
        tint = f"\\1c{_color(color)}&" if color else ""
        override = pos + (f"\\fs{size}" if size != fs else "") + tint + "\\fad(120,120)"
        events.append((appear[i], end, "KTV", "{" + override + "}" + _karaoke(line["words"], appear[i])))

        for s, e, reading in line.get("rubies") or []:
            events.append((appear[i], end, "Ruby",
                           _ruby(line, s, e, reading, left, y, size, style, measure, tint, appear[i])))

    if title and lines:
        card_end = min(appear[0] - 0.3, 8.0)
        if card_end - 0.5 >= 2.0:
            sub = f"\\N{{\\fs{round(fs * 0.7)}}}{_escape(artist)}" if artist else ""
            if note:
                # 備註在第三行，字比演唱者小；太長就縮小字級，不讓它超出畫面（標題畫面不自動換行）
                note_size = round(fs * 0.5)
                while note_size > 12 and measure.width(note, note_size) > width * 0.9:
                    note_size -= 1
                sub += f"\\N{{\\fs{note_size}}}{_escape(note)}"
            events.append((0.5, card_end, "Title", "{\\fad(400,400)}" + _escape(title) + sub))

    rs = round(fs * style.ruby_ratio)
    header = f"""[Script Info]
ScriptType: v4.00+
PlayResX: {width}
PlayResY: {height}
WrapStyle: 2
ScaledBorderAndShadow: yes
YCbCr Matrix: TV.709

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: KTV,{style.font},{fs},{_color(style.sung)},{_color(style.unsung)},{_color(style.outline)},&H80000000,-1,0,0,0,100,100,0,0,1,{outline_w},{shadow},1,0,0,0,1
Style: Ruby,{style.font},{rs},{_color(style.sung)},{_color(style.unsung)},{_color(style.outline)},&H80000000,-1,0,0,0,100,100,0,0,1,{max(1, round(outline_w * 0.6))},{max(1, round(shadow * 0.6))},2,0,0,0,1
Style: Trans,{style.font},{round(fs * TRANSLATION_RATIO)},{_color(TRANSLATION_COLOR)},{_color(TRANSLATION_COLOR)},{_color(style.outline)},&H80000000,-1,0,0,0,100,100,0,0,1,{max(1, round(outline_w * 0.8))},{shadow},8,0,0,0,1
Style: Title,{style.font},{round(fs * 1.2)},{_color(style.unsung)},{_color(style.unsung)},{_color(style.outline)},&H80000000,-1,0,0,0,100,100,0,0,1,{outline_w},{shadow},5,0,0,0,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
"""
    body = "".join(
        f"Dialogue: 0,{_time(s)},{_time(e)},{name},,0,0,0,,{text}\n"
        for s, e, name, text in sorted(events, key=lambda ev: ev[0])
    )
    return header + body


def _ruby(line: dict, s: int, e: int, reading: str, left: float, y: int, size: int,
          style: Style, measure: _Measure, tint: str, t0: float) -> str:
    """一段假名：置中在 text[s:e] 的正上方，時間跟著那幾個字變色。"""
    text = line["text"]
    x = left + measure.width(text[:s], size) + measure.width(text[s:e], size) / 2
    ruby_bottom = y - round(size * 1.05)  # 字幕行高就是字級，假名貼在這一行上緣之上
    times = _char_times(line["words"])
    start, end = times[s][0], times[e - 1][1]
    wait = max(0, round((start - t0) * 100))
    dur = max(1, round((end - start) * 100))
    fs_override = f"\\fs{round(size * style.ruby_ratio)}"
    return ("{" + f"\\an2\\pos({x:.0f},{ruby_bottom}){fs_override}{tint}\\fad(120,120)" + "}"
            + f"{{\\k{wait}}}{{\\kf{dur}}}" + _escape(reading))


def _char_times(words: list[dict]) -> list[tuple[float, float]]:
    """把每個字（單位）的時間平均分給它的每個字元，給假名對應時間用。"""
    times = []
    for w in words:
        n = len(w["text"])
        span = w["end"] - w["start"]
        times += [(w["start"] + span * k / n, w["start"] + span * (k + 1) / n) for k in range(n)]
    return times


def _schedule(lines: list[dict], style: Style) -> tuple[list[float], list[int]]:
    """決定每句出現的時間與位置（0 = 上行靠左，1 = 下行靠右）。"""
    appear: list[float] = []
    slots: list[int] = []
    block_first = 0
    for i, line in enumerate(lines):
        if i == 0 or line["start"] - lines[i - 1]["end"] > style.break_gap:
            block_first = i
        k = i - block_first
        slots.append(k % 2)
        if k == 0:
            t = line["start"] - style.lead
        elif k == 1:
            t = appear[i - 1]
        else:
            # 同位置的上上句唱完就換上這一句。
            t = lines[i - 2]["end"] + 0.15
        appear.append(max(0.0, min(t, line["start"] - 0.3)))
    return appear, slots


def _karaoke(words: list[dict], t0: float) -> str:
    """以事件開始時間 t0 為基準，組出 {\\k..}{\\kf..}字 的序列（單位：百分之一秒）。"""
    parts = []
    cursor = 0
    for w in words:
        start = max(round((w["start"] - t0) * 100), cursor)
        end = round((w["end"] - t0) * 100)
        if start > cursor:
            parts.append(f"{{\\k{start - cursor}}}")
        dur = max(end - start, 1)
        parts.append(f"{{\\kf{dur}}}{_escape(w['text'])}")
        cursor = start + dur
    return "".join(parts)


def _split_long(line: dict, fs: int, avail: int, measure: _Measure) -> list[dict]:
    """放不下就在最適合的斷點拆開，遞迴直到每段都放得下（或只剩一個詞）。"""
    words = line["words"]
    if len(words) < 2 or measure.width(line["text"].strip(), fs) <= avail:
        return [line]
    i = _best_split(words, fs, avail, measure)
    return (_split_long(_part(line, 0, i), fs, avail, measure)
            + _split_long(_part(line, i, len(words)), fs, avail, measure))


def _best_split(words: list[dict], fs: int, avail: int, measure: _Measure) -> int:
    widths = [measure.width(w["text"], fs) for w in words]
    total = sum(widths) or 1.0
    best, best_score = 1, float("inf")
    left = 0.0
    for i in range(1, len(words)):
        left += widths[i - 1]
        right = total - left
        if not "".join(w["text"] for w in words[:i]).strip() or not "".join(w["text"] for w in words[i:]).strip():
            continue  # 不要切出只有空白的一半
        at_space = words[i - 1]["text"].endswith(" ") or words[i]["text"].startswith(" ")
        pause = max(0.0, words[i]["start"] - words[i - 1]["end"])
        score = (abs(left - right) / total                          # 兩半越平均越好
                 - (0.35 if at_space else 0.0)                      # 空白處（樂句分界）優先
                 - min(pause, 1.0) * 0.5                            # 演唱時的換氣處次之
                 + (0.0 if max(left, right) <= avail else 1.0))     # 拆完放得下最重要
        if score < best_score:
            best, best_score = i, score
    return best


def _part(line: dict, a: int, b: int) -> dict:
    """取出第 a～b 個字組成新的一句；頭尾空白去掉，假名位置跟著平移。"""
    words = [dict(w) for w in line["words"][a:b]]
    offset = sum(len(w["text"]) for w in line["words"][:a])
    lead = len(words[0]["text"]) - len(words[0]["text"].lstrip())
    words[0]["text"] = words[0]["text"].lstrip()
    words[-1]["text"] = words[-1]["text"].rstrip()
    words = [w for w in words if w["text"]] or words[:1]
    text = "".join(w["text"] for w in words)
    base = offset + lead
    rubies = [(s - base, e - base, r) for s, e, r in line.get("rubies") or []
              if s >= base and e <= base + len(text)]
    return {"text": text, "start": words[0]["start"], "end": words[-1]["end"], "words": words,
            "singer": line.get("singer"), "rubies": rubies}


def _units(text: str) -> float:
    """估計文字寬度（以 em 為單位）：全形字 1，半形字約 0.55。"""
    return sum(1.0 if ord(ch) >= 0x2E80 else 0.55 for ch in text)


def _fit_size(text: str, fs: int, avail: int, measure: _Measure) -> int:
    """拆句後仍放不下（單一個詞就太長）才縮小字級；下限只為避免 0。"""
    w = measure.width(text, fs)
    return fs if w <= avail else max(6, int(fs * avail / w))


def _escape(text: str) -> str:
    return text.replace("\\", "＼").replace("{", "｛").replace("}", "｝")


def _color(hex_rgb: str) -> str:
    """#RRGGBB -> ASS 的 &H00BBGGRR。"""
    h = hex_rgb.lstrip("#")
    return f"&H00{h[4:6]}{h[2:4]}{h[0:2]}".upper()


def _time(t: float) -> str:
    cs = max(0, round(t * 100))
    h, rem = divmod(cs, 360000)
    m, rem = divmod(rem, 6000)
    s, cs = divmod(rem, 100)
    return f"{h}:{m:02d}:{s:02d}.{cs:02d}"
