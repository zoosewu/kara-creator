# 部署、開發環境與測試

## NAS：Mac mini M1（container，用 OrbStack）

Mac 上本來就在跑 OrbStack，所以 NAS 伺服器以 container 執行（`deploy/compose.nas.yml`），不做 launchd 安裝。
Go 執行檔本身照樣可以在 macOS 原生編譯執行（開發、除錯用），但不提供安裝腳本。

- 曲庫在外接硬碟（APFS），bind mount 到 container 的 `/library`
- clone / 硬連結在 OrbStack 的 bind mount 上能不能用，取決於 Mac 端（匯出會自動退回複製，不影響正確性）
- `restart: unless-stopped`；OrbStack 設成登入時啟動
- 提醒使用者：系統設定 → 能源 →「顯示器關閉時防止自動進入睡眠」，否則 AI 伺服器連不到
- 外接硬碟沒掛上時不會啟動成空的曲庫：沒有 `/library/library.json` 就結束並說明（第一次用 `--init` 明確建立新曲庫）
- 資料備份：把 Mac 的 SSH 金鑰唯讀掛載進 container 來 `git push`

### container 映像（Mac 的 OrbStack 與 Linux NAS 共用）

`deploy/nas.Dockerfile`，多階段建置：

```
web    node:24（建置機器的架構）   → npm ci && npm run build（前端）
go     golang:1.27（建置機器的架構）→ 交叉編譯 kara-nas（CGO_ENABLED=0，-trimpath -ldflags "-s -w"）
fetch  debian:trixie-slim          → yt-dlp 單一執行檔（依目標架構）、Noto Sans CJK Bold
最後   debian:trixie-slim + git、openssh-client、tini、tzdata
       + ffmpeg / ffprobe（mwader/static-ffmpeg）、deno（denoland/deno:bin）
       ENTRYPOINT tini -- nas-entrypoint.sh（處理 PUID / PGID 與 SSH 金鑰後執行 kara-nas）
```

- 架構 `linux/arm64` 與 `linux/amd64`；前端與 Go 用交叉編譯，arm64 映像在 x86 電腦上也能很快建好
- 大小（2026-10-04，amd64）：下載約 270 MB、解壓後約 660 MB；最大的是 ffmpeg + ffprobe 靜態版（約 280 MB，NAS 要轉 mp3 / aac / vorbis / opus / flac）、deno（約 95 MB）
- 映像裡的工具與字型：`KARA_TOOLS=/opt/kara/tools`（yt-dlp、deno；yt-dlp 每天自己更新，重建 container 後回到映像的版本再更新）、
  `KARA_FONTS=/opt/kara/fonts`（預設字型，和曲庫的 `fonts/` 一起掃描，同一個檔案用曲庫的）
- volume：`/library`（整個曲庫，含 `data/`、`export/`、`fonts/`；**export 必須在同一個掛載點**，匯出才能 clone / 硬連結）、
  `/ssh`（push 備份用的金鑰，唯讀，選填；入口腳本複製到家目錄並改成 600）
- `PUID` / `PGID`（選填）：用這個身分執行，寫出的檔案擁有者對應到 NAS 使用者，SMB 那邊才改得動。沒設就用 root
  （Mac 的 OrbStack 寫進 bind mount 的檔案本來就屬於 Mac 的使用者）
- `TZ`：時區（紀錄時間用）

### 部署步驟（Mac mini）

見根目錄 README「部署 NAS 伺服器」：下載 `compose.nas.yml`、在同一個資料夾寫 `.env`（`KARA_LIBRARY_DIR` 等）、
第一次 `run --rm kara-nas --init`，之後 `up -d`；更新是 `pull` 再 `up -d`。不必 clone 程式碼。

映像由 CI（`.github/workflows/images.yml`）在 main 有變動時建置並推到 ghcr.io：NAS 是 amd64 + arm64，
AI 是 amd64（PyTorch 那層用 registry 快取，只改程式碼時不必重新上傳）。自己建置：
`docker buildx build --platform linux/arm64 -f deploy/nas.Dockerfile -t ghcr.io/zoosewu/kara-creator-nas --load .`

## AI 伺服器

### container（建議）

`deploy/ai.Dockerfile`（映像 `ghcr.io/zoosewu/kara-creator-ai`，執行 `kara-worker`）：

```
ubuntu:24.04 + tini + ffmpeg（BtbN 靜態版，含 NVENC）
+ Python 3.14（uv）
+ PyTorch 2.11 cu130（單獨一層：最大、最少變動）
+ worker/pyproject.toml 的相依套件（單獨一層）
+ kara-worker 本身（versions.json 打包進套件）
```

- PyTorch 的 cu130 wheel 自帶 CUDA 函式庫，底層用一般的 Ubuntu 即可（驅動由 nvidia-container-runtime 掛進來；
  NVENC 需要 `NVIDIA_DRIVER_CAPABILITIES=compute,video,utility`，映像已經設好）
- 模型（Whisper large-v3 約 3 GB、MMS 約 1.2 GB、Demucs）放 volume `kara-models`，快取放 `kara-ai-cache`，更新映像不必重新下載
- Windows：Docker Desktop + WSL2 + NVIDIA 驅動
- 部署：`deploy/compose.ai.yml` + `.env`（`KARA_NAS`、`KARA_WORKER_NAME`、`KARA_WORKER_TOKEN`），見根目錄 README「部署 AI 伺服器」

### Windows 直接執行

