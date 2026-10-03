"""用 v1 的 Python 實作產生黃金測試的答案（nas/testdata/golden/*.json），Go 的測試讀它比對。

    .venv-linux/bin/python tools/golden.py            # 全部重新產生
    .venv-linux/bin/python tools/golden.py lyrics     # 只產生名稱含 lyrics 的

輸入全部是自己編的句子或隨機字串（公開 repo，不能放真實歌詞）。
隨機案例用固定的 seed，重新產生的結果相同；v1 的規則改了才會有差異。
"""
from __future__ import annotations

import json
import random
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))

import atexit  # noqa: E402
import hashlib  # noqa: E402
import difflib  # noqa: E402
import os  # noqa: E402
import shutil  # noqa: E402
import tempfile  # noqa: E402

# v1 的 ui/server.py 有 _from_plain；import 時會讀寫設定的資料夾，先指到暫存資料夾，不碰使用者的資料。
_tmp = Path(tempfile.mkdtemp(prefix="kara-golden-"))
atexit.register(shutil.rmtree, _tmp, ignore_errors=True)
for _name in ("SONG_OUTPUT_DIR", "SONG_LYRICS_DIR", "SONG_DATA_DIR", "SONG_HOOKS_DIR"):
    os.environ[_name] = str(_tmp / _name.lower())
sys.path.insert(0, str(ROOT / "ui"))

from songtool import catalog, export, karaoke, lyrics, manifest, reading, titles  # noqa: E402
import server as v1_server  # noqa: E402
from migrate import fingerprint  # noqa: E402

OUT = ROOT / "nas" / "testdata" / "golden"

# ---- 輸入 ------------------------------------------------------------------------

LYRICS_CASES = [
    "",
    "\n\n  \n",
    "# title: 自己編的歌\n# artist: 不存在的歌手\n春の風{かぜ}が吹{ふ}く\n",
    "# TITLE：全形冒號\n#Artist:沒有空白\n# 副歌\n一句歌詞\n",
    "# title:\n# artist:   \n歌名是空的\n",
    "[男] 第一句\n[女]第二句\n[合]  第三句\n[男]\n[路人] 不是演唱者\n",
    "﻿開頭有 BOM 的歌詞\n",
    "Windows 換行\r\n第二句\r\n",
    "全形　空白　收斂\n連續   半形\t空白\n",
    "君の声は私{わたし}の灯り\n{本気|マジ}で\n運命{さだめ}を\n",
    "abc{エービーシー}と漢字{かんじ}{ふりがな}\n",
    "{|空的原字}\n{空讀音|}\n{  }\n沒有收尾的{大括號\n}只有收尾\n",
    "重疊{a|x}{b}\n漢字漢字{かな}{|後面}\n標{一}標{二}\n",
    "你佇{tī}遮\n阮{gún}\n心肝{sim-kuann}\n台灣{Tâi-uân}人\n三字{sann jī}漢字\n",
    "歌詞{go1 ci4}\n粵語{jyut6 jyu5}啊\n一{jat1}二{ji6}\n",
    "第一句\n> 第一句的翻譯\n>  第二行翻譯\n> \n第二句\n\n> 前面是空行，當成註解\n",
    "# 註解\n> 開頭就是翻譯\n",
    "\n\n開頭結尾的空行\n\n\n中間的空行\n\n",
    "Made up English line\nanother line with Numbers 123\n",
    "한국어 가사\n",
    "混合 mixed 文字 と かな\n",
    "運命(さだめ)\n運命{うんめい}(さだめ)\n本気（マジ）だ\n# 註解(かっこ)不動\n今(いま)と未来（みらい）\n",
    "ruby 在英數上 ABC{えーびーしー}123{いちにさん}\n",
    "vertical\x0btab\x0cform\x1cfile\x1dgroup\x1erecord\x85next line para\n",
    "unit\x1fseparator 不是換行但是空白\n",
    "  前後空白  \n\t[女]\t前面有 tab\n",
    "𠀀擴充漢字{よみ}\n㐀擴充A{よみ}\n",
    "{a|b|c}\n{{巢狀}}\n}{反過來\n",
]

