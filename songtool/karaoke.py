"""KTV 字幕：把歌詞逐字對齊到人聲，產生 ASS 字幕並燒進伴奏影片。

輸出在 output/karaoke/<下載資料夾名稱>/：
    alignment.json        逐字時間軸（對時很花時間，歌詞與人聲沒變就重複使用）；
                          UI 可手動平移某句（或某句之後全部）的時間，記在 adjustments
    <標題>.ass            字幕檔，可以手動微調；改完重跑只會重新燒錄，不會被覆蓋
    <標題>_karaoke.mp4    伴奏 + 字幕
    <標題>_lyrics.mp4     原曲 + 字幕（--target original / both 時）
    karaoke.json          紀錄檔

每個步驟各自判斷要不要重做：
    對時   歌詞內容、人聲檔、模型、語言任一改變
    字幕   重新對時過、時間被手動調整、字幕樣式改變，或 .ass 不見了
    燒錄   .ass 內容或來源影片改變，或輸出檔不見了
"""
from __future__ import annotations

import hashlib
import json
import shutil
import tempfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

from . import ai, align, ass, catalog, config, lyrics as lyrics_mod, manifest, qa, reading, settings, titles
from .download import Download
from .config import FFMPEG
from .media import Cancelled, run_cancellable, video_size
from .separate import SeparateOptions, is_current, output_dir_for, separate_file

ALIGNMENT = "alignment.json"
RECORD = "karaoke.json"
# 燒錄方式有改動時調高，讓舊成品被視為需要更新。
BURN_VERSION = 1

TARGETS = {
    "instrumental": "_karaoke",   # 伴奏 + 字幕
    "original": "_lyrics",        # 原曲 + 字幕
}


@dataclass
class KaraokeOptions:
    whisper_model: str = "large-v3"
    language: str | None = None      # None = 由歌詞文字判斷
    targets: tuple[str, ...] = ("instrumental",)
    font: str | None = None          # None = 依語言挑選
    device: str = "auto"
    realign: bool = False            # 強制重新對時
    force: bool = False              # 全部重做（包含覆蓋手動修改過的 .ass）


@dataclass
class KaraokeResult:
    item: Download
    status: str  # "made" | "skipped" | "no_lyrics" | "failed"
    out_dir: Path
    outputs: list[Path] = field(default_factory=list)
    error: str | None = None


def output_dir(item: Download) -> Path:
    return config.KARAOKE_DIR / item.name


def title_card(item: Download, lyrics_meta: dict) -> list:
    """前奏標題畫面的 [歌名, 演唱者]（有備註時再加第三項 [歌名, 演唱者, 備註]），來源同 catalog.display_info。
    自動辨識不出格式、只能用整個影片標題時不顯示（[None, None]），避免整串 YouTube 標題上畫面。
    沒有備註時維持兩項，舊的紀錄才不會全部變成需更新。"""
    song = catalog.load().songs.get(catalog.song_key(item))
    title, artist = catalog.display_info(item, song, lyrics_meta)
    known = (song and song.title) or lyrics_meta.get("title") or titles.guess(item.info).source != "fallback"
    if not known:
        return [None, None]
    return [title, artist] + ([song.note] if song and song.note else [])


def language_of(item: Download, lyr: lyrics_mod.Lyrics) -> str | None:
    """演唱語言：曲庫手動指定的優先，沒有就依歌詞文字判斷。"""
    song = catalog.load().songs.get(catalog.song_key(item))
    return (song.language if song else "") or lyr.language


def product_sha1(item: Download) -> str | None:
    """成品的指紋（字幕內容的 sha1），給「已確認」標記用；伴唱帶還沒做好時回傳 None。
    字幕由對時、歌詞、樣式決定，重新燒錄或從備份還原重做時，內容一樣指紋就一樣。"""
    if status(item) != "done":
        return None
    return (manifest.read(output_dir(item) / RECORD) or {}).get("ass_sha1")


