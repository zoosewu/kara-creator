"""任務裡不需要模型的部分：讀音、字幕大小、ASS 產生。"""
import shutil
import subprocess
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


def test_lyric_readings():
    # 歌詞慣用的讀音：私 → わたし、明日 → あした、何時 → いつ、何 依後面的字唸なに / なん（量詞維持なん）
    cases = {
        "私は君が好き": "わたし", "明日へ": "あした", "何時までも": "いつ",
        "何を言っても": "なに", "何が欲しい": "なに", "何も言わずに": "なに", "何か足りない": "なに", "何気ない日々": "なに",
        "何で泣いてるの": "なん", "何て綺麗な空": "なん", "何だろう": "なん", "何のために": "なん", "何と言えば": "なん",
        "何度でも": "なん", "何回も": "なん",
    }
    for text, ruby in cases.items():
        assert reading.auto_furigana(text)[0][2] == ruby, text
    assert reading.auto_furigana("何時までも")[0] == (0, 2, "いつ")
    assert reading.auto_furigana("私立の学校")[0] == (0, 2, "しりつ")       # 複合詞不受影響
    # 對時用的羅馬字也一樣
    assert [u.roman for u in reading.split("明日の何時か", "ja") if u.roman][:3] == ["ashita", "no", "itsu"]


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


@pytest.mark.skipif(not NOTO.exists(), reason="沒有 Noto Sans CJK")
def test_title_wrap():
    m = subtitles._Measure(NOTO, 0)
    max_w = 1920 * 0.9
    # 太長的歌名：切成兩行，每行都放得下、兩行差不多長
    rows = subtitles._balanced_rows("這是一首名字非常非常長而且一行絕對放不下的自己編的歌曲測試標題", 130, max_w, m)
    widths = [m.width(r, 130) for r in rows]
    assert len(rows) == 2 and max(widths) <= max_w and min(widths) > max(widths) * 0.8
    # 英文字不從中間切開
    rows = subtitles._balanced_rows("Imaginary Singer featuring Another Imaginary Vocalist and Friends", 130, max_w, m)
    assert len(rows) == 2 and all(not r.startswith(("ing", "er")) for r in rows)
    assert " ".join(rows).split() == "Imaginary Singer featuring Another Imaginary Vocalist and Friends".split()
    # 兩行還放不下：縮小字級；短的不動
    field = subtitles._title_field("作詞：" + "很長的名字" * 30, 54, max_w, m, sized=True)
    assert field.count("\\N") == 1 and int(field[4:field.index("}")]) < 54
    assert subtitles._title_field("自己編的歌", 130, max_w, m) == "自己編的歌"


def test_canvas_is_16_9():
    # 窄的左右補、寬的上下補，不放大；超過 4K 才縮小；寬高一定是偶數
    assert tasks.canvas(None) == (1920, 1080)
    assert tasks.canvas(tasks.Video(1920, 1080, None)) == (1920, 1080)
    assert tasks.canvas(tasks.Video(854, 480, None)) == (854, 480)
    assert tasks.canvas(tasks.Video(1080, 1080, None)) == (1920, 1080)
    assert tasks.canvas(tasks.Video(720, 720, None)) == (1280, 720)
    assert tasks.canvas(tasks.Video(640, 480, None)) == (854, 480)
    assert tasks.canvas(tasks.Video(1080, 1920, None)) == (3414, 1920)
    assert tasks.canvas(tasks.Video(2560, 1080, None)) == (2560, 1440)
    assert tasks.canvas(tasks.Video(2160, 3840, None)) == (3840, 2160)


def test_gain():
    # 調到 -16 LUFS；無聲不調；最多 ±20 dB
    assert tasks.gain_for(tasks.Loudness(-20.04, -3.0)) == 4.0
    assert tasks.gain_for(tasks.Loudness(-9.0, 0.5)) == -7.0
    assert tasks.gain_for(tasks.Loudness(None, -90.0)) == 0.0
    assert tasks.gain_for(tasks.Loudness(-50.0, -30.0)) == 20.0
    # 調完會超過 -1 dB 才加限幅器；調小不需要；最高點未知（原曲版）時調大就加
    assert tasks.audio_filter(4.0, -6.0) == "volume=4.0dB"
    assert "alimiter=limit=0.8913:level=0:latency=1" in tasks.audio_filter(4.0, -3.0)
    assert tasks.audio_filter(-7.0, 0.5) == "volume=-7.0dB"
    assert "alimiter" in tasks.audio_filter(1.0, None)


