"""任務裡不需要模型的部分：讀音、字幕大小、ASS 產生。"""
from pathlib import Path

import pytest

pytest.importorskip("fugashi")   # 讀音要 MeCab

from kara_worker import subtitles, tasks  # noqa: E402

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
    assert ",Ruby,," in text and "そら" in text          # 日文自動假名
    assert ",Trans,," in text and "天空" in text         # 翻譯
    assert ",Title,," in text and "自己編的歌" in text    # 標題畫面


def test_bitrate_cap():
    # 上限是來源影像的 1.5 倍，至少 1.5 Mbps；純音訊或讀不到位元率時用最低值
    assert tasks.bitrate_cap(tasks.Video(1920, 1080, 1_800_000)) == 2_700_000
    assert tasks.bitrate_cap(tasks.Video(640, 360, 300_000)) == 1_500_000
    assert tasks.bitrate_cap(tasks.Video(1920, 1080, None)) == 1_500_000
    assert tasks.bitrate_cap(None) == 1_500_000
    args = tasks.encoder_args("h264_nvenc", 2_700_000)
    assert args[args.index("-maxrate") + 1] == "2700000" and args[args.index("-bufsize") + 1] == "5400000"
    assert tasks.encoder_args("不認得", 1) is None
