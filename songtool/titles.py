"""從影片資訊推測歌名與演唱者。

依序嘗試：
1. yt-dlp 的歌曲資訊（track / artists）：YouTube Music、「- Topic」頻道等自動產生的影片會有，最準。
2. 標題規則解析：
   - 先去掉只含雜訊的括號，例如 【Official】［MV］（Official Video）
   - 『』「」《》〈〉“” 裡的是歌名，括號前面是演唱者：緑黄色社会『花になって』Official Video
   - 沒有引號時，【】［］[] 裡不是雜訊的內容當歌名：周杰倫【晴天】歌詞
   - 「演唱者 - 歌名」：Ed Sheeran - Shape of You (Official Music Video)
   - 都不符合就整個標題當歌名
   演唱者找不到時用頻道名稱（去掉 - Topic、Official Channel、VEVO 等）。
中英並列時去掉英文譯名：周杰倫 Jay Chou【最後的戰役 The Final Battle】 -> 最後的戰役 / 周杰倫。
演唱者只留主要歌手：去掉 ft. / feat. / featuring 之後的合作歌手（yt-dlp 有多位歌手時取第一位），
歌名裡的「(feat. …)」也一併去掉。
"""
from __future__ import annotations

import re
from dataclasses import dataclass

# 括號內或頭尾出現時視為雜訊的詞（不分大小寫，長的要排前面）。
_NOISE_PHRASES = [
    "official music video", "official lyric video", "official lyrics video", "official audio",
    "official video", "official mv", "official m/v", "music video", "lyric video", "lyrics video",
    "music clip", "visualizer", "remastered", "remaster", "full version", "full ver.", "full ver",
    "short version", "short ver.", "short ver", "official", "audio", "lyrics", "lyric", "video",
    "m/v", "mv", "pv", "hd", "hq", "4k", "1080p", "720p", "teaser", "premiere",
    "官方完整版", "官方版", "官方", "完整版", "高音質", "高畫質", "動態歌詞", "歌詞版", "歌詞",
    "中日字幕", "中文字幕", "中字", "字幕", "首播",
]
_NOISE = re.compile(
    r"(?<![\w])(?:" + "|".join(re.escape(p) for p in _NOISE_PHRASES) + r")(?![\w])", re.IGNORECASE)

_QUOTES = [("『", "』"), ("「", "」"), ("《", "》"), ("〈", "〉"), ("“", "”"), ('"', '"')]
_SQUARE = [("【", "】"), ("［", "］"), ("[", "]"), ("〔", "〕")]
_ROUND = [("(", ")"), ("（", "）")]
_SEPARATOR = re.compile(r"\s+[-–—|｜]\s+|\s*[｜|]\s*")
_CJK = re.compile(r"[぀-ヿ㐀-䶿一-鿿가-힯]")
# 合作歌手標記：前後不能緊接英文字母（避免切到 Soft、Left 這類單字）；中文字緊貼也算。
_FEAT_WORD = r"(?<![A-Za-z])(?:ft|feat|featuring)(?![A-Za-z])\.?"
_FEAT_TAIL = re.compile(r"\s*" + _FEAT_WORD + r".*$", re.IGNORECASE)
_FEAT_BRACKET = re.compile(r"\s*[\(\[（【]\s*" + _FEAT_WORD + r"[^\)\]）】]*[\)\]）】]", re.IGNORECASE)
_CHANNEL_SUFFIX = re.compile(
    r"\s*(?:-\s*topic|official\s+youtube\s+channel|youtube\s+channel|official\s+channel|"
    r"official|vevo|官方頻道|官方|channel)\s*$", re.IGNORECASE)


@dataclass
class Guess:
    title: str
    artist: str
    source: str   # "metadata" | "title" | "fallback"


def guess(info: dict) -> Guess:
    g = _guess(info)
    return Guess(_strip_feat_title(g.title) or g.title, _main_artist(g.artist), g.source)


