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

import re
import struct
from dataclasses import dataclass
from pathlib import Path


@dataclass
class Style:
    font: str = "Noto Sans CJK JP"   # ASS 的 Fontname（NAS 依語言與設定指定）
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

    def singer_color(self, singer: str | None) -> str | None:
        return {"男": self.male, "女": self.female, "合": self.duet}.get(singer or "")


class _Measure:
    """量出文字在字幕中實際畫出來的寬度（像素）。

    ASS 的字級不是字型的 em 大小：libass 會把字型縮放到「usWinAscent + usWinDescent = 字級」，
    所以實際寬度 = 以字級量出的寬度 × unitsPerEm / (usWinAscent + usWinDescent)
    （Yu Gothic 約 0.777，實測 libass 畫出來是 0.77 倍）。
    找不到字型或沒有 Pillow 時用字數估算。
    """

    def __init__(self, path: Path, index: int = 0):
        self._path, self._index = Path(path), index
        self._fonts: dict[int, object] = {}
        self._scale = _libass_scale(self._path, index)

    def width(self, text: str, size: int) -> float:
        font = self._font(size)
        if font and self._scale:
            return font.getlength(text) * self._scale
        return _units(text) * size * 0.77

    def _font(self, size: int):
        if size not in self._fonts:
            try:
                from PIL import ImageFont
                self._fonts[size] = ImageFont.truetype(str(self._path), size, index=self._index)
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


def build(lines: list[dict], width: int, height: int, style: Style, *, font_file: Path, font_index: int = 0,
          title: str | None = None, artist: str | None = None, note: str | None = None,
          translations: list[str] | None = None) -> str:
    """lines: [{"text", "start", "end", "words": [{"text", "start", "end"}],
                "singer"?, "rubies"?: [(start, end, reading)]}]
    font_file / font_index：量字寬用的字型檔（和燒錄用的是同一個）。"""
    measure = _Measure(font_file, font_index)
    fs = round(height * style.size_ratio)
    margin_x = round(width * 0.06)
    avail = width - 2 * margin_x
    if not style.furigana:
        lines = [{k: v for k, v in ln.items() if k != "rubies"} for ln in lines]
    # 翻譯以「原本的一句」為單位（長句被拆成兩行也只顯示一次），所以在拆句之前記下來。
    originals = [(ln, tr) for ln, tr in zip(lines, translations or [], strict=False) if ln.get("words")]
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

        for text in _rubies(line, left, y, size, style, measure, tint, appear[i]):
            events.append((appear[i], end, "Ruby", text))

    card = _title_window(lines, appear, style) if title and lines else None
    if card:
        # 標題畫面不自動換行（WrapStyle 2）：每一欄自己換行，最多兩行，還放不下才縮小字級
        max_w = width * 0.9
        text = _title_field(title, round(fs * 1.2), max_w, measure)
        if artist:
            text += "\\N" + _title_field(artist, round(fs * 0.7), max_w, measure, sized=True)
        if note:
            text += "\\N" + _title_field(note, round(fs * 0.5), max_w, measure, sized=True)
        # 和歌詞同時出現時往上移，不擋到上行歌詞（最大字幕、含假名）也不擋到上方的翻譯
        pos = f"\\an5\\pos({width // 2},{round(height * TITLE_FORCED_Y)})" if card[2] else ""
        events.append((card[0], card[1], "Title", "{" + pos + "\\fad(400,400)}" + text))

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


RUBY_MIN_SCALE = 0.75   # 讀音比漢字寬時，假名最多縮到這個比例
RUBY_SPACING = 0.15     # 同一段讀音裡每拍之間的間距（假名字級的倍數）
RUBY_GAP = 0.5          # 相鄰兩段讀音之間至少留這麼多（假名字級的倍數），要明顯比段內的間距大


def _rubies(line: dict, left: float, y: int, size: int, style: Style, measure: _Measure, tint: str,
            t0: float) -> list[str]:
    """一句的假名，每個假名一個事件：
    - 讀音比漢字短：每拍之間留一點間距（RUBY_SPACING），整組置中在漢字上方，不拉開
    - 讀音比漢字長：先縮小（最多到 RUBY_MIN_SCALE）；還是放不下就置中凸出，但不壓到前一段假名
    - 兩段讀音的漢字緊鄰時，中間留 RUBY_GAP 的間距（比段內的間距大），看得出分界
    單位是一拍（拗音的小假名跟著前一個字）；每一拍跟著下面對應位置的字變色。"""
    text = line["text"]
    times = _char_times(line["words"])
    ruby_bottom = y - round(size * 1.05)  # 字幕行高就是字級，假名貼在這一行上緣之上
    base = round(size * style.ruby_ratio)
    out: list[str] = []
    prev_right: float | None = None
    gap = base * RUBY_GAP
    spans = sorted(r for r in line.get("rubies") or [] if r[2])
    ends, starts = {r[1] for r in spans}, {r[0] for r in spans}
    for s, e, reading in spans:
        # 緊鄰另一段讀音的那一側留半個間距，相鄰兩段看得出分界
        x0 = left + measure.width(text[:s], size) + (gap / 2 if s in ends else 0)
        span = measure.width(text[s:e], size) - gap / 2 * ((s in ends) + (e in starts))
        units = _morae(reading)
        fs = base
        widths = [measure.width(u, fs) for u in units]
        if sum(widths) > span:
            fs = max(round(base * RUBY_MIN_SCALE), int(base * span / sum(widths)))
            widths = [measure.width(u, fs) for u in units]
        # 每拍之間留一點間距（放得下才留），整組置中在漢字上方；不拉開到整段漢字寬，讀起來還是一個詞
        pad = max(0.0, min(base * RUBY_SPACING, (span - sum(widths)) / len(units)))
        x = x0 + span / 2 - (sum(widths) + pad * len(units)) / 2
        centers = []
        for w in widths:
            centers.append(x + pad / 2 + w / 2)
            x += w + pad
        if prev_right is not None:  # 不壓到前一段凸出來的假名
            push = prev_right + gap - (centers[0] - widths[0] / 2)
            if push > 0:
                centers = [c + push for c in centers]
        prev_right = centers[-1] + widths[-1] / 2
        n = len(units)
        for k, unit in enumerate(units):
            a, b = _time_at(times, s, e, k / n), _time_at(times, s, e, (k + 1) / n)
            wait = max(0, round((a - t0) * 100))
            dur = max(1, round((b - a) * 100))
            out.append("{" + f"\\an2\\pos({centers[k]:.0f},{ruby_bottom})\\fs{fs}{tint}\\fad(120,120)" + "}"
                       + f"{{\\k{wait}}}{{\\kf{dur}}}" + _escape(unit))
    return out