def approval(item: Download, song) -> str | None:
    """approved（確認過、成品沒變）/ stale（確認後成品變了，需重新確認）/ None（沒確認過）。"""
    if not song or not song.approved:
        return None
    record = manifest.read(output_dir(item) / RECORD) or {}
    return "approved" if record.get("ass_sha1") == song.approved else "stale"


def line_text(line: dict) -> str:
    """一句實際的文字：以逐字的內容為準。
    Whisper 有時會把句子邊界切歪，對時檔裡整句的 text 和歌詞不同（例如把上一句結尾併進來），
    但逐字的內容是照歌詞切的，一定正確。"""
    words = line.get("words")
    return "".join(w["text"] for w in words) if words else line.get("text", "")


def subtitle_ratio() -> float:
    """字幕字高佔畫面高度的比例：預設樣式 × 全域設定的字幕大小。
    設定是 100% 時和原本的樣式完全相同，已經做好的伴唱帶不會變成需更新。"""
    base = ass.Style().size_ratio
    scale = settings.load()["subtitle_scale"]
    return base if scale == 1 else round(base * scale, 5)


def burn_translations(item: Download, lyr: lyrics_mod.Lyrics, line_count: int) -> list[str]:
    """要燒進伴唱帶的中文翻譯（每句一個，沒有翻譯的句子是空字串）。
    歌曲設定不燒、歌詞沒有翻譯、或句數和對時結果對不上時回傳 []。"""
    song = catalog.load().songs.get(catalog.song_key(item))
    translations = lyr.translations
    if (song and not song.translation) or not any(translations) or len(translations) != line_count:
        return []
    return translations


def status(item: Download) -> str:
    """給 --list / UI 用的狀態：no_lyrics / pending / done / outdated。"""
    lyrics_path = lyrics_mod.find(item)
    if lyrics_path is None:
        return "no_lyrics"
    out_dir = output_dir(item)
    record = manifest.read(out_dir / RECORD)
    if not record:
        return "pending"
    ass_path = out_dir / record.get("ass", "")
    fresh = (
        record.get("lyrics_sha1") == _sha1(lyrics_path)
        and ass_path.is_file() and record.get("ass_sha1") == _sha1(ass_path)
        and all(_video_current(v, record["ass_sha1"], out_dir)
                for v in record.get("videos", {}).values())
    )
    if fresh and "alignment_sha1" in record:
        alignment = manifest.read(out_dir / ALIGNMENT)
        fresh = bool(alignment) and record["alignment_sha1"] == _lines_sha1(alignment["lines"])
    if fresh and record.get("style"):
        # 改了全域的字幕大小：做好的伴唱帶都要重新產生字幕並燒錄
        fresh = record["style"].get("size_ratio") == subtitle_ratio()
    if fresh:
        lyr = lyrics_mod.load(lyrics_path)
        alignment = manifest.read(out_dir / ALIGNMENT)
        count = len(alignment["lines"]) if alignment else 0
        translations = burn_translations(item, lyr, count)
        fresh = (record.get("translations", []) == translations   # 切換了翻譯要重新燒錄
                 and record.get("translation_layout") == (ass.TRANSLATION_LAYOUT if translations else None))
    if fresh and ("title_card" in record or "language" in record):
        if "title_card" in record:
            fresh = record["title_card"] == title_card(item, lyr.meta)
        if fresh and record.get("language"):
            fresh = record["language"] == language_of(item, lyr)   # 改了語言要重新對時
    return "done" if fresh else "outdated"


def make(item: Download, opts: KaraokeOptions | None = None, *,
         log: Callable[[str], None] = print,
         should_stop: Callable[[], bool] | None = None) -> KaraokeResult:
    """should_stop() 為真時在下一個安全點中斷並拋出 media.Cancelled。
    對時交給 AI 伺服器，取消時不必等它做完（AI 伺服器做完這一步後會丟掉結果）。"""
    opts = opts or KaraokeOptions()
    out_dir = output_dir(item)
    lyrics_path = lyrics_mod.find(item)
    if lyrics_path is None:
        return KaraokeResult(item, "no_lyrics", out_dir)
    try:
        return _make(item, lyrics_mod.load(lyrics_path), out_dir, opts, log, should_stop)
    except Cancelled:
        raise
    except Exception as exc:
        return KaraokeResult(item, "failed", out_dir, error=str(exc))


