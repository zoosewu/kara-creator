"""ffmpeg / ffprobe 相關的小工具。"""
from __future__ import annotations

import subprocess
from pathlib import Path

from .config import FFMPEG, FFPROBE

AUDIO_EXTS = {".mp3", ".wav", ".flac", ".m4a", ".aac", ".ogg", ".opus", ".wma", ".aiff"}
VIDEO_EXTS = {".mp4", ".mkv", ".mov", ".webm", ".avi", ".m4v", ".flv"}
MEDIA_EXTS = AUDIO_EXTS | VIDEO_EXTS

# 每個容器對應的音訊編碼器；沒列到的容器一律以 aac 輸出。
AUDIO_CODECS = {
    ".mp3": ["-c:a", "libmp3lame", "-q:a", "0"],
    ".m4a": ["-c:a", "aac", "-b:a", "320k"],
    ".aac": ["-c:a", "aac", "-b:a", "320k"],
    ".ogg": ["-c:a", "libvorbis", "-q:a", "8"],
    ".opus": ["-c:a", "libopus", "-b:a", "256k"],
    ".flac": ["-c:a", "flac"],
    ".wav": ["-c:a", "pcm_s16le"],
    ".aiff": ["-c:a", "pcm_s16be"],
    ".wma": ["-c:a", "aac", "-b:a", "320k"],
}


class Cancelled(Exception):
    """使用者取消了工作；處理途中遇到時一路往上拋，不當成失敗。"""


def run_cancellable(cmd: list[str], should_stop=None, **popen_kwargs) -> tuple[int, str]:
    """執行外部指令並回傳 (returncode, stderr)；should_stop() 為真時終止子程序並拋出 Cancelled。"""
    proc = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
                            text=True, encoding="utf-8", errors="replace", **popen_kwargs)
    try:
        while True:
            try:
                _, err = proc.communicate(timeout=0.5)
                return proc.returncode, err
            except subprocess.TimeoutExpired:
                if should_stop and should_stop():
                    raise Cancelled()
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait()


def run(cmd: list[str]) -> subprocess.CompletedProcess:
    """執行外部指令，失敗時把 stderr 一起帶進例外訊息。"""
    proc = subprocess.run(cmd, capture_output=True, text=True, encoding="utf-8", errors="replace")
    if proc.returncode != 0:
        raise RuntimeError(
            f"指令失敗 ({proc.returncode}): {' '.join(cmd[:3])} ...\n{proc.stderr[-2000:]}"
        )
    return proc


def has_video_stream(path: Path) -> bool:
    """判斷檔案是否含有真正的影像軌（內嵌封面圖不算）。"""
    proc = run([
        FFPROBE, "-v", "error",
        "-select_streams", "v",
        "-show_entries", "stream=codec_name:stream_disposition=attached_pic",
        "-of", "csv=p=0",
        str(path),
    ])
    for line in proc.stdout.strip().splitlines():
        parts = line.split(",")
        attached_pic = parts[-1].strip() if len(parts) > 1 else "0"
        if attached_pic != "1":
            return True
    return False


def duration(path: Path) -> float | None:
    """媒體長度（秒）；讀不到時回傳 None。"""
    try:
        proc = run([FFPROBE, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", str(path)])
        return round(float(proc.stdout.strip()), 3)
    except (RuntimeError, ValueError):
        return None


def video_size(path: Path) -> tuple[int, int] | None:
    """回傳第一條影像軌的 (寬, 高)，沒有影像軌則回傳 None。"""
    if not has_video_stream(path):
        return None
    proc = run([
        FFPROBE, "-v", "error", "-select_streams", "v:0",
        "-show_entries", "stream=width,height", "-of", "csv=p=0", str(path),
    ])
    w, h = proc.stdout.strip().splitlines()[0].split(",")[:2]
    return int(w), int(h)


def extract_wav(src: Path, dst: Path) -> None:
    """抽出音軌成 44.1kHz 立體聲 wav，作為 Demucs 的輸入。"""
    run([
        FFMPEG, "-y", "-v", "error",
        "-i", str(src),
        "-vn", "-map", "0:a:0",
        "-ac", "2", "-ar", "44100", "-c:a", "pcm_s16le",
        str(dst),
    ])


def mux(audio: Path, source: Path, dest: Path, keep_video: bool) -> None:
    """把音軌封裝成目標容器，keep_video 時沿用 source 的影像軌（不重新編碼）。"""
    ext = dest.suffix.lower()
    codec = AUDIO_CODECS.get(ext, ["-c:a", "aac", "-b:a", "320k"])

    cmd = [FFMPEG, "-y", "-v", "error"]
    if keep_video:
        cmd += [
            "-i", str(source), "-i", str(audio),
            "-map", "0:v:0", "-map", "1:a:0",
            "-c:v", "copy", *codec,
            "-map_metadata", "0",
            "-shortest",
        ]
        if ext in {".mp4", ".m4v", ".mov"}:
            cmd += ["-movflags", "+faststart"]
    else:
        cmd += ["-i", str(audio), *codec]
    cmd.append(str(dest))
    run(cmd)
