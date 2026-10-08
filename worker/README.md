# kara-worker

伴唱帶工作室的 AI worker：主動連到 NAS 領任務，做去人聲（Demucs）、對時（Whisper + CTC）、對時檢查、假名與燒錄字幕。
部署方式見根目錄的 [README](../README.md)，協定見 [docs/worker-protocol.md](../docs/worker-protocol.md)。

```
src/kara_worker/
  __main__.py     命令列（kara-worker）
  client.py       和 NAS 的協定：打招呼、領任務、心跳、下載輸入檔、上傳結果
  tasks.py        各種任務的執行
  separate.py     去人聲（Demucs 在子程序 demucs_run.py 裡跑）
  align.py        對時
  qa.py           對時檢查
  reading.py      讀音與變色單位
  subtitles.py    ASS 字幕
  lyrics.py       歌詞的資料結構
  media.py        ffmpeg / ffprobe
  versions.py     versions.json（和 NAS 共用）
```

開發（不需要顯示卡的測試）：

```sh
uv venv && uv pip install -e ".[dev]" --torch-backend cpu
uv run pytest
uv run ruff check
```