@pytest.mark.skipif(not shutil.which(tasks.FFMPEG), reason="沒有 ffmpeg")
def test_measure_loudness(tmp_path):
    # ffmpeg 預設的 1 kHz 正弦波（振幅 1/8，最高點 -18 dBFS）：約 -21 LUFS → 要調大約 5 dB；無聲 → 不調
    tone, silent = tmp_path / "tone.wav", tmp_path / "silent.wav"
    for path, src in [(tone, "sine=f=1000:d=5"), (silent, "anullsrc=d=5")]:
        subprocess.run([tasks.FFMPEG, "-y", "-v", "error", "-f", "lavfi", "-i", src, "-t", "5", str(path)], check=True)
    loud = tasks.measure_loudness(tone)
    assert loud.lufs is not None and abs(loud.lufs - -21) < 1.0 and -19 < loud.peak_db < -17
    assert tasks.gain_for(loud) == round(-16 - loud.lufs, 1)
    assert tasks.gain_for(tasks.measure_loudness(silent)) == 0.0


def test_trim_filters():
    # 不剪：沒有任何濾鏡；剪頭尾：先剪（字幕照來源時間），字幕之後才歸零、淡入淡出
    none = tasks.Trim()
    assert not none and none.video() == ([], []) and none.audio() == [] and none.seek() == []
    assert tasks.video_filter(tasks.Video(1920, 1080, None), (1920, 1080), False, none) == "null"
    t = tasks.Trim.from_params({"trim_start": 12.5, "trim_end": 200})
    assert t.seek() == ["-ss", "12.500", "-to", "200.000"]
    assert t.audio() == ["atrim=start=12.500:end=200.000", "asetpts=PTS-STARTPTS",
                         "afade=t=in:st=0:d=0.5", "afade=t=out:st=187.000:d=0.5"]
    assert tasks.video_filter(tasks.Video(1080, 1080, None), (1920, 1080), True, t) == (
        "trim=start=12.500:end=200.000,scale=1920:1080:force_original_aspect_ratio=decrease:force_divisible_by=2,"
        "pad=1920:1080:(ow-iw)/2:(oh-ih)/2:black,setsar=1,subtitles=sub.ass:fontsdir=fonts,"
        "setpts=PTS-STARTPTS,fade=t=in:st=0:d=0.5,fade=t=out:st=187.000:d=0.5")
    # 只剪開頭：不淡出；只剪結尾：不淡入
    assert tasks.Trim(3, 0).audio()[-1] == "afade=t=in:st=0:d=0.5"
    assert tasks.Trim(0, 60).audio() == ["atrim=start=0.000:end=60.000", "asetpts=PTS-STARTPTS",
                                         "afade=t=out:st=59.500:d=0.5"]
    # 完全被剪掉的句子不要；一部分在範圍裡的留著
    assert [t.keeps({"start": s, "end": e}) for s, e in [(5, 12), (12, 14), (199, 205), (200, 210)]] == [
        False, True, True, False]


@pytest.mark.skipif(not NOTO.exists(), reason="沒有 Noto Sans CJK")
def test_build_ass_trim():
    # 剪掉開頭：被剪掉的句子（含翻譯）不放；時間照來源；前奏從保留的開頭起算（20 秒開始、第一句 30 秒 → 有標題畫面）
    def line(text, start):
        return {"text": text, "start": start, "end": start + 1,
                "words": [{"text": text, "start": start, "end": start + 1}]}
    params = {"lines": [line("剪掉的句子", 5.0), line("留下的句子", 30.0)], "texts": ["剪掉的句子", "留下的句子"],
              "rubies": [[], []], "language": "zh", "translations": ["剪掉的翻譯", "留下的翻譯"],
              "title_card": ["自己編的歌", "虛構歌手"], "scale": 1.0, "trim_start": 20.0,
              "font": {"sha256": "x", "family": "Noto Sans CJK TC", "index": 0}}
    text = tasks.build_ass(params, (1920, 1080), NOTO)
    assert "剪掉" not in text and "留下的句子" in text and "留下的翻譯" in text
    title = next(ln for ln in text.splitlines() if ",Title,," in ln)
    assert title.startswith("Dialogue: 0,0:00:20.50,") and "\\pos(" not in title   # 放在保留的前奏，不強制


def test_bitrate_cap():
    # 上限是來源影像的 1.5 倍，至少 1.5 Mbps；純音訊或讀不到位元率時用最低值
    assert tasks.bitrate_cap(tasks.Video(1920, 1080, 1_800_000)) == 2_700_000
    assert tasks.bitrate_cap(tasks.Video(640, 360, 300_000)) == 1_500_000
    assert tasks.bitrate_cap(tasks.Video(1920, 1080, None)) == 1_500_000
    assert tasks.bitrate_cap(None) == 1_500_000
    args = tasks.encoder_args("h264_nvenc", 2_700_000)
    assert args[args.index("-maxrate") + 1] == "2700000" and args[args.index("-bufsize") + 1] == "5400000"
    assert tasks.encoder_args("不認得", 1) is None
