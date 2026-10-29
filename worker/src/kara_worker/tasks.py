"""各種任務的執行（docs/worker-protocol.md「任務種類」）。

    separate     去人聲（Demucs）
    align        整首對時（Whisper + CTC）
    align_from   從某一句開始重新對時
    align_line   只重對一句
    qa           對時檢查（Whisper 獨立聽寫）
    reading      日文的自動讀音（假名）
    render       產生 ASS 字幕並燒進影片
"""
from __future__ import annotations

import hashlib
import json
import shutil
import subprocess
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path

from . import align, qa, reading, subtitles
from .lyrics import Lyrics
from .media import FFMPEG, FFPROBE, Cancelled, run_cancellable
from .separate import run_demucs

Log = Callable[[str], None]


@dataclass
class Context:
    """一件任務的執行環境。"""
    inputs: dict[str, Path]               # 輸入檔（已經下載到本機快取）
    out_dir: Path                         # 要上傳的檔案放這裡
    log: Log
    progress: Callable[[float], None]
    should_stop: Callable[[], bool]
    font: Callable[[dict], Path]          # 取得字型檔（缺的話向 NAS 下載）
    device: str = "auto"
    files: list[str] = field(default_factory=list)   # 要上傳的檔名


class TaskError(Exception):
    """給使用者看的錯誤（retryable：換一台或稍後再試可能會好，例如顯示卡記憶體不足）。"""

    def __init__(self, message: str, retryable: bool = False):
        super().__init__(message)
        self.retryable = retryable


def run(kind: str, params: dict, ctx: Context) -> dict:
    """執行一件任務，回傳結果（JSON）；要上傳的檔案放在 ctx.out_dir、檔名記在 ctx.files。"""
    if kind == "reading":
        return _reading(params)
    if kind == "render":
        return _render(params, ctx)
    if kind == "separate":
        return _separate(params, ctx)
    audio = ctx.inputs["audio"]
    model, language = params.get("model", "large-v3"), params.get("language")
    if kind == "align":
        return {"lines": align.align(audio, Lyrics.from_params(params), model_name=model, language=language,
                                     device=ctx.device, log=ctx.log)}
    if kind == "align_from":
        return {"lines": align.align_from(audio, Lyrics.from_params(params), int(params["first"]),
                                          float(params["anchor"]), model_name=model, language=language,
                                          device=ctx.device, log=ctx.log)}
    if kind == "align_line":
        one = Lyrics.from_params({"texts": [params["text"]], "rubies": [params.get("rubies") or []]})
        return {"line": align.align_line(audio, one.lines[0], one.rubies[0], float(params["t0"]), params.get("t1"),
                                         language=language, device=ctx.device)}
    if kind == "qa":
        lyr = Lyrics.from_params(params)
        return {"doc": qa.check(params["lines"], lyr.lines, lyr.rubies, audio, language, model_name=model,
                                device=ctx.device, log=ctx.log)}
    raise TaskError(f"不支援的任務：{kind}")


# ---- 去人聲 ------------------------------------------------------------------------

def _separate(params: dict, ctx: Context) -> dict:
    stems = run_demucs(ctx.inputs["audio"], ctx.out_dir / "demucs", model=params.get("model", "htdemucs"),
                       stems=int(params.get("stems", 2)), device=ctx.device, log=ctx.log, progress=ctx.progress,
                       should_stop=ctx.should_stop)
    for f in stems:
        f.replace(ctx.out_dir / f.name)
    shutil.rmtree(ctx.out_dir / "demucs", ignore_errors=True)
    missing = {"vocals.wav", "no_vocals.wav"} - {f.name for f in stems}
    if missing:
        raise TaskError(f"去人聲沒有產生 {', '.join(sorted(missing))}")
    ctx.files += ["vocals.wav", "no_vocals.wav"]
    return {}


# ---- 讀音 ------------------------------------------------------------------------

