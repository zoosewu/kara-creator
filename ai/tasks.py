"""AI worker 的各種任務（docs/v2/worker-protocol.md「任務種類」）。

去人聲、對時、檢查直接用 v1 的 songtool.ai.execute（同一套演算法，結果和 v1 相同）；
讀音、字幕與燒錄是 v2 從歌曲伺服器搬過來的（v1 的 karaoke._make 第 2、3 步）。
"""
from __future__ import annotations

import hashlib
import json
import shutil
import subprocess
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

from songtool import ass, reading
from songtool.ai import execute
from songtool.config import FFMPEG, FFPROBE
from songtool.lyrics import Ruby
from songtool.media import Cancelled, run_cancellable

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
    if kind == "reading":
        return _reading(params)
    if kind == "render":
        return _render(params, ctx)
    audio = ctx.inputs["audio"]
    v1 = dict(params, device=ctx.device)
    if kind == "align_line":
        # v2 一次一句（text / rubies）；v1 的參數是清單
        v1["texts"], v1["rubies"] = [params["text"]], [params.get("rubies") or []]
    result = execute(kind, v1, {"audio": audio}, ctx.out_dir, log=ctx.log, progress=ctx.progress,
                     should_stop=ctx.should_stop)
    if kind == "separate":
        missing = {"vocals.wav", "no_vocals.wav"} - set(result.get("files", []))
        if missing:
            raise TaskError(f"去人聲沒有產生 {', '.join(sorted(missing))}")
        ctx.files += ["vocals.wav", "no_vocals.wav"]
        return {}
    return result


# ---- 讀音 ------------------------------------------------------------------------

def _reading(params: dict) -> dict:
    """每句的自動讀音（只看句子文字，不看手動讀音；目前只有日文有自動讀音）。"""
    language = params.get("language")
    lines = []
    for text in params.get("texts", []):
        spans = reading._auto_furigana(text) if language == "ja" and text else []
        lines.append([{"start": s, "end": e, "ruby": r} for s, e, r in spans])
    return {"lines": lines}


# ---- 字幕與燒錄 --------------------------------------------------------------------

def subtitle_ratio(scale: float) -> float:
    """字幕字高佔畫面高度的比例：預設樣式 × 字幕大小（同 v1 karaoke.subtitle_ratio，100% 時和預設完全相同）。"""
    base = ass.Style().size_ratio
    return base if scale == 1 else round(base * scale, 5)


def _probe_size(path: Path) -> tuple[int, int] | None:
    """第一條真正的影像軌（內嵌封面圖不算）的寬高；純音訊回傳 None。"""
    out = subprocess.run([FFPROBE, "-v", "error", "-show_entries",
                          "stream=codec_type,width,height:stream_disposition=attached_pic", "-of", "json", str(path)],
                         capture_output=True, text=True, encoding="utf-8", errors="replace")
    if out.returncode != 0:
        raise TaskError(f"讀不到影片資訊：{out.stderr.strip()[-300:]}")
    for s in json.loads(out.stdout or "{}").get("streams", []):
        if s.get("codec_type") == "video" and s.get("disposition", {}).get("attached_pic") != 1:
            return int(s["width"]), int(s["height"])
    return None


def _attach_furigana(lines: list[dict], texts: list[str], rubies: list[list[Ruby]]) -> None:
    """替每句加上假名位置（自動讀音 + 手動讀音；同 v1 karaoke._attach_furigana）。
    只處理「逐字串起來剛好等於歌詞原文」的句子，對不上的不加假名，避免標錯字。"""
    if len(lines) != len(texts):
        return
    for line, text, rs in zip(lines, texts, rubies):
        if "".join(w["text"] for w in line["words"]) != text:
            continue
        segments = reading.furigana(text, "ja", rs)
        line["text"] = text
        line["rubies"] = [(s["start"], s["end"], s["ruby"]) for s in segments if s["ruby"]]