def _make(item: Download, lyr: lyrics_mod.Lyrics, out_dir: Path,
          opts: KaraokeOptions, log: Callable[[str], None],
          should_stop: Callable[[], bool] | None = None) -> KaraokeResult:
    def check() -> None:
        if should_stop and should_stop():
            raise Cancelled()

    if not lyr.lines:
        raise RuntimeError(f"歌詞檔沒有內容: {lyr.path}")
    vocals, instrumental = _ensure_separated(item, log, should_stop)
    check()
    out_dir.mkdir(parents=True, exist_ok=True)
    record = manifest.read(out_dir / RECORD) or {}
    language = opts.language or language_of(item, lyr)
    changed = False

    # 1. 對時
    align_key = {
        "lyrics_sha1": lyr.align_sha1,
        "vocals": vocals.name,
        "vocals_size": vocals.stat().st_size,
        "vocals_mtime_ns": vocals.stat().st_mtime_ns,
        "model": opts.whisper_model,
        "language": language,
        "method": align.VERSION,
    }
    alignment = manifest.read(out_dir / ALIGNMENT)
    # 從資料備份還原的對時（scripts/restore.py）：人聲是重新分離的，檔案和當初不同，
    # 但歌詞、語言、對時方法都一樣就直接沿用，手動調整過的時間才不會被重新對時蓋掉。
    restored = (alignment or {}).get("restored")
    if (restored and not (opts.realign or opts.force) and restored.get("lyrics_sha1") == lyr.align_sha1
            and restored.get("language") == language and restored.get("method") == align.VERSION):
        alignment = {"key": align_key, "lines": alignment["lines"], "adjustments": alignment.get("adjustments", [])}
        manifest.write(out_dir / ALIGNMENT, alignment)
        log("  . 沿用資料備份裡的對時（不重新對時）")
    realigned = opts.realign or opts.force or not alignment or alignment.get("key") != align_key
    if realigned:
        if alignment and alignment.get("adjustments"):
            log(f"  . 注意：重新對時會取代先前手動調整的 {len(alignment['adjustments'])} 處時間")
        if language in align.CTC_ONLY:
            log(f"  . 逐字對時中（CTC，{catalog.LANGUAGES.get(language, language)}）...")
        else:
            log(f"  . 逐字對時中（whisper {opts.whisper_model}, 語言 {language or '自動'}）...")
        lines = ai.align_song(vocals, lyr.lines, lyr.rubies, model_name=opts.whisper_model, language=language,
                              device=opts.device, log=log, should_stop=should_stop)
        alignment = {"key": align_key, "lines": lines}
        manifest.write(out_dir / ALIGNMENT, alignment)
        changed = True
        check()
    else:
        log("  . 對時結果沒有變化，沿用 alignment.json")

    # 2. 字幕
    style = ass.Style(font=opts.font or ass.DEFAULT_FONTS.get(language, "Yu Gothic"), size_ratio=subtitle_ratio())
    ass_path = out_dir / f"{item.file.stem}.ass"
    size = video_size(item.file) or (1920, 1080)
    card = title_card(item, lyr.meta)
    # 演唱者只影響字幕顏色，不影響對時；對時結果與歌詞行數一致時才能一句一句對上。
    singers = lyr.singers if len(lyr.singers) == len(alignment["lines"]) else []
    lines_sha1 = _lines_sha1(alignment["lines"])
    retimed = record.get("alignment_sha1", lines_sha1) != lines_sha1   # 舊紀錄沒有這個欄位時不算
    translations = burn_translations(item, lyr, len(alignment["lines"]))
    if retimed and not realigned:
        log("  . 時間已手動調整，重新產生字幕（會取代手動修改過的 .ass）")
    if (realigned or retimed or not ass_path.is_file() or record.get("style") != style.key()
            or record.get("title_card", card) != card or record.get("singers", []) != singers
            or record.get("translations", []) != translations
            or record.get("translation_layout") != (ass.TRANSLATION_LAYOUT if translations else None)):
        log(f"  . 產生字幕 -> {ass_path.name}")
        lines = [dict(line, text=line_text(line)) for line in alignment["lines"]]
        for line, singer in zip(lines, singers):
            line["singer"] = singer
        if language == "ja":
            _attach_furigana(lines, lyr)
        text = ass.build(lines, *size, style, title=card[0], artist=card[1] or None,
                         note=card[2] if len(card) > 2 else None, translations=translations)
        ass_path.write_text(text, encoding="utf-8-sig")
        changed = True
    elif record.get("ass_sha1") != _sha1(ass_path):
        log(f"  . 偵測到 {ass_path.name} 被手動修改，保留修改內容")
    ass_sha1 = _sha1(ass_path)

    # 3. 燒錄
    sources = {"instrumental": instrumental, "original": item.file}
    videos: dict[str, dict] = {}
    outputs = [ass_path]
    for target in opts.targets:
        src = sources[target]
        dest = out_dir / f"{item.file.stem}{TARGETS[target]}.mp4"
        entry = {"file": dest.name, "source": str(src), "source_size": src.stat().st_size,
                 "source_mtime_ns": src.stat().st_mtime_ns, "ass_sha1": ass_sha1,
                 "burn_version": BURN_VERSION}
        if not opts.force and record.get("videos", {}).get(target) == entry and dest.is_file():
            log(f"  . {dest.name} 已是最新")
        else:
            log(f"  . 燒錄字幕 -> {dest.name}")
            check()
            _burn(src, ass_path, dest, size, log, should_stop)
            changed = True
        videos[target] = entry
        outputs.append(dest)

    # 對時品質檢查（本機獨立聽寫比對）；對時沒變且已檢查過就略過。檢查失敗不影響成品。
    check()
    _run_qa(item, lyr, alignment, vocals, language, opts.whisper_model, opts.device, out_dir, log,
            should_stop=should_stop)

    # 這次沒要求的舊成品一併移除，避免留下過期檔案。
    for target, old in record.get("videos", {}).items():
        if target not in videos:
            (out_dir / old["file"]).unlink(missing_ok=True)

    manifest.write(out_dir / RECORD, {
        "lyrics": str(lyr.path),
        "lyrics_sha1": lyr.sha1,
        "language": language,
        "whisper_model": opts.whisper_model,
        "style": style.key(),
        "title_card": card,
        "singers": singers,
        "translations": translations,
        "translation_layout": ass.TRANSLATION_LAYOUT if translations else None,   # 翻譯的位置換了要重新燒錄
        "alignment_sha1": lines_sha1,
        "ass": ass_path.name,
        "ass_sha1": ass_sha1,
        "videos": videos,
        "made_at": manifest.now() if changed else record.get("made_at", manifest.now()),
    })
    return KaraokeResult(item, "made" if changed else "skipped", out_dir, outputs)


