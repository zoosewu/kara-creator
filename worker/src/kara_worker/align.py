"""把歌詞逐字對齊到人聲。分三個階段：

1. stable-ts（Whisper）決定每一句大致的位置。它抓「句子在哪」很準，
   但每句第一個字常會把上一句的長音吃進來，導致提早變色。
2. 用人聲軌的音量修正句子層級的錯誤：
   - 整句落在沒有人聲處（例如被塞進前奏）-> 依字數排進前後句之間真正有人聲的地方
   - 句內大跳躍：前一個字唱完後人聲還持續，後面的字卻被放到遠處 -> 移回來
   - 句首跨越停頓：第一個字從上一句延續的人聲開始、中間有停頓 -> 停頓前還給上一句
   - 句首提前：第一個字前面其實已經在發聲 -> 起點提前到發聲處
3. CTC 強制對齊（MMS 多語言模型）在每句的範圍內重新定出每個音的起點。
   日文用 MeCab 取得讀音、中文用拼音，轉成羅馬字後對齊。
   同時做「整段」（緊接的句子一起對齊，擅長慢板、重複的副歌）與「逐句」（擅長句尾長音
   後緊接下一句的快歌）兩種對齊，每句挑首字速度與同句其他字較一致的結果。
   字與字之間人聲連續（長音）時，填色一路延續到下一個字，不會停在半途。
最後補救零長度的字，避免瞬間變色。
"""
from __future__ import annotations

import subprocess
from collections.abc import Callable
from pathlib import Path

import numpy as np

from . import reading
from .lyrics import Lyrics
from .media import FFMPEG

# 對時方法或修正規則有變動時調高 versions.json 的 align（會讓所有歌整首重新對時，手動調整被取代）。

_SR = 16000
_HOP = 0.02            # 音量包絡的時間解析度（秒）
_MIN_GAP = 1.5         # 句內兩字間隔超過這個秒數才檢查是否放錯
_models: dict = {}
_mms: dict = {}


def align(vocals: Path, lyr: Lyrics, *, model_name: str, language: str | None,
          device: str, log: Callable[[str], None]) -> list[dict]:
    device = _resolve_device(device)
    audio = _load_audio(vocals)
    voiced = _voiced_mask(audio)
    if language in CTC_ONLY:
        lines = _ctc_lines(audio, lyr.lines, lyr.rubies, language, device, log)
    else:
        lines = _stable_ts(vocals, lyr, model_name, language, device, log)
        _retry_collapsed(lines, audio, load_whisper(model_name, device, log), language, log)
    for note in repair(lines, voiced):
        log(f"  . 修正 {note}")
    if len(lines) == len(lyr.lines):
        log("  . CTC 逐音精修中...")
        done = refine_ctc(lines, lyr.lines, audio, language, device, lyr.rubies, voiced)
        log(f"  . CTC 精修完成 {done}/{len(lines)} 句")
    else:
        log(f"  [!] 歌詞 {len(lyr.lines)} 行，對時結果 {len(lines)} 行，略過 CTC 精修，請檢查字幕")
    _finalize(lines, lyr.lines)
    return lines


def _finalize(lines: list[dict], texts: list[str]) -> None:
    """補救零長度的字、更新句子的起訖；句數和歌詞一樣時，整句文字直接用歌詞原文
    （Whisper 的分段文字有時會把句子邊界切歪）。"""
    same = len(lines) == len(texts)
    for i, line in enumerate(lines):
        _fix_zero(line["words"])
        line["start"], line["end"] = line["words"][0]["start"], line["words"][-1]["end"]
        if same:
            line["text"] = texts[i]


