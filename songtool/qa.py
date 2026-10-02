"""對時品質檢查：標出可能不準的句子。全部在本機執行，不需要網路。

兩層：
1. 規則（只看對時結果與人聲音量）：
   - 句首的字唱得比同句其他字慢很多：通常是吃進了上一句的長音或間奏
   - 好幾個字連續一閃而過：通常是整句被擠到錯的位置
   - 整句大多落在沒有人聲的地方
   一般的長音（字與字之間人聲沒斷）不算問題。
2. 獨立聽寫：用同一個 Whisper 模型、不看歌詞，逐段聽寫人聲，把聽寫的時間當參考答案，
   以音節（中文一字一拼音、日文一音拍、英文一個單字）比對每個字的「起點」。
   聽寫本身在長音附近約有 ±1 秒誤差，所以：
   - 時間差 ≥ 1.5 秒：可能不準（不論規則怎麼判斷）
   - 規則有疑慮、但聽寫吻合（差 < 1 秒）：沒問題，不標
   - 規則有疑慮、聽寫也無法確認：待確認
結果寫在 output/karaoke/<名稱>/qa.json，對時結果改變後自動失效。
"""
from __future__ import annotations

import difflib
import hashlib
import json
import statistics
from pathlib import Path
from typing import Callable

from . import align, reading

VERSION = 4
QA_FILE = "qa.json"

_WRONG = 1.5       # 與聽寫差這麼多秒以上：可能不準
_AGREE = 1.0       # 與聽寫差這麼多秒以內：視為吻合（聽寫本身在長音附近約有 ±1 秒誤差）
_MIN_MATCH = 0.5   # 至少要對上這個比例的音節，聽寫的時間才算數
_NEAR = 5.0        # 只在這句前後幾秒內的聽寫裡找對應（避免配到遠處重複的副歌）
_CLUSTER = 1.0     # 時間差在這個範圍內的配對算同一群

Token = tuple[str, float]   # (音節的羅馬字, 開始唱的時間)


def key_of(lines: list[dict]) -> dict:
    """qa.json 對應的對時結果；對時一變就要重新檢查。"""
    digest = hashlib.sha1(json.dumps(lines, ensure_ascii=False, sort_keys=True).encode()).hexdigest()
    return {"alignment_sha1": digest, "version": VERSION}