def qa_doc(item: Download) -> dict | None:
    """目前有效的對時檢查結果；還沒檢查或對時已經改變時回傳 None。"""
    out_dir = output_dir(item)
    doc = manifest.read(out_dir / qa.QA_FILE)
    alignment = manifest.read(out_dir / ALIGNMENT)
    if not doc or not alignment or doc.get("key") != qa.key_of(alignment["lines"]):
        return None
    return doc


def _lines_sha1(lines: list[dict]) -> str:
    return hashlib.sha1(json.dumps(lines, ensure_ascii=False, sort_keys=True).encode()).hexdigest()


def timing(item: Download) -> dict | None:
    """每句的開始 / 結束時間（給 UI 調整用）；還沒對時回傳 None。"""
    alignment = manifest.read(output_dir(item) / ALIGNMENT)
    if not alignment:
        return None
    return {
        "lines": [{"index": i, "text": line_text(line), "start": line["start"], "end": line["end"],
                   "words": [{"text": w["text"], "start": w["start"], "end": w["end"]} for w in line.get("words", [])]}
                  for i, line in enumerate(alignment["lines"])],
        "adjustments": alignment.get("adjustments", []),
    }


MIN_LINE = 0.3          # 一句至少要有的長度（秒）
MIN_PER_WORD = 0.08     # 每個字至少要有的長度（秒）；一句的最短長度取兩者較大的