def align_from(vocals: Path, lyr: Lyrics, first: int, anchor: float, *, model_name: str,
               language: str | None, device: str, log: Callable[[str], None]) -> list[dict]:
    """從第 first 句開始重新對時（使用者已經確認第 first 句從 anchor 秒開始唱）。

    只拿 anchor 之後的人聲去對第 first 句之後的歌詞，流程和 align() 相同
    （Whisper 抓句子位置 -> 人聲修正 -> CTC 逐音精修），所以 AI 不會再被前面的前奏、
    間奏或對錯的段落帶偏。回傳第 first 句（含）之後的新結果，時間為整首歌的絕對時間。
    """
    device = _resolve_device(device)
    texts, rubies = lyr.lines[first:], lyr.rubies[first:]
    t0 = max(0.0, anchor - 0.05)
    audio = _load_audio(vocals)[int(t0 * _SR):]
    if len(audio) < _SR:
        raise ValueError("起點之後的音訊太短")
    # 以下都在「從 t0 開始」的時間軸上計算，最後再加回 t0。
    voiced = _voiced_mask(audio)
    if language in CTC_ONLY:
        lines = _ctc_lines(audio, texts, rubies, language, device, log)
    else:
        model = load_whisper(model_name, device, log)
        result = model.align(audio, "\n".join(texts), language=language,
                             original_split=True, vad=True, verbose=None)
        lines = _segments(result)
        if len(lines) != len(texts):
            raise ValueError(f"AI 對出 {len(lines)} 句，歌詞是 {len(texts)} 句，結果不可靠，沒有套用")
        _retry_collapsed(lines, audio, model, language, log)
    for note in repair(lines, voiced):
        log(f"  . 修正 {note}")
    log("  . CTC 逐音精修中...")
    done = refine_ctc(lines, texts, audio, language, device, rubies, voiced)
    log(f"  . CTC 精修完成 {done}/{len(lines)} 句")
    _pin_start(lines[0], anchor - t0)
    _finalize(lines, texts)
    _offset(lines, t0)
    return lines


def align_line(vocals: Path, text: str, rubies: list, t0: float, t1: float | None, *,
               language: str | None, device: str) -> dict:
    """只重對一句：這句確定從 t0 開始、在 t1（下一句開頭）之前唱完，
    在這個範圍內用 CTC 重新定出每個字的時間。只用 CTC，幾秒就完成。"""
    device = _resolve_device(device)
    audio = _load_audio(vocals)
    voiced = _voiced_mask(audio)
    end = len(audio) / _SR
    t1 = min(t1, end) if t1 is not None else end
    units = reading.split(text, language, rubies)
    if not any(u.roman for u in units):
        raise ValueError("這句沒有可以對齊的讀音")
    spans = _ctc(audio, t0, t1, [units], device, _mms_model(device)) if t1 - t0 >= 0.3 else None
    if not spans:
        raise ValueError("這句的範圍太短（到下一句開頭之前），請先確認這句和下一句的開頭")
    words = spans[0]
    line = {"text": text, "start": words[0]["start"], "end": words[-1]["end"], "words": words}
    # _finish 需要知道下一句從哪裡開始（句尾長音不能超過它）：放一個代表下一句開頭的佔位句。
    marker = {"text": "", "start": t1, "end": t1, "words": [{"text": "", "start": t1, "end": t1}]}
    _finish([line, marker], voiced, end)
    _pin_start(line, t0)
    _fix_zero(line["words"])
    line["start"], line["end"] = line["words"][0]["start"], line["words"][-1]["end"]
    return line


def _pin_start(line: dict, start: float) -> None:
    """使用者指定的句首：第一個字從 start 開始，其他字不早於它。"""
    start = round(start, 3)
    for i, w in enumerate(line["words"]):
        if i == 0 or w["start"] < start:
            w["start"] = start
            w["end"] = max(w["end"], round(start + 0.05, 3))


def _offset(lines: list[dict], dt: float) -> None:
    for line in lines:
        for w in line["words"]:
            w["start"], w["end"] = round(w["start"] + dt, 3), round(w["end"] + dt, 3)
        line["start"], line["end"] = line["words"][0]["start"], line["words"][-1]["end"]


def _resolve_device(device: str) -> str:
    if device != "auto":
        return device
    import torch
    return "cuda" if torch.cuda.is_available() else "cpu"


def load_whisper(model_name: str, device: str, log: Callable[[str], None]):
    """取得 Whisper 模型；同一個行程內重複使用，一次只留一個模型在顯示卡上。"""
    import stable_whisper

    key = (model_name, _resolve_device(device))
    if key not in _models:
        _models.clear()
        log(f"  . 載入 whisper {model_name}（第一次會下載模型）...")
        _models[key] = stable_whisper.load_model(model_name, device=key[1])
    return _models[key]