def build_ass(params: dict, size: tuple[int, int], font_file: Path) -> str:
    """產生 ASS 字幕（v1 karaoke._make 第 2 步）。"""
    font = params["font"]
    style = ass.Style(font=font["family"], size_ratio=subtitle_ratio(float(params.get("scale", 1))))
    lines = [dict(line) for line in params["lines"]]
    singers = params.get("singers") or []
    for line, singer in zip(lines, singers):
        line["singer"] = singer
    texts = params.get("texts", [])
    rubies = [[Ruby(**r) for r in line] for line in params.get("rubies", [])]
    if params.get("language") == "ja":
        _attach_furigana(lines, texts, rubies)
    card = params.get("title_card") or [None, None]
    return ass.build(lines, *size, style, title=card[0], artist=card[1] or None,
                     note=card[2] if len(card) > 2 else None, translations=params.get("translations") or [],
                     font_file=font_file, font_index=int(font.get("index", 0)))


def _render(params: dict, ctx: Context) -> dict:
    media = ctx.inputs["media"]
    size = _probe_size(media)
    width, height = size or (1920, 1080)
    font_file = ctx.font(params["font"])
    ass_path = ctx.out_dir / "karaoke.ass"
    if "ass" in ctx.inputs:
        ctx.log("  . 使用手動修改過的字幕")
        shutil.copyfile(ctx.inputs["ass"], ass_path)
    else:
        ctx.log("  . 產生字幕")
        # utf-8-sig：Aegisub 等軟體要 BOM 才認得是 UTF-8（同 v1）
        ass_path.write_text(build_ass(params, (width, height), font_file), encoding="utf-8-sig")
        ctx.files.append("karaoke.ass")
    if ctx.should_stop():
        raise Cancelled()
    _burn(media, ass_path, ctx.out_dir / "video.mp4", (width, height), size is not None, font_file, params, ctx)
    ctx.files.append("video.mp4")
    return {"width": width, "height": height, "ass_sha256": hashlib.sha256(ass_path.read_bytes()).hexdigest()}


ENCODERS = {
    # v1 的參數：NVENC p5 cq23 / x264 medium crf18
    "h264_nvenc": ["-c:v", "h264_nvenc", "-preset", "p5", "-cq", "23"],
    "libx264": ["-c:v", "libx264", "-preset", "medium", "-crf", "18"],
}


def _burn(src: Path, ass_path: Path, dest: Path, size: tuple[int, int], has_video: bool, font_file: Path,
          params: dict, ctx: Context) -> None:
    """燒錄字幕（v1 karaoke._burn）。字型只用 NAS 給的那個檔（fontsdir 只放它），多台 worker 燒出來才一致。"""
    work = ctx.out_dir / "burn"
    fonts = work / "fonts"
    fonts.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(font_file, fonts / font_file.name)
    # subtitles 濾鏡對路徑中的冒號、括號等字元很敏感：用工作資料夾裡的 ASCII 相對路徑
    shutil.copyfile(ass_path, work / "sub.ass")
    if has_video:
        inputs, maps = ["-i", str(src)], ["-map", "0:v:0", "-map", "0:a:0"]
    else:
        w, h = size   # 純音訊來源：用黑底當畫面
        inputs = ["-f", "lavfi", "-i", f"color=c=black:s={w}x{h}:r=30", "-i", str(src)]
        maps = ["-map", "0:v:0", "-map", "1:a:0", "-shortest"]
    base = [FFMPEG, "-y", "-v", "error", *inputs, *maps, "-vf", "subtitles=sub.ass:fontsdir=fonts"]
    tail = ["-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "320k", "-movflags", "+faststart", "out.mp4"]
    err = ""
    for name in params.get("encode", {}).get("prefer") or ["h264_nvenc", "libx264"]:
        if name not in ENCODERS:
            continue
        ctx.log(f"  . 燒錄字幕（{name}）...")
        code, err = run_cancellable([*base, *ENCODERS[name], *tail], ctx.should_stop, cwd=work)
        if code == 0:
            (work / "out.mp4").replace(dest)
            shutil.rmtree(work, ignore_errors=True)
            return
        ctx.log(f"  . {name} 無法使用，改用下一個編碼器")
    raise TaskError(f"燒錄失敗：{err.strip()[-500:]}")