def _reading(params: dict) -> dict:
    """每句的自動讀音（只看句子文字，不看手動讀音；目前只有日文有自動讀音）。"""
    language = params.get("language")
    lines = []
    for text in params.get("texts", []):
        spans = reading.auto_furigana(text) if language == "ja" and text else []
        lines.append([{"start": s, "end": e, "ruby": r} for s, e, r in spans])
    return {"lines": lines}


# ---- 字幕與燒錄 --------------------------------------------------------------------

def subtitle_ratio(scale: float) -> float:
    """字幕字高佔畫面高度的比例：預設樣式 × 字幕大小（100% 時和預設完全相同）。"""
    base = subtitles.Style().size_ratio
    return base if scale == 1 else round(base * scale, 5)


@dataclass
class Video:
    width: int
    height: int
    bitrate: int | None   # 影像軌的位元率（bps）；讀不到時用整個檔案的位元率，再讀不到為 None


def _probe_video(path: Path) -> Video | None:
    """第一條真正的影像軌（內嵌封面圖不算）；純音訊回傳 None。"""
    out = subprocess.run([FFPROBE, "-v", "error", "-show_entries",
                          "format=bit_rate:stream=codec_type,width,height,bit_rate:stream_disposition=attached_pic",
                          "-of", "json", str(path)],
                         capture_output=True, text=True, encoding="utf-8", errors="replace")
    if out.returncode != 0:
        raise TaskError(f"讀不到影片資訊：{out.stderr.strip()[-300:]}")
    info = json.loads(out.stdout or "{}")
    for s in info.get("streams", []):
        if s.get("codec_type") == "video" and s.get("disposition", {}).get("attached_pic") != 1:
            rate = s.get("bit_rate") or info.get("format", {}).get("bit_rate")   # webm、mkv 的影像軌常常沒有位元率
            return Video(int(s["width"]), int(s["height"]), int(rate) if rate and str(rate).isdigit() else None)
    return None


# 燒出來的畫面一律 16:9：比較窄（正方形、直式）的影片左右補黑邊，比較寬的上下補，字幕才排得開。
# 不放大原畫面；只有補邊後超過 4K（NVENC 的 H.264 寬度上限是 4096）才整個縮小。
MAX_CANVAS = (3840, 2160)


def canvas(video: Video | None) -> tuple[int, int]:
    """燒錄畫面的大小（寬, 高）：16:9、偶數。純音訊用 1920x1080 的黑底。"""
    if not video:
        return 1920, 1080
    w, h = video.width, video.height
    if w * 9 < h * 16:
        w = h * 16 / 9
    else:
        h = w * 9 / 16
    k = min(1.0, MAX_CANVAS[0] / w)
    w, h = round(w * k), round(h * k)
    return w + w % 2, h + h % 2


# 音量：每首的伴奏都調到同樣的響度（EBU R128 整合響度），換歌時伴奏一樣大聲。
# 整首套用同一個增益（不壓縮動態）；調大後超過上限的瞬間才用限幅器壓住，避免破音。
# 原曲版（target original）套用同一首伴奏算出的增益，伴奏的音量和伴唱帶一致。
TARGET_LUFS = -16.0
PEAK_LIMIT_DB = -1.0
MAX_GAIN_DB = 20.0   # 幾乎無聲的音軌不要無限放大


@dataclass
class Loudness:
    lufs: float | None   # 整合響度；無聲時為 None
    peak_db: float       # 最高的 true peak（dBTP）


def measure_loudness(path: Path, should_stop: Callable[[], bool] | None = None) -> Loudness:
    """量第一條音軌的響度（ffmpeg loudnorm 的分析結果）。"""
    code, err = run_cancellable([FFMPEG, "-hide_banner", "-nostats", "-i", str(path), "-map", "0:a:0",
                                 "-af", f"loudnorm=I={TARGET_LUFS}:TP={PEAK_LIMIT_DB}:print_format=json", "-f", "null", "-"],
                                should_stop)
    start = err.rfind("{")
    if code != 0 or start < 0:
        raise TaskError(f"量不到音量：{err.strip()[-300:]}")
    info = json.loads(err[start:err.index("}", start) + 1])
    lufs = float(info["input_i"])
    return Loudness(None if lufs == float("-inf") or lufs < -70 else lufs, float(info["input_tp"]))