def _stable_ts(vocals: Path, lyr: Lyrics, model_name: str, language: str | None,
               device: str, log: Callable[[str], None]) -> list[dict]:
    key = (model_name, device)
    load_whisper(model_name, device, log)

    # vad=True：用 Silero VAD 判斷哪裡在唱，實測能大幅減少字被放到和聲 / 尾奏的情況。
    result = _models[key].align(str(vocals), "\n".join(lyr.lines), language=language,
                                original_split=True, vad=True, verbose=None)
    return _segments(result)


def _segments(result) -> list[dict]:
    lines = []
    for seg in result.segments:
        words = [{"text": w.word, "start": round(w.start, 3), "end": round(w.end, 3)}
                 for w in seg.words]
        if words:
            lines.append({"text": seg.text.strip(), "start": words[0]["start"],
                          "end": words[-1]["end"], "words": words})
    return lines


def repair(lines: list[dict], voiced: np.ndarray) -> list[str]:
    """用發聲遮罩就地修正 lines，回傳修正說明。"""
    notes = _restore_collapsed(lines, voiced)
    notes += _relocate_silent(lines, voiced)
    for li, line in enumerate(lines):
        words = line["words"]
        prev_end = lines[li - 1]["end"] if li else 0.0

        for j in range(1, len(words)):
            a, b = words[j - 1], words[j]
            if b["start"] - a["end"] < _MIN_GAP:
                continue
            run_end = _run_end(voiced, a["end"])
            rest = words[j:]
            leftover = run_end - a["end"]  # 前一個字之後、還沒分配給任何字的人聲
            chars = sum(len(w["text"].strip()) for w in rest)
            # 放錯的跡象：被放在沒有人聲的地方，或那段剩下的人聲剛好容得下這幾個字
            # （後者常見於字被放到下一句的開頭，壓到下一句）。
            misplaced = (_voiced_ratio(voiced, b["start"], rest[-1]["end"]) < 0.3
                         or leftover >= 0.12 * chars)
            if leftover >= 0.2 and b["start"] - run_end >= 0.5 and misplaced:
                old = b["start"]
                _spread(rest, a["end"], run_end)
                moved = "".join(w["text"] for w in rest).strip()
                notes.append(f"第 {li + 1} 句「{moved}」：{old:.2f}s -> {a['end']:.2f}s")

        if li:
            # 句首的字若從上一句延續下來的人聲開始、中間又有停頓，停頓前其實是
            # 上一句最後一個字的長音 -> 長音還給上一句，這個字從停頓後開始。
            prev_last, w0 = lines[li - 1]["words"][-1], words[0]
            continued = _run_start(voiced, w0["start"], prev_last["start"]) <= prev_last["end"] + 0.05
            pause = _first_silence(voiced, w0["start"], w0["end"], 0.3) if continued else None
            if pause:
                old = w0["start"]
                prev_last["end"] = lines[li - 1]["end"] = round(pause[0], 3)
                w0["start"] = round(pause[1], 3)
                prev_end = prev_last["end"]
                notes.append(f"第 {li + 1} 句開頭：{old:.2f}s -> {w0['start']:.2f}s"
                             f"（之前是上一句的長音）")

        onset = _run_start(voiced, words[0]["start"], limit=max(prev_end + 0.05,
                                                              words[0]["start"] - 1.5))
        if onset < words[0]["start"] - 0.1:
            words[0]["start"] = round(onset, 3)

        _fix_zero(words)
        line["start"], line["end"] = words[0]["start"], words[-1]["end"]
    return notes


_COLLAPSED = 0.05      # 平均每個字短於這個秒數（或結束早於開始）視為 Whisper 沒對上
_ROOM = 0.15           # 重新安排時，每個字至少要有這麼多秒的空間
_ABSORB = 10           # 空間不夠時，最多再把前後幾句被擠住的句子一起納入


def _collapsed(line: dict) -> bool:
    chars = len(line["text"].replace(" ", ""))
    return line["end"] - line["start"] < max(0.15, _COLLAPSED * chars)