def _min_length(line: dict) -> float:
    return max(MIN_LINE, MIN_PER_WORD * len(line.get("words") or [None]))


def _move(line: dict, delta: float) -> None:
    line["start"] = round(line["start"] + delta, 3)
    line["end"] = round(line["end"] + delta, 3)
    for word in line.get("words", []):
        word["start"] = round(word["start"] + delta, 3)
        word["end"] = round(word["end"] + delta, 3)


def _fit_before(line: dict, limit: float) -> None:
    """這句唱到 limit（下一句開頭）之後的話，依比例壓縮每個字的時間，讓它在 limit 前唱完。"""
    end = limit - 0.02
    if line["end"] <= end or line["end"] <= line["start"]:
        return
    factor = (end - line["start"]) / (line["end"] - line["start"])
    origin = line["start"]
    for word in line.get("words", []):
        word["start"] = round(origin + (word["start"] - origin) * factor, 3)
        word["end"] = round(origin + (word["end"] - origin) * factor, 3)
    line["end"] = round(end, 3)


def shift_timing(item: Download, line: int, delta: float, following: bool = True) -> dict:
    """把第 line 句（following=True 時連同之後所有句子）整句平移 delta 秒。

    只移一句時不會被前後的句子擋住（AI 整段對歪好幾句時，「設為現在」也能直接用）：
    - 往後移：這句太短或撞到下一句，就把下一句往後推到最近的合法位置，再撞到就繼續往後推
    - 往前移：撞到上一句（上一句剩下的長度不夠），就把上一句往前推到最近的合法位置，再撞到就繼續往前推
    被推動的句子唱不完的部分依比例壓縮到下一句開頭之前。只有推到歌曲開頭都放不下時才會拒絕。
    回傳的時間資料多一個 pushed：一起被推動的句子（從 0 起算）。
    改完之後伴唱帶顯示「需更新」，重新製作時只會重新產生字幕並燒錄，不必重新對時。
    """
    path = output_dir(item) / ALIGNMENT
    alignment = manifest.read(path)
    if not alignment:
        raise ValueError("還沒有對時結果，請先製作伴唱帶")
    lines = alignment["lines"]
    if not 0 <= line < len(lines):
        raise ValueError("沒有這一句")
    delta = round(float(delta), 3)
    if abs(delta) < 0.001:
        raise ValueError("移動量是 0")
    start = lines[line]["start"]
    new_start = start + delta
    # 往前最多推到：前面每一句都只剩最短長度、第一句從 0 秒開始（只移一句時）
    floor = sum(_min_length(ln) for ln in lines[:line]) if not following else 0.0
    if following and line > 0:
        floor = lines[line - 1]["start"] + _min_length(lines[line - 1])
    if new_start < floor:
        where = "上一句開始" if following and line > 0 else "歌曲開頭（前面的句子都已經擠到最短）"
        raise ValueError(f"最多只能往前移 {max(0.0, start - floor):.2f} 秒（不能早於{where}）")

    _remember_baseline(item, lines)
    pushed: list[int] = []
    if following:
        for target in lines[line:]:
            _move(target, delta)
    else:
        _move(lines[line], delta)
        # 往後推：這句太短或撞到下一句時，下一句移到最近的合法位置，連鎖往後
        k = line
        while k + 1 < len(lines):
            need = lines[k]["start"] + _min_length(lines[k])
            if lines[k + 1]["start"] >= need:
                break
            _move(lines[k + 1], need - lines[k + 1]["start"])
            pushed.append(k + 1)
            k += 1
        # 往前推：撞到上一句（上一句剩下的長度不夠）時，上一句往前移到最近的合法位置，連鎖往前
        k = line
        while k > 0:
            latest = lines[k]["start"] - _min_length(lines[k - 1])
            if lines[k - 1]["start"] <= latest:
                break
            _move(lines[k - 1], latest - lines[k - 1]["start"])
            pushed.append(k - 1)
            k -= 1
        # 被移動的句子（和它的上一句）唱不完的部分壓縮到下一句開頭之前
        moved = sorted({line, *pushed})
        for k in sorted({*moved, *(m - 1 for m in moved if m > 0)}):
            if k + 1 < len(lines):
                _fit_before(lines[k], lines[k + 1]["start"])
    alignment.setdefault("adjustments", []).append(
        {"line": line, "text": lines[line]["text"], "delta": delta, "following": following,
         "pushed": pushed, "at": manifest.now()})
    manifest.write(path, alignment)
    return {**timing(item), "pushed": pushed}


