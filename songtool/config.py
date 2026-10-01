"""專案路徑與外部工具位置，所有模組共用。"""
from __future__ import annotations

import os
import shutil
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parent.parent
TOOLS_DIR = PROJECT_ROOT / "tools"
FFMPEG_BIN = TOOLS_DIR / "ffmpeg" / "bin"

# 可用環境變數 SONG_OUTPUT_DIR 改變輸出根目錄（測試或日後 UI 指定工作區時用）。
OUTPUT_DIR = Path(os.environ.get("SONG_OUTPUT_DIR") or PROJECT_ROOT / "output")
DOWNLOADS_DIR = OUTPUT_DIR / "downloads"
SEPARATED_DIR = OUTPUT_DIR / "separated"
KARAOKE_DIR = OUTPUT_DIR / "karaoke"
# 依曲庫編號整理好的成品（硬連結），以及曲庫的資料夾、編號、歌名資料。
EXPORT_DIR = OUTPUT_DIR / "export"
LIBRARY_FILE = OUTPUT_DIR / "library.json"

# 歌詞是使用者提供的輸入，不放在 output/ 以免和產出物混在一起。
LYRICS_DIR = Path(os.environ.get("SONG_LYRICS_DIR") or PROJECT_ROOT / "lyrics")

# 可重做的資料備份（歌單、影片連結、歌詞、對時）。是另一個私人 git repo，不進公開的程式 repo。
DATA_DIR = Path(os.environ.get("SONG_DATA_DIR") or PROJECT_ROOT / "data")
# 使用者自己的 hook 腳本（例如佇列清空後 rsync 輸出資料夾）。
HOOKS_DIR = Path(os.environ.get("SONG_HOOKS_DIR") or PROJECT_ROOT / "hooks")

# 專案自帶的工具優先，找不到才退回系統 PATH 上的版本。
for _bin in (FFMPEG_BIN, TOOLS_DIR / "deno"):
    if _bin.is_dir():
        os.environ["PATH"] = str(_bin) + os.pathsep + os.environ.get("PATH", "")

FFMPEG = shutil.which("ffmpeg") or "ffmpeg"
FFPROBE = shutil.which("ffprobe") or "ffprobe"