def _collapsed_runs(lines: list[dict], end_of_audio: float) -> list[tuple[int, int, float, float]]:
    """找出 Whisper 沒對上的連續句子 [(第一句, 最後一句, 範圍起, 範圍訖)]。

    範圍是前一句結束到下一句開始；太窄的話（前後的句子也被擠在一起），
    把相鄰的句子也納入，直到每個字至少有 _ROOM 秒。
    """
    runs = []
    i = 0
    while i < len(lines):
        if not _collapsed(lines[i]):
            i += 1
            continue
        first = last = i
        while last + 1 < len(lines) and _collapsed(lines[last + 1]):
            last += 1
        for _ in range(_ABSORB):
            t0 = lines[first - 1]["end"] if first else 0.0
            t1 = lines[last + 1]["start"] if last + 1 < len(lines) else end_of_audio
            chars = sum(max(len(ln["text"].replace(" ", "")), 1) for ln in lines[first:last + 1])
            if t1 - t0 >= _ROOM * chars:
                break
            if last + 1 < len(lines):
                last += 1
            elif first > 0:
                first -= 1
            else:
                break
        t0 = lines[first - 1]["end"] if first else 0.0
        t1 = lines[last + 1]["start"] if last + 1 < len(lines) else end_of_audio
        runs.append((first, last, t0, t1))
        i = last + 1
    return runs


def _retry_collapsed(lines: list[dict], audio: np.ndarray, model, language: str | None,
                     log: Callable[[str], None]) -> None:
    """Whisper 對不上的句子（stable-ts 警告「N segments failed to align」）會被壓成長度 0、
    擠在同一個時間點。只拿前後對好的句子之間那段音訊，讓 Whisper 針對這幾句再對一次；
    範圍小、上下文單純，通常就對得上。還是對不上的留給 _restore_collapsed 依字數安排。"""
    for first, last, t0, t1 in _collapsed_runs(lines, len(audio) / _SR):
        which = f"第 {first + 1} 句" if first == last else f"第 {first + 1}–{last + 1} 句"
        seg = audio[int(t0 * _SR):int(t1 * _SR)]
        if len(seg) < _SR:
            continue
        texts = [ln["text"] for ln in lines[first:last + 1]]
        try:
            result = model.align(seg, "\n".join(texts), language=language,
                                 original_split=True, vad=True, verbose=None)
        except Exception:
            continue
        again = _segments(result)
        if len(again) != len(texts) or any(_collapsed(ln) for ln in again):
            continue
        _offset(again, t0)
        for target, fresh in zip(lines[first:last + 1], again, strict=False):
            target["words"], target["start"], target["end"] = fresh["words"], fresh["start"], fresh["end"]
        log(f"  . 修正 {which} AI 第一次沒對上，在 {t0:.2f}s–{t1:.2f}s 重新對齊成功")


def _restore_collapsed(lines: list[dict], voiced: np.ndarray) -> list[str]:
    """重新對齊後仍然沒對上的句子：依字數排進範圍內第一段有人聲的地方，
    之後 CTC 會在這個範圍內找出每個字真正的位置。"""
    notes = []
    for first, last, t0, t1 in _collapsed_runs(lines, len(voiced) * _HOP):
        run = lines[first:last + 1]
        start = _next_voiced(voiced, t0, t1) or t0
        chars = sum(max(len(ln["text"].replace(" ", "")), 1) for ln in run)
        if t1 - start < 0.1 * chars:
            continue
        old, t = run[0]["start"], start
        for ln in run:
            span = (t1 - start) * max(len(ln["text"].replace(" ", "")), 1) / chars
            _spread(ln["words"], t, t + span)
            ln["start"], ln["end"] = ln["words"][0]["start"], ln["words"][-1]["end"]
            t += span
        which = f"第 {first + 1} 句" if first == last else f"第 {first + 1}–{last + 1} 句"
        notes.append(f"{which} AI 沒對上（擠在 {old:.2f}s），先依字數排進 {start:.2f}s–{t1:.2f}s 再精修")
    return notes