def _remember_baseline(item: Download, lines: list[dict]) -> None:
    """舊版紀錄沒有 alignment_sha1：先補上修改前的值，重新製作時才認得出時間被改過。"""
    record_path = output_dir(item) / RECORD
    record = manifest.read(record_path)
    if record and "alignment_sha1" not in record:
        record["alignment_sha1"] = _lines_sha1(lines)
        manifest.write(record_path, record)


RETIME_MODES = {"from": "重新對時這句及之後全部", "line": "只重對這一句"}


def retime(item: Download, line: int, mode: str, *, log: Callable[[str], None] = print,
           should_stop: Callable[[], bool] | None = None) -> dict:
    """以第 line 句目前的開頭為準，讓 AI 重新對時。

    mode="from"：第 line 句之後全部重新對（只看這個開頭之後的人聲）；之前的句子不動。
    mode="line"：只重對第 line 句每個字的時間，範圍是它的開頭到下一句的開頭。
    和平移一樣只改 alignment.json，重新製作伴唱帶時重產字幕並燒錄、不會整首重新對時。
    """
    if mode not in RETIME_MODES:
        raise ValueError(f"不支援的方式：{mode}")
    out_dir = output_dir(item)
    path = out_dir / ALIGNMENT
    alignment = manifest.read(path)
    lyrics_path = lyrics_mod.find(item)
    if not alignment or lyrics_path is None:
        raise RuntimeError("還沒有對時結果，請先製作伴唱帶")
    lyr = lyrics_mod.load(lyrics_path)
    lines = alignment["lines"]
    # 對時之後歌詞（文字與讀音）有沒有改過：比對對時當下記下的歌詞指紋，不逐句比文字
    # （對時檔裡整句的文字可能被 Whisper 切歪，見 line_text）。
    same = len(lines) == len(lyr.lines) and alignment["key"].get("lyrics_sha1") == lyr.align_sha1
    if not same:
        raise RuntimeError("歌詞改過，和目前的對時結果對不上：請先按「更新伴唱帶」（會整首重新對時）再調整")
    if not 0 <= line < len(lines):
        raise ValueError("沒有這一句")

    vocals, _ = _ensure_separated(item, log, should_stop)
    if should_stop and should_stop():
        raise Cancelled()
    key = alignment["key"]
    anchor = lines[line]["start"]
    log(f"  . {RETIME_MODES[mode]}：第 {line + 1} 句，從 {anchor:.2f}s 開始")
    _remember_baseline(item, lines)
    if mode == "from":
        new = ai.align_from(vocals, lyr.lines, lyr.rubies, line, anchor, model_name=key.get("model", "large-v3"),
                            language=key.get("language"), log=log, should_stop=should_stop)
        if line:
            # 上一句的尾音不能拖過新的起點。
            prev = lines[line - 1]
            tail = prev["words"][-1]
            tail["end"] = round(max(tail["start"] + 0.05, min(tail["end"], anchor)), 3)
            prev["end"] = tail["end"]
        lines[line:] = new
    else:
        nxt = lines[line + 1]["start"] if line + 1 < len(lines) else None
        lines[line] = ai.align_line(vocals, lyr.lines[line], lyr.rubies[line], anchor, nxt,
                                    language=key.get("language"), log=log, should_stop=should_stop)
    alignment.setdefault("adjustments", []).append(
        {"line": line, "text": lines[line]["text"], "retime": mode, "anchor": anchor, "at": manifest.now()})
    manifest.write(path, alignment)
    log(f"  . 完成，重新製作伴唱帶後生效")
    return timing(item)