`worker/setup.ps1` 下載 ffmpeg 到 `worker/tools/`、建 `worker/.venv`（Python 3.14 + PyTorch cu130），以 editable 方式安裝 kara-worker；
`worker/worker.ps1` 把 ffmpeg 加進 PATH 後執行 `kara-worker`。更新：`git pull` 後再執行一次 `setup.ps1`。

> 2026-10-04 使用者決定：這個方式保留，但沒有實測過；實際部署由使用者自己執行，遇到問題再討論。

## 映像與 CI

- `.github/workflows/ci.yml`：Go（vet、race 測試、OpenAPI 是否過期）、前端（check、build）、worker（ruff、pytest，不裝 PyTorch）
- `.github/workflows/images.yml`：main 有相關變動時建置並推到 ghcr.io（NAS 是 amd64 + arm64，AI 是 amd64；
  PyTorch 那層用 registry 快取，只改程式碼時不必重新上傳）。標籤 `latest`、`sha-xxxxxxx`，推 `v*` tag 時另外推版本號
- 自己建置：`docker buildx build --platform linux/arm64 -f deploy/nas.Dockerfile -t ghcr.io/zoosewu/kara-creator-nas --load .`

## 開發環境（container 內）

使用者會在 container 內 clone 這個 repo 開發。開發 container 內的工具可以直接安裝或更新到最新版
（使用者的 Windows PC、Mac 上安裝東西仍然要先問）。

| 工具 | 用途 | 目前（2026-10-03） |
| --- | --- | --- |
| Go（最新穩定版） | NAS 伺服器 | 1.27.1 |
| Node.js LTS + npm | 前端建置 | 24.21 |
| Python 3.14 + CPU 版 PyTorch 2.11 | worker 的測試與 ruff | `.venv-linux/`（uv 建立） |
| ffmpeg | 兩邊都要 | BtbN 的最新靜態版，`~/.local/bin` |
| Chromium | 前端截圖驗證 | `google-chrome`、Playwright 的 Chromium |

```sh
go test -race ./...                     # Go（含假的 worker）
cd nas/web && npm run check && npm run build
cd worker && ../.venv-linux/bin/python -m pytest -q && ../.venv-linux/bin/ruff check   # worker
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
    -e PYTHONPATH=/workspace/kara-creator/worker/src -w /workspace/kara-creator kara-ai-dev \
    -m kara_worker --nas http://172.17.0.2:8799 --name gpu-4070
  ```
- 網路：GitHub 下載很慢（約 0.5 MB/s），Whisper 模型（Azure）、PyPI 快；YouTube 需要 JavaScript runtime（沒有 deno 時用 node）

## 測試策略

### 規格測試（NAS 的純規則）

`nas/testdata/spec/*.json` 是規格資料：**自己編的**輸入與應有的結果，Go 的測試逐一比對。
規則要改時，同時改程式和規格資料（並說明為什麼改）。

| 檔案 | 內容 |
| --- | --- |
| `lyrics_parse.json` | 歌詞文字 → 結構 → 寫回（讀音簡寫、`{原字|讀音}`、台羅音節數規則、翻譯行、註解、空行、全形空白）與語言判斷 |
| `lyrics_plain.json` | 「原始歌詞」改過之後新舊句子的對應（插入、刪除、修改） |
| `paren.json` | 括號讀音提示與轉換 |
| `language.json` | 依歌詞文字判斷語言 |
| `readings.json` | 手動與自動讀音的合併、歌詞編輯器的各種檢視 |
| `titles.json` | 標題辨識（各種 YouTube 標題、頻道名稱、feat.、中英並列） |
| `catalog.json` | 曲庫的資料夾與拖曳排序（隨機操作序列，每一步的結果） |
| `export_names.json` | 匯出檔名、同名編號、非法字元 |
| `shift_timing.json` | 移動單句的連鎖推動與壓縮 |
| `difflib.json` | 和 Python difflib 相同的比對結果 |
| `fingerprint.json` | data.md 的指紋（算法改了所有歌都會被判定需要重做） |

字串處理以 Python 的語意定義（`nas/internal/pystr` 的 isspace、splitlines、casefold，以及 `difflib`），規格資料就是這樣算出來的。
**範例歌詞、標題一律自己編**，不能用真實歌曲（版權，公開 repo）。

### 假的 worker

`nas/internal/fakeworker`（`go run ./nas/cmd/fakeworker`）：實作 worker 協定，回傳固定的結果（例如每句平均分配時間、
把輸入影片直接當成成品），用來測 NAS 的整條流程、取消、改派、版本不符、斷線，不需要 GPU 與模型。
`api.TestEndToEnd` 用它跑完「下載 → 去人聲 → 對時 → 燒錄 → 檢查 → 確認 → 匯出 → 備份」。

### worker 的 GPU 回歸比對

改 worker 的程式（重構、升級套件）但不打算改變結果時：

1. 改之前，用目前的程式對幾首歌（公有領域的歌 + 自己編的歌，檔案放在 repo 外面）跑每種任務，記下輸出：
   `align`、`align_from`、`align_line`、`qa`、`reading`、`render` 的 ASS 都是確定性的，`separate` 記下 wav
2. 改之後再跑一次：確定性的任務要**逐字相同**；去人聲在 GPU 上本來就不是位元相同，比對人聲 / 伴奏的 SNR，
   要和「改之前的程式自己跑兩次」的差異在同一個範圍（2026-10-08 的例子：約 28–34 dB）
3. 結果有變就是行為改了：要嘛是錯誤，要嘛要調 versions.json 的版本（`align` 要先問使用者）