def check(lines: list[dict], texts: list[str], rubies: list, vocals: Path, language: str | None,
          *, model_name: str, device: str, log: Callable[[str], None]) -> dict:
    """回傳 {"key", "lines": [{index, text, start, end, status, reasons, offset, match}]}。"""
    audio = align._load_audio(vocals)
    voiced = align._voiced_mask(audio)
    # 台語、粵語 Whisper 聽寫不出正確的字，只做不靠聽寫的規則檢查。
    transcribe = language not in align.CTC_ONLY

    # 分段聽寫：Whisper 聽寫整首長音檔時，時間戳會越來越漂移（實測差到 4 秒），
    # 所以沿用對時的分段，每段前後多留 2 秒，只在那段的聽寫結果裡找對應。
    heard_of: dict[int, list[Token]] = {}
    if transcribe:
        log("  . 對時檢查：逐段獨立聽寫人聲（不看歌詞）...")
        model = align.load_whisper(model_name, device, log)
    else:
        log("  . 對時檢查：台語 / 粵語只做規則檢查（不做聽寫比對）")
    total = len(audio) / align._SR
    for first, last in (align._chunks(lines) if transcribe else []):
        t0 = max(0.0, lines[first]["start"] - 2.0)
        t1 = min(total, lines[last]["end"] + 2.0)
        clip = audio[int(t0 * align._SR):int(t1 * align._SR)]
        result = model.transcribe(clip, language=language, word_timestamps=True,
                                  condition_on_previous_text=False, verbose=None)
        heard = [(syl, t0 + t) for syl, t in _heard_tokens(result, language)]
        for i in range(first, last + 1):
            heard_of[i] = heard

    checks = []
    for i, line in enumerate(lines):
        text = texts[i] if i < len(texts) else line["text"]
        ruby = rubies[i] if rubies and i < len(rubies) else ()
        ours, word_of, per_word = _lyric_tokens(line["words"], text, ruby, language)
        rules = _rule_reasons(line, per_word, voiced)
        offset, match, pairs = _compare(ours, heard_of.get(i, []), line["start"], line["end"], language)
        # 句首吃進長音時只有第一個字錯，整句的多數仍是對的，所以首字要單獨比對。
        first = [d for k, d in pairs if word_of[k] == 0]
        first_off = statistics.median(first) if first else None
        agrees = match >= _MIN_MATCH and offset is not None and abs(offset) < _AGREE
        status, reasons = "ok", []
        if match >= _MIN_MATCH and offset is not None and abs(offset) >= _WRONG:
            status = "wrong"
            reasons = [t for _, t in rules] + [f"比獨立聽寫{'晚' if offset > 0 else '早'} {abs(offset):.1f} 秒"]
        elif any(kind == "rush" for kind, _ in rules) and first_off is not None and abs(first_off) >= _AGREE:
            status = "wrong"
            reasons = [t for _, t in rules] + [f"句首比獨立聽寫{'晚' if first_off > 0 else '早'} {abs(first_off):.1f} 秒"]
        elif rules:
            # 聽寫確認時間是對的就不標；句首過長要首字本身也吻合才算數。
            remaining = [(k, t) for k, t in rules if not agrees or (k == "rush" and first_off is None)]
            if remaining:
                status = "suspect"
                reasons = [t for _, t in remaining]
                if not transcribe:
                    pass
                elif match < _MIN_MATCH or offset is None:
                    reasons.append("聽寫對不上這句歌詞，無法確認")
                elif not agrees:
                    reasons.append(f"與獨立聽寫差 {abs(offset):.1f} 秒")
                else:
                    reasons.append("聽寫聽不出句首這個字，無法確認")
        checks.append({"index": i, "text": "".join(w["text"] for w in line["words"]) or line["text"], "start": line["start"], "end": line["end"],
                       "status": status, "reasons": reasons,
                       "offset": None if offset is None else round(offset, 2), "match": round(match, 2)})
    return {"key": key_of(lines), "lines": checks}


def summary(doc: dict | None) -> dict:
    if not doc:
        return {"checked": False, "wrong": 0, "suspect": 0}
    lines = doc.get("lines", [])
    return {"checked": True,
            "wrong": sum(1 for c in lines if c["status"] == "wrong"),
            "suspect": sum(1 for c in lines if c["status"] == "suspect")}


# ---- 規則 --------------------------------------------------------------------

def _rule_reasons(line: dict, per_word: list[int], voiced) -> list[tuple[str, str]]:
    """回傳 [(種類, 說明)]；per_word：每個字有幾個讀音字母（估計該唱多久用）。"""
    words = line["words"]
    reasons = []
    starts = [w["start"] for w in words]
    if len(words) >= 3:
        rates = [(b - a) / max(n, 1) for a, b, n in zip(starts, starts[1:], per_word)]
        typical = statistics.median(rates[1:])
        first = starts[1] - starts[0]
        if typical > 0 and rates[0] > 2.5 * typical and first > 0.8:
            reasons.append(("rush", f"句首「{words[0]['text'].strip()}」唱了 {first:.1f} 秒，比同句其他字慢很多"))
    run = best = 0
    for a, b in zip(starts, starts[1:]):
        run = run + 1 if b - a < 0.08 else 0
        best = max(best, run)
    if best >= 2:
        reasons.append(("flash", f"有 {best + 1} 個字連續一閃而過"))
    if align._voiced_ratio(voiced, line["start"], line["end"]) < 0.5:
        reasons.append(("silent", "大部分落在沒有人聲的地方"))
    return reasons