def check_timing(item: Download, *, log: Callable[[str], None] = print,
                 should_stop: Callable[[], bool] | None = None, force: bool = True) -> dict:
    """單獨檢查一首已經對時過的歌。force=False 時，對時沒變且已檢查過就略過。"""
    out_dir = output_dir(item)
    alignment = manifest.read(out_dir / ALIGNMENT)
    lyrics_path = lyrics_mod.find(item)
    if not alignment or lyrics_path is None:
        raise RuntimeError("還沒有對時結果，請先製作伴唱帶")
    old = manifest.read(out_dir / qa.QA_FILE)
    if not force and old and old.get("key") == qa.key_of(alignment["lines"]):
        log("  . 對時沒有變動、已檢查過，略過")
        return old
    vocals, _ = _ensure_separated(item, log, should_stop)
    key = alignment["key"]
    return _run_qa(item, lyrics_mod.load(lyrics_path), alignment, vocals, key.get("language"),
                   key.get("model", "large-v3"), "auto", out_dir, log, force=force, should_stop=should_stop,
                   strict=True)


def _run_qa(item: Download, lyr: lyrics_mod.Lyrics, alignment: dict, vocals: Path,
            language: str | None, model_name: str, device: str, out_dir: Path,
            log: Callable[[str], None], force: bool = False,
            should_stop: Callable[[], bool] | None = None, strict: bool = False) -> dict | None:
    """strict=False（製作伴唱帶的最後一步）：檢查失敗只記在紀錄裡，不影響成品；
    strict=True（單獨檢查）：失敗就讓這件工作失敗。"""
    path = out_dir / qa.QA_FILE
    old = manifest.read(path)
    if not force and old and old.get("key") == qa.key_of(alignment["lines"]):
        return old
    try:
        doc = ai.check(alignment["lines"], lyr.lines, lyr.rubies, vocals, language,
                       model_name=model_name, device=device, log=log, should_stop=should_stop)
    except Cancelled:
        raise
    except Exception as exc:
        if strict:
            raise
        log(f"  [!] 對時檢查失敗（不影響伴唱帶）：{exc}")
        return None
    doc["checked_at"] = manifest.now()
    manifest.write(path, doc)
    counts = qa.summary(doc)
    if counts["wrong"] or counts["suspect"]:
        log(f"  . 對時檢查：{counts['wrong']} 句可能不準、{counts['suspect']} 句待確認（在歌詞編輯器查看）")
    else:
        log("  . 對時檢查：沒有發現問題")
    return doc


def _attach_furigana(lines: list[dict], lyr: lyrics_mod.Lyrics) -> None:
    """替每句加上假名位置（自動讀音 + 歌詞裡手動指定的讀音）。

    假名要對到字元位置，所以只處理「逐字時間串起來剛好等於歌詞原文」的句子
    （CTC 精修過的句子都是）；對不上的句子不加假名，避免標錯字。
    """
    if len(lines) != len(lyr.lines):
        return
    for line, text, rubies in zip(lines, lyr.lines, lyr.rubies):
        if "".join(w["text"] for w in line["words"]) != text:
            continue
        segments = reading.furigana(text, "ja", rubies)
        line["text"] = text
        line["rubies"] = [(s["start"], s["end"], s["ruby"]) for s in segments if s["ruby"]]