ALPHABET = list("君の声私空海運命花風春夏秋冬心肝你佇遮阮台灣人一二三あいうかさたなアイウカサマジーさだめabcABC xyz019")
ALPHABET += ["{", "}", "|", "[男]", "[女]", "[合]", "[", "]", "#", ">", ":", "：", "title", "artist", "# title:",
             " ", "　", "\t", "\n", "\n", "\n", "\r\n", "\r", "(", ")", "（", "）", "ー", "・", "々", "ヶ", "〆",
             "tī", "-", "sim-kuann", "jyut6 ", "á", "ⁿ", "͘", "́", "'", "한", "𠀀", "㐀", "\x1c", " ", " "]


def random_texts(n: int, seed: int) -> list[str]:
    rng = random.Random(seed)
    return ["".join(rng.choice(ALPHABET) for _ in range(rng.randint(1, 40))) for _ in range(n)]


LANGUAGE_CASES = [
    [], [""], ["   "], ["自己編的中文"], ["ひらがな"], ["カタカナ"], ["漢字とかな"], ["한국어"], ["中文 한국어"],
    ["Made up English"], ["English 中文"], ["123 456"], ["a1 2 3"], ["ab 12"], ["ab 123"], ["ëñ"], ["ー"], ["・"],
    ["㐀擴充"], ["𠀀"], ["", "第二句才有字"], ["English", "かな"], ["Café au lait"],
]

# ---- 產生 ------------------------------------------------------------------------


def lyric_case(text: str) -> dict:
    doc = lyrics.parse(text.removeprefix("﻿"))
    lyric = lyrics.Lyrics(Path("x.txt"), doc)
    return {
        "input": text,
        "doc": lyrics.to_dict(doc),
        "serialized": lyrics.serialize(doc),
        "language": lyrics.detect_language(lyric.lines),
        "v1_align_sha1": lyric.align_sha1,
    }


def gen_lyrics_parse() -> list[dict]:
    return [lyric_case(t) for t in LYRICS_CASES + random_texts(400, seed=1)]


def gen_paren() -> list[dict]:
    texts = LYRICS_CASES + random_texts(200, seed=2)
    return [{"input": t, "readings": lyrics.paren_readings(t), "converted": lyrics.paren_to_ruby(t)} for t in texts]


def gen_language() -> list[dict]:
    return [{"lines": lines, "language": lyrics.detect_language(lines)} for lines in LANGUAGE_CASES]


def gen_difflib() -> list[dict]:
    rng = random.Random(3)
    cases = [([], []), (["a"], []), ([], ["a"]), (["a", "b"], ["a", "b"]), (["a", "b", "c"], ["c", "b", "a"])]
    for _ in range(300):
        alphabet = "abcde"[:rng.randint(1, 5)]
        cases.append(([rng.choice(alphabet) for _ in range(rng.randint(0, 12))],
                      [rng.choice(alphabet) for _ in range(rng.randint(0, 12))]))
    out = []
    for a, b in cases:
        ops = difflib.SequenceMatcher(a=a, b=b, autojunk=False).get_opcodes()
        out.append({"a": a, "b": b, "opcodes": [list(op) for op in ops]})
    return out


PLAIN_BASES = [
    "# title: 編的歌\n[男] 第一句{だい}\n> 翻譯一\n[女] 第二句\n> 翻譯二\n# 副歌\n[合] 第三句\n",
    "空{そら}の色{いろ}\n海{うみ}の音{おと}\n風{かぜ}の歌\n",
    "[男] 你佇{tī}遮\n[女] 阮{gún}佇遐\n",
    "",
]
PLAIN_EDITS = [
    "第一句\n第二句\n第三句\n",
    "第一句\n新的一句\n第二句\n第三句\n",
    "第二句\n第三句\n",
    "第一句改了\n第二句\n第三句\n",
    "第一句\n> 新翻譯\n第二句\n第三句\n",
    "[女] 第一句\n第二句\n第三句\n",
    "空の色\n海の音\n風の歌\n",
    "空の色\n海の声\n風の歌\n",
    "空の青\n海の音\n",
    "海の音\n空の色\n",
    "你佇遮\n阮佇遐\n多一句\n",
    "你佇遐\n阮佇遮\n",
    "",
    "全部換掉\n",
]