# ---- 聽寫比對 ----------------------------------------------------------------

def _lyric_tokens(words: list[dict], text: str, rubies,
                  language) -> tuple[list[Token], list[int], list[int]]:
    """歌詞這一句的音節與開始時間、每個音節屬於第幾個字、每個字的讀音字母數。

    優先用整句的讀音（含手動指定的讀音），依字元位置對回每個字；對不上時逐字轉換。
    同一個字裡有好幾個音節時，平均分在這個字到下一個字之間。
    """
    per_unit: list[list[str]] = [[] for _ in words]
    if "".join(w["text"] for w in words) == text:
        bounds, pos = [], 0
        for w in words:
            bounds.append((pos, pos + len(w["text"])))
            pos += len(w["text"])
        pos = 0
        for u in reading.split(text, language, rubies):
            k = next((i for i, (a, b) in enumerate(bounds) if a <= pos < b), None)
            if k is not None and u.roman:
                per_unit[k].append(u.roman)
            pos += len(u.text)
    else:
        per_unit = [[u.roman for u in reading.split(w["text"], language) if u.roman] for w in words]

    tokens: list[Token] = []
    word_of: list[int] = []
    for k, (w, syllables) in enumerate(zip(words, per_unit)):
        nxt = words[k + 1]["start"] if k + 1 < len(words) else w["end"]
        span = max(0.0, nxt - w["start"])
        tokens += [(syl, w["start"] + span * j / len(syllables)) for j, syl in enumerate(syllables)]
        word_of += [k] * len(syllables)
    return tokens, word_of, [sum(len(s) for s in syllables) for syllables in per_unit]


def _heard_tokens(result, language) -> list[Token]:
    tokens: list[Token] = []
    for seg in result.segments:
        for w in seg.words:
            syllables = [u.roman for u in reading.split(w.word.strip(), language) if u.roman]
            span = max(0.0, w.end - w.start)
            tokens += [(syl, w.start + span * j / len(syllables)) for j, syl in enumerate(syllables)]
    return tokens


def _compare(ours: list[Token], heard: list[Token], start: float, end: float,
             language: str | None) -> tuple[float | None, float, list[tuple[int, float]]]:
    """以音節對應，回傳（我們 - 聽寫 的起點時間差, 對上的比例, [(我們第幾個音節, 時間差)]）。

    相似或重複的歌詞（副歌、「明日も 君の声は…」對「今日も 君の夢は…」）很容易配錯，所以：
    - 只在這句前後 5 秒內的聽寫裡找對應
    - 只採用連續好幾個音節相同的對應（日文音拍很常見，要 3 個；其他 2 個）
    - 時間差取「最大的一群一致結果」：配對依時間差分群（1 秒內同一群），零星的湊巧配對自成小群被忽略
    """
    heard = [h for h in heard if start - _NEAR <= h[1] <= end + _NEAR]
    if not ours or not heard:
        return None, 0.0, []
    a = [s for s, _ in ours]
    b = [s for s, _ in heard]
    min_block = 1 if len(a) < 3 else (3 if language == "ja" and len(a) >= 6 else 2)
    pairs = []
    for block in difflib.SequenceMatcher(None, a, b, autojunk=False).get_matching_blocks():
        if block.size >= min_block:
            pairs += [(block.a + k, ours[block.a + k][1] - heard[block.b + k][1]) for k in range(block.size)]
    if not pairs:
        return None, 0.0, []
    diffs = sorted(d for _, d in pairs)
    best: list[float] = []
    lo = 0
    for hi in range(len(diffs)):
        while diffs[hi] - diffs[lo] > _CLUSTER:
            lo += 1
        if hi - lo + 1 > len(best):
            best = diffs[lo:hi + 1]
    return statistics.median(best), len(best) / len(ours), pairs
