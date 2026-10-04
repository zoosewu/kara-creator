# 部署、開發環境與測試

## NAS：Mac mini M1（Q1：container，用 OrbStack）

Mac 上本來就在跑 OrbStack，所以 NAS 伺服器以 container 執行（`deploy/compose.nas.yml`），不做 launchd 安裝。
Go 執行檔本身照樣可以在 macOS 原生編譯執行（開發、除錯用），但不提供安裝腳本。

- 曲庫在外接硬碟（APFS，Q2），bind mount 到 container 的 `/library`
- clone / 硬連結在 OrbStack 的 bind mount 上能不能用，階段 5 在 Mac 上實測（匯出會自動退回複製，不影響正確性）
- `restart: unless-stopped`；OrbStack 設成登入時啟動
- 提醒使用者：系統設定 → 能源 →「顯示器關閉時防止自動進入睡眠」，否則 AI 伺服器連不到
- 外接硬碟沒掛上時 container 不要啟動成空的曲庫：啟動時檢查 `/library/library.json`（或 `--init` 明確建立新曲庫），沒有就結束並說明
- 資料備份：把 Mac 的 SSH 金鑰唯讀掛載進 container 來 `git push`

### container 映像（Mac 的 OrbStack 與 Linux NAS 共用）

`deploy/nas.Dockerfile`，多階段建置：

```
stage 1  node:lts      → npm ci && npm run build（前端）
stage 2  golang        → go build（CGO_ENABLED=0，-trimpath -ldflags "-s -w"）
stage 3  debian:bookworm-slim 或 alpine
         + ffmpeg（靜態版）、yt-dlp 單一執行檔、deno、git、openssh-client、tini
         + Noto Sans CJK 預設字型（OFL 授權，可以放進映像）
         ENTRYPOINT tini -- kara-nas
```

- 映像目標 < 300 MB；架構 `linux/arm64` 與 `linux/amd64`（`docker buildx`）
- volume：`/library`（整個曲庫，**必須是同一個 volume**，匯出才能 clone / 硬連結）、`/data`（備份 repo）、`/fonts/custom`（自訂字型，選填）、
  `/root/.ssh`（push 備份用，唯讀）
- `PUID` / `PGID`：寫出的檔案擁有者對應到 NAS 使用者，SMB 那邊才改得動
- `TZ`：時區（紀錄時間用）

## AI 伺服器

### Windows 本機（現在的 PC）

> 2026-10-04：host 版保留，但先不實測；實際部署由使用者自己執行，遇到問題再討論。container 版已在同一台 PC（Docker Desktop + WSL2）驗證過。

- `setup.ps1` 建好 `.venv`（同 v1：Python 3.14 + PyTorch 2.11 CUDA 13.0 + requirements），另外下載預設字型到快取（其實不必：缺字型時會向 NAS 要）
- `ai.ps1`：只執行 `.venv\Scripts\python.exe ai\worker.py @args`
- requirements 拆成 `requirements-ai.txt`（worker 需要的：torch、demucs、stable-ts、fugashi、unidic-lite、pykakasi、pypinyin、numpy、soundfile、pillow）；
  v1 UI 的套件（fastapi、uvicorn、yt-dlp）在階段 6 移除

### container（CUDA）

`deploy/ai.Dockerfile`：

```
FROM nvidia/cuda:13.0.x-cudnn-runtime-ubuntu24.04
+ Python 3.14、ffmpeg（含 NVENC 的版本）、requirements-ai.txt（torch cu130）
ENTRYPOINT python ai/worker.py
```

- `docker run --gpus all -e KARA_NAS=http://mac-mini.local:8765 -e KARA_WORKER_NAME=pc-4070 -v kara-ai-cache:/cache -v kara-models:/models …`
- 模型（Whisper large-v3 約 3 GB、MMS 約 1.2 GB、Demucs）放 volume，避免每次重建都重新下載
- Windows 上用 Docker Desktop + WSL2 + NVIDIA 驅動的 GPU 支援；ffmpeg 的 NVENC 在 container 內要另外驗證（需要 `NVIDIA_DRIVER_CAPABILITIES=compute,video,utility`）

### compose 範例

`deploy/compose.nas.yml`（NAS）、`deploy/compose.ai.yml`（GPU 電腦）。兩邊分開，因為通常不在同一台。

## 開發環境（container 內）

使用者會在 container 內 clone 這個 repo 開發。2026-10-03 使用者同意開發 container 內的工具可以直接安裝或更新到最新版
（使用者的 Windows PC、Mac 上安裝東西仍然要先問）。