def gen_lyrics_plain() -> list[dict]:
    out = []
    for base in PLAIN_BASES:
        for plain in PLAIN_EDITS:
            doc = v1_server._from_plain(plain, lyrics.parse(base))
            out.append({"base": lyrics.to_dict(lyrics.parse(base)), "plain": plain, "doc": lyrics.to_dict(doc)})
    return out


# 自己編的歌手、歌名、頻道（不能用真實的）。
TITLE_CASES = [
    {"title": "虛構歌手『花になって』Official Video"},
    {"title": "架空樂團【不存在的歌】歌詞版", "channel": "架空樂團 Official Channel"},
    {"title": "Made Up Band - Imaginary Song (Official Music Video)"},
    {"title": "Imaginary Song - Made Up Band", "channel": "Made Up Band VEVO"},
    {"title": "虛構歌手 Fake Singer【假的歌 The Fake Song】", "channel": "虛構歌手 Fake Singer"},
    {"title": "I love you 伝えたい Fake Artist", "channel": "Fake Artist - Topic"},
    {"title": "Some Song (feat. Another Person)", "channel": "Somebody"},
    {"title": "Some Song ft. Another Person | Somebody"},
    {"title": "主唱 Lead ft. 客串 Guest - 合作的歌"},
    {"title": "【MV】［Official］（Official Video）只有歌名"},
    {"title": "歌名 - Official Video"},
    {"title": "歌名｜歌手｜官方完整版"},
    {"title": "完全沒有規則的標題"},
    {"title": "", "channel": "Empty Title Official YouTube Channel"},
    {"title": "Soft Left Feature 不該被切掉 - 歌手"},
    {"title": "Feat. 開頭就是合作"},
    {"title": "歌手《歌名》MV 首播", "uploader": "上傳者 官方頻道"},
    {"title": "〈歌名〉歌手 4K 1080p"},
    {"title": "\"Quoted Song\" by Someone"},
    {"title": "OFFİCİAL ſTRANGE CAſE Song - Singer"},
    {"title": "x", "track": "Real Track", "artists": ["", " Main Artist ", "Second"]},
    {"title": "x", "track": "Real Track", "artist": "Only Artist ft. Guest"},
    {"title": "x", "track": "  ", "artists": ["Someone"]},
    {"title": "x", "track": "Track (feat. Guest)", "artists": ["主唱 Lead feat. 客串"]},
    {"title": "歌手 - 歌名 (Remastered 2020)"},
    {"title": "歌手 — 歌名 – 版本"},
    {"title": "歌名 [Lyrics] [中文字幕]"},
    {"title": "a/b/c"},
]

TITLE_TOKENS = ["虛構歌手", "架空樂團", "Fake", "Singer", "Band", "歌名", "假的歌", "Song", "I", "love", "伝えたい", "한국", "노래",
                "『", "』", "「", "」", "【", "】", "［", "］", "[", "]", "(", ")", "（", "）", "《", "》", "〈", "〉", "“", "”", '"',
                " - ", " – ", " — ", " | ", "｜", "|", "/", ":", "：", ",", ".", "&", "+",
                "Official", "official video", "MV", "M/V", "Lyrics", "歌詞", "動態歌詞", "官方", "完整版", "4K", "HD", "Remaster",
                "ft.", "feat.", "Feat", "featuring", "FT", "Soft", "left", "Lefty", "ſ", "İ", "ı", "\u212a", "_", "1080p", "Video2",
                " ", " ", " ", "\u3000", "\t"]