def _relocate_silent(lines: list[dict], voiced: np.ndarray) -> list[str]:
    """整句（或連續幾句）幾乎沒有人聲時，依字數排進前後句之間第一段有人聲的地方。

    常見於前奏：Whisper 把開頭幾句塞進沒人唱的前奏，下一句的第一個字又把真正的
    演唱段落吃掉。只處理「幾乎完全沒人聲」（< 10%）的句子，避免動到輕聲唱的段落。
    """
    notes = []
    silent = [_voiced_ratio(voiced, ln["start"], ln["end"]) < 0.1 for ln in lines]
    i = 0
    while i < len(lines):
        if not silent[i]:
            i += 1
            continue
        j = i
        while j + 1 < len(lines) and silent[j + 1]:
            j += 1
        run = lines[i:j + 1]
        prev_end = lines[i - 1]["end"] if i else 0.0
        next_start = lines[j + 1]["start"] if j + 1 < len(lines) else len(voiced) * _HOP
        onset = _next_voiced(voiced, prev_end, next_start)
        chars = sum(len(ln["text"]) for ln in run) or 1
        if onset is not None and next_start - onset >= 0.06 * chars:
            old, t = run[0]["start"], onset
            for ln in run:
                span = (next_start - onset) * len(ln["text"]) / chars
                _spread(ln["words"], t, t + span)
                ln["start"], ln["end"] = ln["words"][0]["start"], ln["words"][-1]["end"]
                t += span
            which = f"第 {i + 1} 句" if i == j else f"第 {i + 1}–{j + 1} 句"
            notes.append(f"{which}原本落在沒有人聲處：{old:.2f}s -> {onset:.2f}s")
        i = j + 1
    return notes


_CHUNK_GAP = 1.0       # 句與句間隔小於這個秒數就放進同一段一起對齊
_CHUNK_MAX = 45.0      # 一段最長秒數（太長時在最大的間隔切開，避免顯示卡記憶體不夠）
_RUSH_LIMIT = 2.5      # 首字速度慢於同句其他字的幾倍，就視為吃進了不屬於它的聲音


def refine_ctc(lines: list[dict], texts: list[str], audio: np.ndarray,
               language: str | None, device: str, rubies: list | None = None,
               voiced: np.ndarray | None = None) -> int:
    """用 CTC 重新定出每個音的起訖，回傳成功精修的句數。

    同時用兩種範圍對齊，每句挑比較可信的結果：
    - 整段：緊接的句子（間隔 < 1 秒）一起對齊，每個字要跟前後句的字搶位置。
      擅長慢板、重複的副歌（不會把「我愛你」對到下一句「我想你」的聲音上）。
    - 逐句：每句只在自己的範圍內對齊。擅長句尾長音後緊接下一句的快歌
      （整段對齊有時會把上一句的長音分給下一句的第一個字）。
    兩種的錯誤都表現在「句首的字唱得比同句其他字慢很多」，所以挑首字速度較一致的那個。
    """
    units_of = [reading.split(text, language, rubies[i] if rubies else ())
                for i, text in enumerate(texts)]
    model = _mms_model(device)
    chunked = _ctc_chunked(lines, units_of, audio, device, model)
    single = _ctc_single(lines, units_of, audio, device, model)
    done = 0
    for i, line in enumerate(lines):
        options = [c[i] for c in (chunked, single) if i in c]
        if options:
            line["words"] = min(options, key=_rush)  # 同分時取整段對齊的結果
            done += 1
    _finish(lines, voiced, len(audio) / _SR)
    return done


def _ctc_chunked(lines, units_of, audio, device, model) -> dict[int, list[dict]]:
    out: dict[int, list[dict]] = {}
    for first, last in _chunks(lines):
        members = [i for i in range(first, last + 1) if any(u.roman for u in units_of[i])]
        if not members:
            continue
        prev_end = lines[first - 1]["end"] if first else 0.0
        limit = lines[last + 1]["words"][0]["end"] if last + 1 < len(lines) else len(audio) / _SR
        t0 = max(prev_end, lines[first]["start"] - 1.0)
        t1 = max(min(lines[last]["end"] + 1.5, limit), lines[last]["end"])
        spans = _ctc(audio, t0, t1, [units_of[i] for i in members], device, model)
        if spans:
            out.update(zip(members, spans, strict=False))
    return out


def _ctc_single(lines, units_of, audio, device, model) -> dict[int, list[dict]]:
    """範圍：從上一句結束（最多往前 1 秒）到下一句第一個字結束（最多往後 1.5 秒）。
    下一句第一個字的「結束」通常是準的，錯的是它提早的「開始」，所以用它當上限。"""
    out: dict[int, list[dict]] = {}
    for i, line in enumerate(lines):
        if not any(u.roman for u in units_of[i]):
            continue
        prev_end = lines[i - 1]["end"] if i else 0.0
        limit = lines[i + 1]["words"][0]["end"] if i + 1 < len(lines) else len(audio) / _SR
        t0 = max(prev_end, line["start"] - 1.0)
        t1 = max(min(line["end"] + 1.5, limit), line["end"])
        spans = _ctc(audio, t0, t1, [units_of[i]], device, model)
        if spans:
            out[i] = spans[0]
    return out