| 工具 | 用途 | 目前（2026-10-03） |
| --- | --- | --- |
| Go（最新穩定版） | NAS 伺服器 | 1.27.1 |
| Node.js LTS + npm | 前端建置 | 24.21 |
| Python 3.14 + CPU 版 PyTorch 2.11 | worker 的單元測試、產生黃金測試資料、搬遷工具 | `.venv-linux/`（uv 建立） |
| ffmpeg | 兩邊都要 | BtbN 的最新靜態版，`~/.local/bin` |
| Chromium | 前端截圖驗證 | `google-chrome`、Playwright 的 Chromium |

```sh
go test -race ./...                     # Go（含假的 worker）
cd nas/web && npm run check && npm run build
.venv-linux/bin/python -m pytest -q     # Python
go run ./nas/cmd/kara-nas --library <暫存曲庫> --listen 127.0.0.1:8766 &
go run ./nas/cmd/fakeworker --nas http://127.0.0.1:8766 --delay 2s
```

開發 container 是 DooD（Docker outside of Docker，主機是使用者 PC 上的 Docker Desktop + WSL2），**可以直接建立 container，
而且主機有 nvidia runtime（RTX 4070 Ti SUPER）**：GPU 驗證可以在這裡做。

- repo 在 docker volume `zoo_volume`（掛在 `/workspace`），其他 container 用 `-v zoo_volume:/workspace` 拿到同一份程式
- 開發 container 在預設的 bridge 網路（172.17.0.2）：NAS 監聽 `0.0.0.0:<port>`，worker container 連 `http://172.17.0.2:<port>`
- 開發用的 worker 映像：沿用主機上已有的 `yanwk/comfyui-boot:cu130-megapak-pt211`（Python 3.13 + PyTorch 2.11 cu130 + ffmpeg/NVENC），
  只補裝 demucs、stable-ts 等小套件；模型放 volume `kara-models`、快取放 `kara-ai-cache`
  ```sh
  docker run -d --name kara-ai-dev --gpus all -v zoo_volume:/workspace -v kara-models:/models -v kara-ai-cache:/cache \
    -w /workspace/kara-creator kara-ai-dev ai/worker.py --nas http://172.17.0.2:8799 --name gpu-4070
  ```
- 網路：GitHub 下載很慢（約 0.5 MB/s），Whisper 模型（Azure）、PyPI 快；YouTube 需要 JavaScript runtime（沒有 deno 時用 node）

## 測試策略

### 黃金測試（Go 移植 v1 的規則）

`tools/golden.py`：用 v1 的 Python 實作，對**自己編的**輸入產生答案，存成 `nas/testdata/golden/*.json`；Go 的測試讀它比對。

| 檔案 | 內容 |
| --- | --- |
| `lyrics_parse.json` | 歌詞文字 → 結構 → 寫回（含讀音簡寫、`{原字|讀音}`、台羅音節數規則、翻譯行、註解、空行、全形空白） |
| `lyrics_plain.json` | `_from_plain`：新舊歌詞對應（插入、刪除、修改句子） |
| `paren.json` | 括號讀音提示與轉換 |
| `language.json` | `detect_language` |
| `titles.json` | 標題辨識（各種 YouTube 標題、頻道名稱、feat.、中英並列） |
| `catalog_place.json` | 拖曳排序的編號變化 |
| `export_names.json` | 匯出檔名、同名編號、非法字元 |
| `shift_timing.json` | 移動單句的連鎖推動與壓縮 |
| `local_id.json` | 手動放入的 id |
| `fingerprint.json` | data.md 的指紋（Python 的搬遷工具和 Go 要算出一樣的值） |

**範例歌詞、標題一律自己編**，不能用真實歌曲（版權，公開 repo）。

### 假的 worker

`nas/internal/scheduler/fakeworker`（或 `tools/fakeworker.py`）：實作 worker 協定，回傳固定的結果（例如每句平均分配時間、
把輸入影片直接當成成品），用來測 NAS 的整條流程、取消、改派、版本不符、斷線，不需要 GPU 與模型。

### 真實 GPU 驗證（使用者的 Windows PC）

階段 2 完成時，請使用者在 PC 上執行 worker 連到開發中的 NAS，確認：

- 對時、檢查、AI 重對的結果和 v1 **逐字相同**（2026-10-03 拆分 AI 伺服器時用過的做法：同一首歌兩邊各跑一次，比對 JSON 是否完全相同）
- 去人聲的結果和 v1 相同（或差異在可接受範圍：GPU 運算不保證位元相同，比對人聲 / 伴奏的能量差）
- 燒錄出來的畫面：和 v1 同一首歌（換成同一個字型）截幾個時間點比對

### 搬遷演練

用使用者真實曲庫的**複本**跑搬遷工具試算（在使用者的 Windows 上執行，報告不進 git），確認：

- 沒有任何一首會重新對時
- 手動調整的時間全部保留
- 已確認的狀態延續（依 Q6 的結論）
