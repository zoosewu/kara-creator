"""任務裡不需要模型的部分：讀音、字幕大小、ASS 產生。"""
from pathlib import Path

import pytest

pytest.importorskip("fugashi")   # 讀音要 MeCab

from kara_worker import reading, subtitles, tasks  # noqa: E402

NOTO = Path("/usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc")


def test_reading_only_auto_spans():
    out = tasks._reading({"language": "ja", "texts": ["空を見る", "かなだけ", ""]})
    assert out["lines"][0] == [{"start": 0, "end": 1, "ruby": "そら"}, {"start": 2, "end": 3, "ruby": "み"}]
    assert out["lines"][1] == [] and out["lines"][2] == []
    assert tasks._reading({"language": "zh", "texts": ["中文"]})["lines"] == [[]]


def test_subtitle_ratio():
    assert tasks.subtitle_ratio(1) == subtitles.Style().size_ratio      # 100% 時和預設樣式完全相同
    assert tasks.subtitle_ratio(1.2) == round(subtitles.Style().size_ratio * 1.2, 5)


@pytest.mark.skipif(not NOTO.exists(), reason="沒有 Noto Sans CJK")
def test_build_ass():
    # 前奏要夠長才有標題畫面
    words = [{"text": "空", "start": 10.0, "end": 10.5}, {"text": "を", "start": 10.5, "end": 11.0}]
    params = {"lines": [{"text": "空を", "start": 10.0, "end": 11.0, "words": words}], "texts": ["空を"], "rubies": [[]],
              "language": "ja", "singers": ["女"], "translations": ["天空"], "title_card": ["自己編的歌", "虛構歌手"],
              "scale": 1.0, "font": {"sha256": "x", "family": "Noto Sans CJK JP", "index": 0}}
    text = tasks.build_ass(params, (1920, 1080), NOTO)
    assert "Style: KTV,Noto Sans CJK JP," in text
    assert ",Ruby,," in text and "}そ\n" in text and "}ら\n" in text   # 日文自動假名（一拍一個事件）
    assert ",Trans,," in text and "天空" in text         # 翻譯
    assert ",Title,," in text and "自己編的歌" in text    # 標題畫面
    title = next(ln for ln in text.splitlines() if ",Title,," in ln)
    assert "\\pos(" not in title                       # 前奏夠長：置中
    # 一開頭就唱、沒有間奏：強制放在開頭，往上移到翻譯和上行歌詞之間
    params["lines"] = [{"text": "空を", "start": 1.0, "end": 2.0,
                        "words": [{"text": "空", "start": 1.0, "end": 1.5}, {"text": "を", "start": 1.5, "end": 2.0}]}]
    title = next(ln for ln in tasks.build_ass(params, (1920, 1080), NOTO).splitlines() if ",Title,," in ln)
    assert f"\\an5\\pos(960,{round(1080 * subtitles.TITLE_FORCED_Y)})" in title


def test_split_core():
    # 漢字中間夾著假名：讀音拆給各段漢字；對不上就整段標在一起
    assert reading._split_core("取り戻", "とりもど", 0) == [(0, 1, "と"), (2, 3, "もど")]
    assert reading._split_core("見つめ合", "みつめあ", 5) == [(5, 6, "み"), (8, 9, "あ")]
    assert reading._split_core("取り戻", "とももど", 0) == [(0, 3, "とももど")]
    assert reading._split_core("故郷", "ふるさと", 0) == [(0, 2, "ふるさと")]


def test_morae():
    assert subtitles._morae("きょうしゅう") == ["きょ", "う", "しゅ", "う"]
    assert subtitles._morae("ょう") == ["ょ", "う"]


def _ruby_events(text: str, rubies: list) -> tuple[list[tuple[float, int]], "subtitles._Measure"]:
    times = [(i * 0.5, i * 0.5 + 0.5) for i in range(len(text))]
    line = {"text": text, "words": [{"text": c, "start": a, "end": b} for c, (a, b) in zip(text, times, strict=True)],
            "rubies": rubies}
    measure = subtitles._Measure(NOTO, 0)
    out = subtitles._rubies(line, 100, 500, 80, subtitles.Style(), measure, "", 0)
    xs = [float(ev.split("\\pos(")[1].split(",")[0]) for ev in out]
    sizes = [int(ev.split("\\fs")[1].split("\\")[0]) for ev in out]
    return list(zip(xs, sizes, strict=True)), measure