def gain_for(loud: Loudness) -> float:
    """讓響度到 TARGET_LUFS 要調幾 dB（取到 0.1）。"""
    if loud.lufs is None:
        return 0.0
    return round(max(-MAX_GAIN_DB, min(MAX_GAIN_DB, TARGET_LUFS - loud.lufs)), 1)


def audio_filter(gain_db: float, peak_db: float | None) -> str:
    """套用增益的濾鏡；調完會超過上限時才加限幅器（peak_db 未知時調大就加）。
    level=0：不要自動再調整音量；latency=1：補回限幅器的延遲，聲音和畫面才對得上。"""
    f = f"volume={gain_db}dB"
    if gain_db > 0 and (peak_db is None or peak_db + gain_db > PEAK_LIMIT_DB):
        f += f",alimiter=limit={10 ** (PEAK_LIMIT_DB / 20):.4f}:level=0:latency=1"
    return f


def _attach_furigana(lines: list[dict], lyr: Lyrics) -> None:
    """替每句加上假名位置（自動讀音 + 手動讀音）。
    只處理「逐字串起來剛好等於歌詞原文」的句子，對不上的不加假名，避免標錯字。"""
    if len(lines) != len(lyr.lines):
        return
    for line, text, rs in zip(lines, lyr.lines, lyr.rubies, strict=False):
        if "".join(w["text"] for w in line["words"]) != text:
            continue
        segments = reading.furigana(text, "ja", rs)
        line["text"] = text
        line["rubies"] = [(s["start"], s["end"], s["ruby"]) for s in segments if s["ruby"]]


def build_ass(params: dict, size: tuple[int, int], font_file: Path) -> str:
    """產生 ASS 字幕。"""
    font = params["font"]
    style = subtitles.Style(font=font["family"], size_ratio=subtitle_ratio(float(params.get("scale", 1))))
    lines = [dict(line) for line in params["lines"]]
    for line, singer in zip(lines, params.get("singers") or [], strict=False):
        line["singer"] = singer
    if params.get("language") == "ja":
        _attach_furigana(lines, Lyrics.from_params(params))
    card = params.get("title_card") or [None, None]
    return subtitles.build(lines, *size, style, font_file=font_file, font_index=int(font.get("index", 0)),
                           title=card[0], artist=card[1] or None, note=card[2] if len(card) > 2 else None,
                           translations=params.get("translations") or [])


def _render(params: dict, ctx: Context) -> dict:
    media = ctx.inputs["media"]
    video = _probe_video(media)
    width, height = canvas(video)
    font_file = ctx.font(params["font"])
    ass_path = ctx.out_dir / "karaoke.ass"
    if "ass" in ctx.inputs:
        ctx.log("  . 使用手動修改過的字幕")
        shutil.copyfile(ctx.inputs["ass"], ass_path)
    else:
        ctx.log("  . 產生字幕")
        # utf-8-sig：Aegisub 等軟體要 BOM 才認得是 UTF-8
        ass_path.write_text(build_ass(params, (width, height), font_file), encoding="utf-8-sig")
        ctx.files.append("karaoke.ass")
    if ctx.should_stop():
        raise Cancelled()
    ctx.log("  . 量伴奏的音量")
    # 伴唱帶的 media 就是伴奏；原曲版另外給伴奏（instrumental）當基準
    ref = ctx.inputs.get("instrumental", media)
    loud = measure_loudness(ref, ctx.should_stop)
    gain = gain_for(loud)
    ctx.log(f"  . 響度 {'無聲' if loud.lufs is None else f'{loud.lufs:.1f} LUFS'}，調整 {gain:+.1f} dB → {TARGET_LUFS:g} LUFS")
    _burn(media, ass_path, ctx.out_dir / "video.mp4", (width, height), video, font_file, params, ctx,
          audio_filter(gain, loud.peak_db if ref == media else None))   # 原曲有人聲，最高點和伴奏不同
    ctx.files.append("video.mp4")
    return {"width": width, "height": height, "ass_sha256": hashlib.sha256(ass_path.read_bytes()).hexdigest(),
            "gain_db": gain, "loudness": loud.lufs}