def _guess(info: dict) -> Guess:
    track = (info.get("track") or "").strip()
    artists = [a.strip() for a in info.get("artists") or ([info["artist"]] if info.get("artist") else [])]
    if track and any(artists):
        return Guess(track, next(a for a in artists if a), "metadata")

    raw = " ".join((info.get("title") or "").split())
    channel = clean_channel(info.get("channel") or info.get("uploader") or "")
    text = _drop_noise_brackets(raw)

    for pairs in (_QUOTES, _SQUARE):
        found = _first_bracket(text, pairs)
        if found:
            start, end, inner = found
            title = _strip_translation(_clean(inner), keep_leading=True)
            artist = _strip_translation(_clean(text[:start])) or channel
            if title:
                return Guess(title, artist, "title")

    parts = [p for p in _SEPARATOR.split(text) if _clean(p)]
    if len(parts) >= 2:
        left, right = _clean(parts[0]), _clean(parts[1])
        # 頻道名稱出現在右邊時代表是「歌名 - 演唱者」。
        if channel and _similar(right, channel) and not _similar(left, channel):
            left, right = right, left
        return Guess(_strip_translation(right, keep_leading=True), _strip_translation(left), "title")

    return Guess(_clean(text) or raw, channel, "fallback")


def _main_artist(name: str) -> str:
    """「李榮浩 Ronghao Li ft. 張惠妹 aMEI」->「李榮浩」：去掉 ft. 之後的合作歌手與英文譯名。"""
    return _strip_translation(_FEAT_TAIL.sub("", name).strip(" -–—,，、&")) or name


def _strip_feat_title(title: str) -> str:
    """「Uptown Funk (feat. Bruno Mars)」、「Uptown Funk ft. Bruno Mars」->「Uptown Funk」。"""
    title = _FEAT_BRACKET.sub("", title)
    return _FEAT_TAIL.sub("", title).strip(" -–—,，")


def clean_channel(name: str) -> str:
    name = " ".join(name.split())
    while True:
        stripped = _CHANNEL_SUFFIX.sub("", name).strip()
        if stripped == name or not stripped:
            return _strip_translation(name)
        name = stripped


def _drop_noise_brackets(text: str) -> str:
    """去掉內容全是雜訊的括號，例如【Official】（Official Video）［MV］。"""
    for open_, close in _SQUARE + _ROUND + _QUOTES[2:]:
        pattern = re.compile(re.escape(open_) + r"([^" + re.escape(open_ + close) + r"]*)" + re.escape(close))
        text = pattern.sub(lambda m: " " if _is_noise(m.group(1)) else m.group(0), text)
    # 結尾的「- Official Video」這類雜訊
    text = re.sub(r"[\s\-–—|｜:：/]*(?:" + _NOISE.pattern + r")[\s\-–—|｜:：/]*$", "", text,
                  flags=re.IGNORECASE)
    return " ".join(text.split())


def _is_noise(inner: str) -> bool:
    return not re.sub(r"[\s\-–—|｜:：/.,&+]", "", _NOISE.sub("", inner))


def _first_bracket(text: str, pairs) -> tuple[int, int, str] | None:
    best = None
    for open_, close in pairs:
        i = text.find(open_)
        if i == -1:
            continue
        j = text.find(close, i + 1)
        if j == -1:
            continue
        inner = text[i + 1:j]
        if not _is_noise(inner) and (best is None or i < best[0]):
            best = (i, j, inner)
    return best


def _clean(text: str) -> str:
    """去掉雜訊詞與頭尾的分隔符號、括號殘留。"""
    text = _NOISE.sub(" ", text)
    text = re.sub(r"[\(\)（）\[\]［］【】]", " ", text)
    return " ".join(text.split()).strip(" -–—|｜:：/,.")


def _strip_translation(text: str, keep_leading: bool = False) -> str:
    """中英並列時去掉英文譯名：「周杰倫 Jay Chou」->「周杰倫」。
    歌名只去掉後面的英文（keep_leading），避免「I love you 伝えたい」這類歌名被切掉。"""
    tokens = text.split()
    if not any(_CJK.search(t) for t in tokens):
        return text
    latin = lambda t: not _CJK.search(t)  # noqa: E731
    while tokens and latin(tokens[-1]):
        tokens.pop()
    while not keep_leading and tokens and latin(tokens[0]):
        tokens.pop(0)
    return " ".join(tokens)


def _similar(a: str, b: str) -> bool:
    a, b = a.casefold().replace(" ", ""), b.casefold().replace(" ", "")
    return bool(a and b) and (a in b or b in a)