def _ctc(audio, t0, t1, groups, device, model) -> list[list[dict]] | None:
    """在 [t0, t1] 對齊多句的單位，回傳每句的字（start = CTC 標出的起點，end 暫為該音結束）。"""
    import torch

    net, tokenizer, aligner = model
    romans = [u.roman for units in groups for u in units if u.roman]
    seg = audio[int(t0 * _SR):int(t1 * _SR)]
    if len(seg) < _SR * 0.1:
        # 範圍是空的或太短（例如句子被放在人聲檔結束之後、或前後句重疊）：
        # 模型無法處理，放棄精修這一段，沿用前一階段的結果。
        return None
    with torch.inference_mode():
        emission, _ = net(torch.from_numpy(seg.copy()).unsqueeze(0).to(device))
    tokens = tokenizer(romans)
    if emission.size(1) < sum(len(t) for t in tokens) * 2:
        return None
    spans = iter(aligner(emission[0], tokens))
    return _words_from_spans(groups, spans, t0, len(seg) / _SR / emission.size(1))


def _words_from_spans(groups, spans, t0: float, spf: float) -> list[list[dict]]:
    """把 CTC 對齊出的每個讀音位置轉成每句的字（start = 起點，end 暫為該音結束）。"""
    result = []
    for units in groups:
        words: list[dict] = []
        prefix = ""
        for u in units:
            if not u.roman:
                # 沒有讀音的空白、標點併入前一個單位（句首則併入下一個）。
                if words:
                    words[-1]["text"] += u.text
                else:
                    prefix += u.text
                continue
            span = next(spans)
            words.append({"text": prefix + u.text, "roman": u.roman,
                          "start": round(t0 + span[0].start * spf, 3),
                          "end": round(t0 + span[-1].end * spf, 3)})
            prefix = ""
        result.append(words)
    return result


CTC_ONLY = {"nan", "yue"}   # Whisper 不支援的語言（台語、粵語）：不經 Whisper，整首直接用 CTC 對齊
_CTC_HOP = 320              # MMS 模型每個輸出 frame 對應的取樣數（16kHz 下 20ms）


def _ctc_lines(audio: np.ndarray, texts: list[str], rubies: list | None, language: str | None,
               device: str, log: Callable[[str], None]) -> list[dict]:
    """整首一次用 CTC 對齊所有歌詞，取代 Whisper 抓句子位置的步驟。
    讀音來自使用者在歌詞裡手動標的台羅 / 粵拼（沒標的字暫用國語拼音），之後照常人聲修正與逐句精修。"""
    units_of = [reading.split(t, language, rubies[i] if rubies else ()) for i, t in enumerate(texts)]
    if any(not units for units in units_of):
        raise ValueError("有句子無法切成讀音單位，請檢查歌詞")
    net, tokenizer, aligner = _mms_model(device)
    log("  . CTC 整首對齊中（台語 / 粵語不經 Whisper）...")
    emission = _emission(audio, device, net)
    tokens = tokenizer([u.roman for units in units_of for u in units if u.roman])
    if emission.size(1) < sum(len(t) for t in tokens):
        raise ValueError("歌詞的讀音比人聲長度還多，無法對齊（歌詞是不是貼錯了？）")
    words_of = _words_from_spans(units_of, iter(aligner(emission[0], tokens)), 0.0, _CTC_HOP / _SR)
    lines, prev = [], 0.0
    for text, words in zip(texts, words_of, strict=False):
        if not words:   # 整句沒有讀音（例如只有符號）：先放在上一句結尾，之後的修正會重新安排
            words = [{"text": text, "start": prev, "end": prev}]
        lines.append({"text": text, "start": words[0]["start"], "end": words[-1]["end"], "words": words})
        prev = lines[-1]["end"]
    return lines