def _ensure_separated(item: Download, log: Callable[[str], None],
                      should_stop: Callable[[], bool] | None = None) -> tuple[Path, Path]:
    """取得人聲與伴奏檔；還沒分離（或只有 4 軌）時自動做 2 軌分離。"""
    sep_dir = output_dir_for(item.file)
    record = manifest.read(sep_dir / manifest.SEPARATE)
    if not (record and record.get("stems") == 2 and is_current(record, item.file, sep_dir)):
        log("  . 尚未有人聲 / 伴奏分離結果，先進行分離")
        result = separate_file(item.file, SeparateOptions(stems=2), log=log, should_stop=should_stop)
        if result.status == "failed":
            raise RuntimeError(f"分離失敗: {result.error}")
        record = manifest.read(sep_dir / manifest.SEPARATE)

    def pick(label: str) -> Path:
        name = next(n for n in record["outputs"] if Path(n).stem.endswith(f"_{label}"))
        return sep_dir / name

    return pick("vocals"), pick("instrumental")


def _burn(src: Path, ass_path: Path, dest: Path, size: tuple[int, int],
          log: Callable[[str], None], should_stop: Callable[[], bool] | None = None) -> None:
    has_video = video_size(src) is not None
    with tempfile.TemporaryDirectory(prefix="karaoke_") as tmpdir:
        tmp = Path(tmpdir)
        # subtitles 濾鏡對路徑中的冒號、括號等字元很敏感，改用暫存目錄裡的 ASCII 檔名。
        shutil.copyfile(ass_path, tmp / "sub.ass")
        tmp_out = tmp / "out.mp4"
        if has_video:
            inputs = ["-i", str(src)]
            maps = ["-map", "0:v:0", "-map", "0:a:0"]
        else:
            # 純音訊來源：用黑底當畫面。
            w, h = size
            inputs = ["-f", "lavfi", "-i", f"color=c=black:s={w}x{h}:r=30", "-i", str(src)]
            maps = ["-map", "0:v:0", "-map", "1:a:0", "-shortest"]
        base = ["-y", "-v", "error", *inputs, *maps, "-vf", "subtitles=sub.ass"]
        tail = ["-c:a", "aac", "-b:a", "320k", "-movflags", "+faststart", str(tmp_out)]

        nvenc = [FFMPEG, *base, "-c:v", "h264_nvenc", "-preset", "p5", "-cq", "23",
                 "-pix_fmt", "yuv420p", *tail]
        x264 = [FFMPEG, *base, "-c:v", "libx264", "-preset", "medium", "-crf", "18",
                "-pix_fmt", "yuv420p", *tail]
        code, _ = run_cancellable(nvenc, should_stop, cwd=tmp)
        if code != 0:
            log("  . NVENC 無法使用，改用 CPU 編碼（較慢）")
            code, err = run_cancellable(x264, should_stop, cwd=tmp)
            if code != 0:
                raise RuntimeError(f"燒錄失敗:\n{err[-2000:]}")
        shutil.move(str(tmp_out), dest)


def _video_current(entry: dict, ass_sha1: str, out_dir: Path) -> bool:
    src = Path(entry.get("source", ""))
    return (
        (out_dir / entry.get("file", "")).is_file() and src.is_file()
        and entry.get("ass_sha1") == ass_sha1
        and entry.get("burn_version") == BURN_VERSION
        and (entry.get("source_size"), entry.get("source_mtime_ns"))
        == (src.stat().st_size, src.stat().st_mtime_ns)
    )


def _sha1(path: Path) -> str:
    return hashlib.sha1(path.read_bytes()).hexdigest()