# 品質目標 + 位元率上限：燒字幕一定要重新編碼，只用固定品質的話，編碼器會連來源的壓縮雜訊一起保留，
# 位元率常常比來源高好幾倍、畫質卻不會更好。上限是來源影像的 1.5 倍（至少 1.5 Mbps）。
MIN_CAP = 1_500_000
CAP_RATIO = 1.5
AUDIO_BITRATE = "192k"   # 來源（YouTube）通常是 128k，再高也沒有意義


def bitrate_cap(video: Video | None) -> int:
    """燒錄的位元率上限（bps）。純音訊（黑底）或讀不到來源位元率時用最低值。"""
    if not video or not video.bitrate:
        return MIN_CAP
    return max(MIN_CAP, round(video.bitrate * CAP_RATIO))


def encoder_args(name: str, cap: int) -> list[str] | None:
    limit = ["-maxrate", str(cap), "-bufsize", str(cap * 2)]
    return {
        "h264_nvenc": ["-c:v", "h264_nvenc", "-preset", "p5", "-rc", "vbr", "-cq", "26", "-b:v", "0", *limit],
        "libx264": ["-c:v", "libx264", "-preset", "medium", "-crf", "21", *limit],
    }.get(name)


def _burn(src: Path, ass_path: Path, dest: Path, size: tuple[int, int], video: Video | None, font_file: Path,
          params: dict, ctx: Context, af: str) -> None:
    """燒錄字幕。字型只用 NAS 給的那個檔（fontsdir 只放它），多台 worker 燒出來才一致。"""
    work = ctx.out_dir / "burn"
    fonts = work / "fonts"
    fonts.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(font_file, fonts / font_file.name)
    # subtitles 濾鏡對路徑中的冒號、括號等字元很敏感：用工作資料夾裡的 ASCII 相對路徑
    shutil.copyfile(ass_path, work / "sub.ass")
    w, h = size
    fit = ""
    if video:
        inputs, maps = ["-i", str(src)], ["-map", "0:v:0", "-map", "0:a:0"]
        if (video.width, video.height) != size:   # 原畫面置中、不放大，四周補黑邊成 16:9
            fit = (f"scale={w}:{h}:force_original_aspect_ratio=decrease:force_divisible_by=2,"
                   f"pad={w}:{h}:(ow-iw)/2:(oh-ih)/2:black,setsar=1,")
    else:
        # 純音訊來源：用黑底當畫面
        inputs = ["-f", "lavfi", "-i", f"color=c=black:s={w}x{h}:r=30", "-i", str(src)]
        maps = ["-map", "0:v:0", "-map", "1:a:0", "-shortest"]
    base = [FFMPEG, "-y", "-v", "error", *inputs, *maps, "-vf", f"{fit}subtitles=sub.ass:fontsdir=fonts"]
    tail = ["-pix_fmt", "yuv420p", "-af", af, "-c:a", "aac", "-b:a", AUDIO_BITRATE, "-movflags", "+faststart", "out.mp4"]
    cap = bitrate_cap(video)
    err = ""
    for name in params.get("encode", {}).get("prefer") or ["h264_nvenc", "libx264"]:
        args = encoder_args(name, cap)
        if args is None:
            continue
        ctx.log(f"  . 燒錄字幕（{name}，最高 {cap / 1e6:.1f} Mbps）...")
        code, err = run_cancellable([*base, *args, *tail], ctx.should_stop, cwd=work)
        if code == 0:
            (work / "out.mp4").replace(dest)
            shutil.rmtree(work, ignore_errors=True)
            return
        ctx.log(f"  . {name} 無法使用，改用下一個編碼器")
    raise TaskError(f"燒錄失敗：{err.strip()[-500:]}")