CHANNELS = ["", "", "Fake Singer - Topic", "架空樂團 Official Channel", "虛構歌手", "Singer VEVO", "Band official youtube channel",
            "官方頻道", "Channel", "歌名"]


def random_titles(n: int, seed: int) -> list[dict]:
    rng = random.Random(seed)
    out = []
    for _ in range(n):
        info = {"title": "".join(rng.choice(TITLE_TOKENS) + rng.choice(["", " "]) for _ in range(rng.randint(1, 12))),
                "channel": rng.choice(CHANNELS)}
        if rng.random() < 0.1:
            info["track"] = rng.choice(["", "Track", "Track ft. Guest", "歌 (feat. 客)"])
            info["artists"] = rng.choice([[], [""], ["Lead ft. Guest"], ["主唱 Lead", "Other"]])
        out.append(info)
    return out


def gen_titles() -> list[dict]:
    out = []
    for info in TITLE_CASES + random_titles(1500, seed=4):
        g = titles.guess(info)
        out.append({"info": info, "guess": {"title": g.title, "artist": g.artist, "source": g.source},
                    "channel": titles.clean_channel(info.get("channel") or "")})
    return out


def gen_fingerprint() -> dict:
    rng = random.Random(5)
    lyric_cases = []
    for text in LYRICS_CASES + random_texts(100, seed=6):
        doc = lyrics.parse(text)
        texts = [ln.text for ln in doc.lyric_lines]
        rubies = [[{"start": r.start, "end": r.end, "reading": r.reading} for r in ln.rubies] for ln in doc.lyric_lines]
        if rubies and rng.random() < 0.3:
            rubies = [list(reversed(r)) for r in rubies]  # 順序亂掉也要依 start 排序
        lyric_cases.append({"texts": texts, "rubies": rubies, "fingerprint": fingerprint.lyrics(texts, rubies)})
    times = [0, 0.0005, 0.0015, 0.0025, 1.0005, 2.4995, 12.345, 123.4565, 59.9999, 0.1 + 0.2, 1e-9, 3600.5005]
    align_cases = []
    for _ in range(60):
        lines = []
        for _ in range(rng.randint(0, 4)):
            words = [{"text": rng.choice(["空", "の", "a ", "b", "歌詞", ""]), "start": rng.choice(times) + rng.random() * 100,
                      "end": rng.choice(times)} for _ in range(rng.randint(0, 3))]
            lines.append({"text": "".join(w["text"] for w in words), "start": rng.choice(times), "end": rng.choice(times) + rng.random(),
                          "words": words})
        align_cases.append({"lines": lines, "fingerprint": fingerprint.alignment(lines)})
    ms_cases = [{"t": t, "ms": fingerprint.ms(t)} for t in times + [rng.random() * 1000 for _ in range(200)]]
    items_cases = [{"values": v, "items": fingerprint.items(v)} for v in [[], [None], ["男", None, "女"], ["", "合"]]]
    texts = ["", "普通", "有\n換行", "反斜線\\n", "x=y", "全形　空白", "\x1f"]
    stage_cases = []
    for i in range(80):
        pick = lambda: rng.choice(texts)  # noqa: E731
        sha = lambda: hashlib.sha256(str(rng.random()).encode()).hexdigest()  # noqa: E731
        kw = {"target": rng.choice(["instrumental", "original"]), "media": sha(), "alignment": sha(), "lyrics": sha(),
              "singers": fingerprint.items(rng.choice([[], ["男", None]])), "translations": pick(), "title": pick(),
              "artist": pick(), "note": pick(), "scale": rng.choice([1, 0.6, 1.005, 0.125, 1.6, 0.995, 1 / 3]),
              "font": sha(), "size": rng.choice(["1920x1080", "", "640x360"]), "version": rng.randint(1, 9),
              "reading": rng.randint(1, 3),
              "ass": rng.choice([None, None, sha()])}
        stage_cases.append({"kind": "render", "args": kw, "fingerprint": fingerprint.render(**kw)})
        kw = {"source": sha(), "model": "htdemucs", "stems": 2, "version": rng.randint(1, 3)}
        stage_cases.append({"kind": "separate", "args": kw, "fingerprint": fingerprint.separate(**kw)})
        kw = {"lyrics": sha(), "vocals": sha(), "model": "large-v3", "language": rng.choice(["", "ja", "nan"]), "version": 7}
        stage_cases.append({"kind": "align", "args": kw, "fingerprint": fingerprint.align(**kw)})
        kw = {"alignment": sha(), "lyrics": sha(), "vocals": sha(), "language": "zh", "model": "large-v3", "version": 4}
        stage_cases.append({"kind": "qa", "args": kw, "fingerprint": fingerprint.qa(**kw)})
        kw = {"align_key": sha(), "run": rng.choice(["", "r" + sha()[:12]])}
        stage_cases.append({"kind": "approve", "args": kw, "fingerprint": fingerprint.approve(**kw)})
    return {"lyrics": lyric_cases, "alignment": align_cases, "ms": ms_cases, "items": items_cases, "stages": stage_cases}