@pytest.mark.skipif(not NOTO.exists(), reason="沒有 Noto Sans CJK")
def test_ruby_layout():
    base = round(80 * subtitles.Style().ruby_ratio)
    # 讀音比漢字短：置中，每拍之間只留一點間距，不拉開到整段漢字寬
    evs, m = _ruby_events("故郷", [(0, 2, "ふる")])
    mid = 100 + m.width("故郷", 80) / 2
    assert abs((evs[0][0] + evs[1][0]) / 2 - mid) < 1
    assert evs[1][0] - evs[0][0] <= m.width("ふ", base) + base * subtitles.RUBY_SPACING + 1
    # 讀音比漢字長：縮小但不小於 75%；凸出時不壓到前一段假名
    evs, m = _ruby_events("空海", [(0, 1, "そらそら"), (1, 2, "うみうみ")])
    assert all(fs >= round(base * subtitles.RUBY_MIN_SCALE) for _, fs in evs)
    fs = evs[0][1]
    first_right = evs[3][0] + m.width("ら", fs) / 2
    second_left = evs[4][0] - m.width("う", fs) / 2
    assert second_left - first_right >= base * subtitles.RUBY_GAP - 1   # 相鄰兩段之間留間距
    # 單獨一段（旁邊沒有讀音）不為了間距縮小
    evs, _ = _ruby_events("夢を", [(0, 1, "ゆめ")])
    assert [fs for _, fs in evs] == [base, base]


def _lines(*spans):
    return [{"text": "歌", "start": a, "end": b, "words": [{"text": "歌", "start": a, "end": b}]} for a, b in spans]


def test_title_window():
    style = subtitles.Style()
    def window(*spans):
        lines = _lines(*spans)
        appear, _ = subtitles._schedule(lines, style)
        return subtitles._title_window(lines, appear, style)
    # 前奏超過 6 秒（和間奏同一個標準）：放在開頭，最多 7.5 秒
    assert window((10, 12), (12, 14)) == (0.5, 7.2, False)
    assert window((6.5, 12), (12, 14)) == (0.5, 6.5 - style.lead - 0.3, False)
    # 前奏不到 6 秒：當成開頭直接唱，改放在間奏
    assert window((5.5, 10), (10, 20), (35, 38))[0] == 20 + style.tail + 0.3
    # 開頭直接唱副歌：放在第一段間奏，前一句字幕收掉之後、下一段字幕出現之前
    start, end, _ = window((1, 5), (5, 20), (35, 38))
    assert start == 20 + style.tail + 0.3 and end == start + subtitles.TITLE_MAX
    start, end, _ = window((1, 5), (5, 20), (29, 31))
    assert end == 29 - style.lead - 0.3
    # 開頭 40 秒內沒有夠長的空檔（間奏太晚、太短、完全沒有）：強制放在開頭
    forced = (0.5, 0.5 + subtitles.TITLE_MAX, True)
    assert window((1, 5), (5, 30), (45, 48)) == forced
    assert window((1, 5), (5, 20), (25, 28)) == forced
    assert window((1, 5), (5, 9)) == forced


def test_bitrate_cap():
    # 上限是來源影像的 1.5 倍，至少 1.5 Mbps；純音訊或讀不到位元率時用最低值
    assert tasks.bitrate_cap(tasks.Video(1920, 1080, 1_800_000)) == 2_700_000
    assert tasks.bitrate_cap(tasks.Video(640, 360, 300_000)) == 1_500_000
    assert tasks.bitrate_cap(tasks.Video(1920, 1080, None)) == 1_500_000
    assert tasks.bitrate_cap(None) == 1_500_000
    args = tasks.encoder_args("h264_nvenc", 2_700_000)
    assert args[args.index("-maxrate") + 1] == "2700000" and args[args.index("-bufsize") + 1] == "5400000"
    assert tasks.encoder_args("不認得", 1) is None