def _emission(audio: np.ndarray, device: str, net):
    """整首歌的 CTC 輸出。一次算整首會用掉太多顯示卡記憶體，所以每 30 秒一段、前後多帶 1 秒上下文，
    只取中間那 30 秒的結果接起來（frame 對齊取樣位置，不會累積誤差）。"""
    import torch

    win, pad = 30 * _SR, _SR
    parts = []
    with torch.inference_mode():
        for s in range(0, len(audio), win):
            e = min(s + win, len(audio))
            a, b = max(0, s - pad), min(len(audio), e + pad)
            em, _ = net(torch.from_numpy(audio[a:b].copy()).unsqueeze(0).to(device))
            f0 = (s - a) // _CTC_HOP
            parts.append(em[0, f0:f0 + (e - s) // _CTC_HOP])
    return torch.cat(parts).unsqueeze(0)


def _rush(words: list[dict]) -> float:
    """句首的字比同句其他字慢多少（以每個讀音字母花的時間比較），正常為 0。"""
    if len(words) < 3:
        return 0.0
    rates = [(b["start"] - a["start"]) / max(len(a["roman"]), 1) for a, b in zip(words, words[1:], strict=False)]
    typical = float(np.median(rates[1:]))
    return max(0.0, rates[0] / typical - _RUSH_LIMIT) if typical > 0 else 0.0


def _finish(lines: list[dict], voiced: np.ndarray | None, audio_end: float) -> None:
    """CTC 只標出每個音「出現」的片刻；KTV 要讓填色一路延續到下一個音開始。
    間隔短（< 0.6 秒）或間隔中人聲連續（長音）時延續，真正的停頓才中斷。
    句尾唱到人聲結束（長音）為止，但不超過下一句的開頭。"""
    for i, line in enumerate(lines):
        words = line["words"]
        for w in words:
            w.pop("roman", None)
        if i:
            # 兩種對齊方式混用時，確保這一句不會早於上一句最後一個字的開頭。
            floor = lines[i - 1]["words"][-1]["start"] + 0.05
            for w in words:
                if w["start"] < floor:
                    w["start"] = round(floor, 3)
                    w["end"] = max(w["end"], w["start"])
        for a, b in zip(words, words[1:], strict=False):
            if b["start"] - a["end"] < 0.6 or _held(voiced, a["end"], b["start"]):
                a["end"] = b["start"]
        nxt = lines[i + 1]["words"][0]["start"] if i + 1 < len(lines) else audio_end
        tail = words[-1]
        held_to = _run_end(voiced, tail["end"]) if voiced is not None else tail["end"]
        end = max(tail["end"], held_to, line["end"] if line["end"] <= nxt else tail["end"])
        tail["end"] = round(max(tail["start"] + 0.05, min(end, nxt)), 3)
        line["start"], line["end"] = words[0]["start"], words[-1]["end"]


def _chunks(lines: list[dict]) -> list[tuple[int, int]]:
    """把緊接的句子分段：[(第一句, 最後一句)]，太長的段落在最大的間隔處切開。"""
    groups, start = [], 0
    for i in range(1, len(lines) + 1):
        if i == len(lines) or lines[i]["start"] - lines[i - 1]["end"] >= _CHUNK_GAP:
            groups.append((start, i - 1))
            start = i
    result = []
    while groups:
        first, last = groups.pop(0)
        if lines[last]["end"] - lines[first]["start"] <= _CHUNK_MAX or first == last:
            result.append((first, last))
            continue
        gaps = [(lines[i + 1]["start"] - lines[i]["end"], i) for i in range(first, last)]
        _, cut = max(gaps)
        groups[:0] = [(first, cut), (cut + 1, last)]
    return result


def _held(voiced: np.ndarray | None, t0: float, t1: float) -> bool:
    """t0 到 t1 之間人聲幾乎沒斷（長音）。"""
    return voiced is not None and t1 > t0 and _voiced_ratio(voiced, t0, t1) > 0.8


def _next_voiced(mask: np.ndarray, t0: float, t1: float) -> float | None:
    """t0 之後、t1 之前第一個有人聲的時間。"""
    i, stop = _idx(mask, t0), _idx(mask, t1)
    while i < stop and not mask[i]:
        i += 1
    return i * _HOP if i < stop else None


def _mms_model(device: str):
    if device not in _mms:
        import torchaudio
        bundle = torchaudio.pipelines.MMS_FA
        _mms.clear()
        _mms[device] = (bundle.get_model().to(device).eval(),
                        bundle.get_tokenizer(), bundle.get_aligner())
    return _mms[device]


def _load_audio(vocals: Path) -> np.ndarray:
    """人聲軌轉成 16kHz 單聲道 float32。"""
    raw = subprocess.run(
        [FFMPEG, "-v", "error", "-i", str(vocals), "-vn", "-ac", "1", "-ar", str(_SR),
         "-f", "s16le", "-"], capture_output=True, check=True).stdout
    return np.frombuffer(raw, np.int16).astype(np.float32) / 32768


def _voiced_mask(audio: np.ndarray) -> np.ndarray:
    """人聲軌每 20ms 是否有聲音；會補平 0.25 秒以內的短暫停頓（換氣等）。"""
    hop = int(_SR * _HOP)
    frames = audio[: len(audio) // hop * hop].reshape(-1, hop)
    db = 20 * np.log10(np.sqrt((frames ** 2).mean(axis=1)) + 1e-9)
    # 門檻相對於整首歌的大聲段落，不受錄音音量影響。
    threshold = max(np.percentile(db, 90) - 28, -50)
    mask = db > threshold

    max_hole = int(0.25 / _HOP)
    i = 0
    while i < len(mask):
        if not mask[i]:
            j = i
            while j < len(mask) and not mask[j]:
                j += 1
            if 0 < i and j < len(mask) and j - i <= max_hole:
                mask[i:j] = True
            i = j
        else:
            i += 1
    return mask


def _idx(mask: np.ndarray, t: float) -> int:
    return min(max(int(t / _HOP), 0), len(mask) - 1)


def _run_end(mask: np.ndarray, t: float) -> float:
    """從 t 開始的連續發聲段在何時結束；t 當下沒有聲音就回傳 t。"""
    i = _idx(mask, t)
    if not mask[i]:
        return t
    while i < len(mask) and mask[i]:
        i += 1
    return i * _HOP


def _run_start(mask: np.ndarray, t: float, limit: float) -> float:
    """包含 t 的連續發聲段從何時開始，不早於 limit。"""
    i = _idx(mask, t)
    if not mask[i]:
        return t
    stop = _idx(mask, limit)
    while i > stop and mask[i - 1]:
        i -= 1
    return max(i * _HOP, limit)


def _first_silence(mask: np.ndarray, t0: float, t1: float,
                   min_len: float) -> tuple[float, float] | None:
    """[t0, t1] 內第一段至少 min_len 秒、且之後還有人聲的靜音 (開始, 結束)。"""
    i, stop = _idx(mask, t0), _idx(mask, t1)
    while i < stop:
        if mask[i]:
            i += 1
            continue
        j = i
        while j < len(mask) and not mask[j]:
            j += 1
        # 靜音要在 t1 之前結束；一路靜到 t1 之後的話，代表這段聲音根本不在範圍內。
        if (j - i) * _HOP >= min_len and j < len(mask) and j <= stop:
            return i * _HOP, j * _HOP
        i = j
    return None


def _voiced_ratio(mask: np.ndarray, t0: float, t1: float) -> float:
    seg = mask[_idx(mask, t0):_idx(mask, t1) + 1]
    return float(seg.mean()) if len(seg) else 0.0


def _spread(words: list[dict], t0: float, t1: float) -> None:
    """依字數比例把 words 重新排進 [t0, t1]。"""
    weights = [max(len(w["text"].strip()), 1) for w in words]
    total = sum(weights)
    t = t0
    for w, weight in zip(words, weights, strict=False):
        w["start"] = round(t, 3)
        t += (t1 - t0) * weight / total
        w["end"] = round(t, 3)


def _fix_zero(words: list[dict]) -> None:
    """連續的零長度字和後一個字（最後一個則和前一個字）平分時間。"""
    i = 0
    while i < len(words):
        if words[i]["end"] - words[i]["start"] >= 0.05:
            i += 1
            continue
        k = i
        while k + 1 < len(words) and words[k + 1]["end"] - words[k + 1]["start"] < 0.05:
            k += 1
        if k + 1 < len(words):
            group = words[i:k + 2]
        elif i > 0:
            group = words[i - 1:k + 1]
        else:
            break
        t0, t1 = group[0]["start"], group[-1]["end"]
        if t1 - t0 >= 0.05 * len(group):
            _spread(group, t0, t1)
        i = k + 2