_SMALL_KANA = set("ゃゅょぁぃぅぇぉゎャュョァィゥェォヮ")


def _morae(reading: str) -> list[str]:
    """把讀音切成一拍一拍（拗音的小假名跟著前一個字：きょう → き ょ う 會很難讀，要 きょ う）。"""
    units: list[str] = []
    for ch in reading:
        if ch in _SMALL_KANA and units:
            units[-1] += ch
        else:
            units.append(ch)
    return units


def _time_at(times: list[tuple[float, float]], s: int, e: int, f: float) -> float:
    """text[s:e] 這段從頭唱到 f（0–1）的時間點（逐字的時間內線性內插）。"""
    pos = s + f * (e - s)
    i = min(int(pos), e - 1)
    start, end = times[i]
    return start + (pos - i) * (end - start)


def _char_times(words: list[dict]) -> list[tuple[float, float]]:
    """把每個字（單位）的時間平均分給它的每個字元，給假名對應時間用。"""
    times = []
    for w in words:
        n = len(w["text"])
        span = w["end"] - w["start"]
        times += [(w["start"] + span * k / n, w["start"] + span * (k + 1) / n) for k in range(n)]
    return times


TITLE_MIN = 2.0      # 放在空檔時至少顯示幾秒
TITLE_MAX = 7.5      # 最多顯示幾秒
TITLE_SEARCH = 40.0  # 前奏太短時，往後找這麼多秒內的第一段間奏；找不到就強制放在開頭
TITLE_FORCED_Y = 0.33  # 強制放在開頭（和歌詞同時出現）時，標題中心的高度 / 畫面高：放在上方翻譯和上行歌詞（最大字幕、含假名）之間


def _title_window(lines: list[dict], appear: list[float], style: Style) -> tuple[float, float, bool]:
    """標題畫面的時間：放在第一段夠長的空檔（超過 break_gap 秒，和字幕判斷間奏同一個標準）：
    前奏（歌曲開頭到第一句）夠長就放前奏；開頭直接唱副歌的話，放在開頭 TITLE_SEARCH 秒內的第一段間奏，
    前一句的字幕收掉之後出現、下一段字幕出現之前消失。都沒有就強制放在開頭，和歌詞同時出現（第三個值為 True）。"""
    for i in range(len(lines)):
        if i and lines[i]["start"] > TITLE_SEARCH:
            break
        prev_end = lines[i - 1]["end"] if i else 0.0
        if lines[i]["start"] - prev_end <= style.break_gap:
            continue
        start = prev_end + style.tail + 0.3 if i else 0.5
        end = min(appear[i] - 0.3, start + TITLE_MAX)
        if end - start >= TITLE_MIN:
            return start, end, False
    return 0.5, 0.5 + TITLE_MAX, True


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


TITLE_LINES = 2  # 標題畫面每一欄（歌名、演唱者、備註）最多幾行


def _title_field(text: str, size: int, max_w: float, measure: _Measure, *, sized: bool = False) -> str:
    """標題畫面的一欄：太寬就換行（最多 TITLE_LINES 行，各行盡量一樣長），還放不下才縮小字級。
    sized：這一欄本來就要指定字級（演唱者、備註比歌名小）。"""
    base = size
    while True:
        rows = _balanced_rows(text, size, max_w, measure)
        if len(rows) <= TITLE_LINES or size <= 12:
            break
        size -= 1
    body = "\\N".join(_escape(r) for r in rows)
    if sized or size != base:
        # 換行之後的行也要同樣字級：\fs 會一直有效到下一個 \fs
        return "{\\fs" + str(size) + "}" + body
    return body


_TOKEN = re.compile(r"\s+|[A-Za-z0-9'’.\-]+|.")


def _balanced_rows(text: str, size: int, max_w: float, measure: _Measure) -> list[str]:
    """把一行字切成寬度都不超過 max_w 的幾行，各行盡量一樣長（英文字、數字不從中間切開）。"""
    text = text.strip()
    total = measure.width(text, size)
    if total <= max_w:
        return [text]
    tokens = _TOKEN.findall(text)

    def wrap(limit: float) -> list[str]:
        rows, cur = [], ""
        for tok in tokens:
            if cur.strip() and measure.width((cur + tok).strip(), size) > limit:
                rows.append(cur.strip())
                cur = tok.lstrip()
            else:
                cur += tok
        if cur.strip():
            rows.append(cur.strip())
        return rows

    n = len(wrap(max_w))
    # 從平均寬度開始放寬，找到同樣行數裡各行最平均的切法
    limit = total / n
    while limit < max_w:
        rows = wrap(limit)
        if len(rows) <= n:
            return rows
        limit += max_w * 0.02
    return wrap(max_w)


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