def gen_catalog() -> list[dict]:
    """隨機的曲庫操作序列。v1 每次修改都重新讀檔（catalog.edit），所以每一步都存檔再讀回，同號時的順序才和 v1 相同。"""
    rng = random.Random(7)
    counter = iter(range(10**9))

    class _Hex:
        def __init__(self):
            self.hex = f"{next(counter):08x}" + "0" * 24

    catalog.uuid.uuid4 = _Hex
    names = ["日文", "中文", "  空白  名稱 ", "", "a", "A", "b", "新資料夾", "子"]
    sequences = []
    for _ in range(60):
        cat = catalog.Catalog()
        steps = []
        for _ in range(rng.randint(5, 40)):
            folders = list(cat.folders)
            songs = list(cat.songs)
            fid = lambda: rng.choice(folders + [None, "missing"]) if rng.random() < 0.9 else None  # noqa: E731
            sid = lambda: rng.choice(songs + ["missing"]) if songs else "missing"  # noqa: E731
            kind = rng.choice(["add_folder", "add_folder", "update_folder", "delete_folder", "ensure_song", "ensure_song",
                               "ensure_song", "update_song", "place", "place", "place", "place"])
            if kind == "add_folder":
                op = {"op": kind, "name": rng.choice(names), "parent": fid(), "number": rng.choice([None, None, 1, 2, 5])}
            elif kind == "update_folder":
                op = {"op": kind, "id": fid(), "name": rng.choice([None, rng.choice(names)]),
                      "number": rng.choice([None, None, 0, 1, 3]), "set_parent": rng.random() < 0.5, "parent": fid()}
            elif kind == "delete_folder":
                op = {"op": kind, "id": fid()}
            elif kind == "ensure_song":
                op = {"op": kind, "key": f"song{rng.randint(0, 15):02d}", "folder": fid()}
            elif kind == "update_song":
                op = {"op": kind, "key": sid(), "set_folder": rng.random() < 0.6, "folder": fid(),
                      "number": rng.choice([None, None, 1, 2, 4, -1])}
            else:
                which = rng.choice(["folder", "song"])
                item = fid() if which == "folder" else sid()
                pool = folders if which == "folder" else songs
                op = {"op": kind, "kind": which, "id": item, "parent": fid(),
                      "before": rng.choice([None] + pool + ["missing"]) if pool else None}
            error = None
            try:
                if op["op"] == "add_folder":
                    op["new_id"] = cat.add_folder(op["name"], op["parent"], op["number"]).id
                elif op["op"] == "update_folder":
                    kw = {"name": op["name"], "number": op["number"]}
                    if op["set_parent"]:
                        kw["parent"] = op["parent"]
                    cat.update_folder(op["id"], **kw)
                elif op["op"] == "delete_folder":
                    cat.delete_folder(op["id"])
                elif op["op"] == "ensure_song":
                    cat.ensure_song(op["key"], op["folder"])
                elif op["op"] == "update_song":
                    kw = {"number": op["number"]}
                    if op["set_folder"]:
                        kw["folder"] = op["folder"]
                    cat.update_song(op["key"], **kw)
                else:
                    cat.place(op["kind"], op["id"], op["parent"], op["before"])
            except (KeyError, ValueError) as exc:
                error = exc.args[0]
            state = cat.to_dict()
            cat = catalog.Catalog.from_dict(state)
            steps.append({"op": op, "error": error,
                          "folders": state["folders"],
                          "songs": {k: {"folder": v["folder"], "number": v["number"]} for k, v in state["songs"].items()}})
        sequences.append(steps)
    return sequences


def gen_shift_timing() -> list[dict]:
    """v1 karaoke.shift_timing：讀寫檔的部分導到暫存資料夾，時間戳固定。"""
    rng = random.Random(8)
    work = _tmp / "shift"
    work.mkdir(parents=True, exist_ok=True)
    karaoke.output_dir = lambda item: work
    manifest.now = lambda: "2026-10-03T12:00:00+08:00"
    cases = []
    for _ in range(400):
        lines, t = [], rng.uniform(0, 5)
        for i in range(rng.randint(1, 8)):
            words, w = [], t
            for k in range(rng.randint(0, 5)):
                length = rng.choice([0, 0.05, 0.1, 0.3, rng.uniform(0, 1.5)])
                words.append({"text": rng.choice(["空", "の", "a ", "歌"]), "start": round(w, 3), "end": round(w + length, 3)})
                w += length + rng.choice([0, 0, 0.02, 0.4])
            end = round(max(w, t + rng.choice([0, 0.1, 0.5])), 3)
            lines.append({"text": f"第{i}句", "start": round(t, 3), "end": end, "words": words})
            t = end + rng.choice([0, 0.01, 0.2, 1, 3, -0.2])
        line = rng.randrange(len(lines)) if rng.random() < 0.95 else len(lines)
        delta = rng.choice([0, 0.0004, 0.1, -0.1, 0.5, -0.5, 2, -2, 10, -10, rng.uniform(-5, 5)])
        following = rng.random() < 0.3
        before = {"key": "k", "lines": lines}
        manifest.write(work / karaoke.ALIGNMENT, before)
        error = result = None
        try:
            result = karaoke.shift_timing(None, line, delta, following)
        except ValueError as exc:
            error = str(exc)
        after = manifest.read(work / karaoke.ALIGNMENT)
        cases.append({"lines": lines, "line": line, "delta": delta, "following": following, "error": error,
                      "pushed": result["pushed"] if result else None, "after": after})
    return cases


READING_CASES = [
    ("ja", "# title: 編的歌\n[女] 空を見上げて歩いた日々\n> 抬頭看著天空走過的日子\n君の声{こえ}は私{わたし}の灯り\n{本気|マジ}で笑った\n# 副歌\n\n明日{あした}へ 走り出す\n"),
    ("ja", "運命(さだめ)を信じて\n今日も明日も\n"),
    ("", "風が吹く丘の上で\n花{はな}が咲いた\n"),
    ("", "自己編的中文歌詞\n第二句\n> 翻譯\n"),
    ("nan", "[男] 你佇{tī}遮\n[女] 阮{gún}佇遐\n心肝{sim-kuann}寶貝\n"),
    ("yue", "我哋一齊{jat1 cai4}行\n"),
    ("en", "Made up English line\nanother line\n"),
    ("ja", ""),
    ("ja", "全部ひらがなのうた\nカタカナモアル\n漢字だけ\n"),
    ("ja", "空{から}っぽの部屋\n空{そら}と海\n{見上|みあ}げる\n見上{みあ}げる\n"),
]


def gen_readings() -> list[dict]:
    out = []
    for language, text in READING_CASES:
        doc = lyrics.parse(text)
        auto = {ln.text: [list(sp) for sp in reading._auto_furigana(ln.text)] for ln in doc.lyric_lines}
        views = v1_server._lyrics_views(doc, language or None)
        annotated = views["annotated"]
        # 標註原文改一個讀音再轉回來：和自動讀音相同的不算手動
        edited = annotated.replace("{そら}", "{くう}", 1)
        back = v1_server._from_annotated(annotated, language or None)
        back_edited = v1_server._from_annotated(edited, language or None)
        for ln in lyrics.parse(edited).lyric_lines + lyrics.parse(annotated).lyric_lines:
            auto.setdefault(ln.text, [list(sp) for sp in reading._auto_furigana(ln.text)])
        out.append({"language": language, "text": text, "auto": auto, "views": views,
                    "annotated": annotated, "from_annotated": lyrics.to_dict(back),
                    "edited": edited, "from_edited": lyrics.to_dict(back_edited)})
    return out


def gen_export_names() -> dict:
    """v1 export.file_name 與 targets（檔名、同名編號、資料夾名稱）。"""
    from types import SimpleNamespace
    rng = random.Random(9)
    pieces = ["歌名", "歌手", "Song", "song", "SONG", "a/b", "c:d", "問?號", "星*", "<角>", "\"引號\"", "pipe|", "back\\slash",
              "結尾點.", "結尾空白 ", " ", "", "\x01控制", "ß", "SS", "İ", "很長" * 100, "  前後  "]
    names = []
    for _ in range(300):
        title = "".join(rng.choice(pieces) for _ in range(rng.randint(0, 3)))
        artist = "".join(rng.choice(pieces) for _ in range(rng.randint(0, 2)))
        copy = rng.choice([1, 1, 2, 3])
        names.append({"title": title, "artist": artist, "copy": copy, "name": export.file_name(title, artist, copy)})

    export.catalog.song_key = lambda item: item.key
    export.lyrics.find = lambda item: None
    export.catalog.display_info = lambda item, song, meta: (item.title, item.artist)
    libraries = []
    for _ in range(40):
        cat = catalog.Catalog()
        folders = [None]
        for k in range(rng.randint(0, 5)):
            parent = rng.choice(folders)
            name = rng.choice(["日文", "中文", "a/b", "結尾點.", "  ", "子", "日文"])
            f = catalog.Folder(f"f{k:07d}", name, rng.randint(1, 3), parent)
            cat.folders[f.id] = f
            folders.append(f.id)
        items = []
        for k in range(rng.randint(1, 12)):
            key = f"s{k:02d}"
            cat.songs[key] = catalog.Song(key, rng.choice(folders), rng.randint(1, 4))
            items.append(SimpleNamespace(key=key, title=rng.choice(["晴天", "晴天", "Song", "song", "雨"]),
                                         artist=rng.choice(["", "歌手", "歌手 ", "ß", "SS"])))
        state = cat.to_dict()
        libraries.append({"folders": state["folders"],
                          "songs": {k: {"folder": v["folder"], "number": v["number"]} for k, v in state["songs"].items()},
                          "display": {i.key: [i.title, i.artist] for i in items},
                          "targets": export.targets(catalog.Catalog.from_dict(state), items)})
    return {"names": names, "libraries": libraries}


GENERATORS = {
    "lyrics_parse": gen_lyrics_parse,
    "paren": gen_paren,
    "language": gen_language,
    "difflib": gen_difflib,
    "lyrics_plain": gen_lyrics_plain,
    "titles": gen_titles,
    "fingerprint": gen_fingerprint,
    "catalog": gen_catalog,
    "shift_timing": gen_shift_timing,
    "readings": gen_readings,
    "export_names": gen_export_names,
}


def main() -> None:
    wanted = sys.argv[1:]
    OUT.mkdir(parents=True, exist_ok=True)
    for name, gen in GENERATORS.items():
        if wanted and not any(w in name for w in wanted):
            continue
        cases = gen()
        path = OUT / f"{name}.json"
        path.write_text(json.dumps(cases, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
        print(f"{path.relative_to(ROOT)}：{len(cases)} 筆")


if __name__ == "__main__":
    main()
