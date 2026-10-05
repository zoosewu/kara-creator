# v2 施工待辦

規格：[docs/v2/](../docs/v2/README.md)。Q1–Q15 已於 2026-10-03 定案（見 docs/v2/README.md）。

## 準備

- [x] Q1–Q15 結論寫回 docs/v2；移除 hook（Q5）
- [x] 開發環境：Go 1.27.1、Node 24 LTS、Python 3.14（`.venv-linux`，CPU 版 PyTorch 2.11）、Chromium（Playwright 已有）
- [x] ffmpeg（最新靜態版，`~/.local/bin`）

## 階段 0：骨架

- [x] `versions.json`（protocol 1、separate 1、align 7、qa 4、reading 1、render 1）
- [x] Go module（repo 根目錄 `go.mod`，程式在 `nas/`；根目錄的 `versions.go` 用 go:embed 內嵌 versions.json）
- [x] `nas/cmd/kara-nas`：參數與環境變數、`/healthz`、內嵌前端、優雅關閉
- [x] `nas/web`：Svelte 5 + Vite + TypeScript，build 到 `nas/internal/api/dist`
- [x] `nas/internal/workerproto`：worker 協定的資料結構（NAS 端與假 worker 共用）
- [x] 假的 AI worker（Go，`nas/internal/fakeworker` + `nas/cmd/fakeworker`）+ 測試
- [x] Python：`pyproject.toml`（pytest 設定）、`ai/tests/`（versions.json 與 v1 VERSION 一致）
- [x] CI：`.github/workflows/v2.yml`（go vet/test/build、npm build、pytest）
- [x] 完成條件：`go build`、`npm run build`、`pytest` 都能跑

## 階段 1：Go NAS 核心

依相依順序。純規則的模組先寫黃金測試（`tools/golden.py` 用 v1 產生答案 → `nas/testdata/golden/*.json`）。

- [x] `tools/golden.py` + `lyrics`：parse / serialize / 括號讀音 / detect_language
- [x] `difflib`：移植 Python `SequenceMatcher(autojunk=False).get_opcodes()` + `_from_plain`
- [x] `titles`：標題辨識
- [x] `fingerprint`：data.md 的指紋（Python 版也要，搬遷工具用）
- [x] `store`：`song.json`、`library.json`、原子寫入、`--init`、外接硬碟沒掛上時拒絕啟動
- [x] `library`：資料夾、順序（v1 `catalog.place`）、display_info
- [x] `media`：ffprobe / ffmpeg 包裝（抽音軌、封裝、speech.wav、vocals.flac）
- [x] `timing`：`shift_timing`、AI 重對結果的套用
- [x] `readings`：讀音快取 + 手動 / 自動讀音合併（v1 `reading.furigana`）
- [x] `planner`：狀態判斷、下一步
- [x] `scheduler`：AI 任務佇列、/worker/v1 端點、優先順序、快取優先、租約、改派、取消（假的 worker 測）
- [x] `pipeline`：去人聲、製作伴唱帶、檢查、AI 重對（整合測試）
- [x] `jobs`：工作佇列（同一首歌依序、下載 2 條、批次上限、互動優先、持久化、關機後繼續、收尾）
- [x] `download`（假的 yt-dlp 測試；更新 / 退回上一版）、`inbox`（fsnotify）、`proc`（取消時整個 process group 結束）
- [x] `export`：clone（Linux FICLONE / macOS clonefile）→ 硬連結 → 複製；檔名規則黃金測試
- [x] `backup`
- [x] `restore`（`kara-nas restore`；v2 與 v1 格式的備份、v1 歌詞雜湊驗證、確認延續、曲庫檔案鎖）
- [x] `fonts`：字型目錄（自己讀 name / OS/2 表）、預設字型、字型 id = sha256:index
- [x] `app`（組裝、狀態快取、檔案監看、收尾、yt-dlp 自動更新）+ `api`：huma REST + SSE + 媒體 / 字型 + `openapi` 子命令
- [x] 完成條件：假的 worker 跑完「下載 → 去人聲 → 對時 → 燒錄 → 檢查」（`api.TestEndToEnd`）；黃金測試通過

## 階段 2：Python AI worker

- [x] `ai/worker.py`（協定、兩個通道、續傳、字型快取）、`ai/tasks.py`（沿用 v1 execute；讀音、字幕與燒錄）
- [x] 真實 GPU（RTX 4070 Ti SUPER，DooD container）：下載 → 去人聲 → 對時 → NVENC 燒錄 → 檢查
- [x] 和 v1 逐字相同：日文（ふるさと）、國語（茉莉花）的對時、檢查、只重對這句、這句及之後全部（同一份 speech.wav，v1 / v2 產生的 speech.wav 位元組相同）
- [x] 正式映像檔 `deploy/ai.Dockerfile`（9.3 GB，Python 3.14 + PyTorch 2.11 cu130）建置並實際跑完一首
- [x] `worker.ps1`（Windows；v1 的 ai.ps1 保留到搬家）
- 延後：Windows 本機實測 `worker.ps1`（2026-10-04 使用者決定：host 版保留但先不測，實際部署由使用者自己跑，有問題再討論）

## 階段 3：前端（Svelte）

- [x] 曲庫、資料夾、拖曳、批次、新增歌曲、歌曲資訊、設定（字型、系統資訊）、AI 伺服器與佇列、執行紀錄
- [x] 歌詞編輯器（四種檢視、讀音、翻譯、時間微調）、播放畫面（即時字幕、鍵盤、AI 重對、確認）
- [x] 確認與匯出：只需重燒 / 需重新對時、確認紀錄與改了哪幾句、只匯出已確認的、按了才匯出（待匯出 / 待移除）
- [x] Playwright 截圖與互動腳本驗證（亮 / 暗、手機寬度、AI 離線、NAS 斷線重連）
- [x] 附上歌詞、新歌放進選取的資料夾（階段 5 的 container 測試用 API 實測）
- [ ] 台語 / 粵語每字一格：用新的歌實測

## 階段 4：搬遷（延後）

2026-10-04 使用者決定：先把系統做完、放上生產環境，之後再用 `kara-nas restore` 從 v1 的資料備份搬 metadata（使用者有原始影片，
不需要搬影片、去人聲與成品）。原本「在 Windows 上跑的 Python 搬遷工具」取消。上線後要討論：`--sources`（用現有影片不重新下載）、
v1 的已確認要不要帶過來、手動改過的 .ass。

## 階段 5：打包

- [x] 內建字型資料夾（`--fonts` / `KARA_FONTS`，和 `<library>/fonts` 一起掃描）；映像附 Noto Sans CJK Bold（固定版本）
- [x] `deploy/nas.Dockerfile`：前端 → Go → debian slim + ffmpeg / ffprobe、yt-dlp、deno、git、ssh、tini、字型；`PUID` / `PGID`
- [x] `deploy/compose.nas.yml`、`deploy/compose.ai.yml`
- [x] 在這裡實際跑 amd64 映像：曲庫沒掛上時拒絕啟動、`--init`、檔案擁有者、worker 連上做完一首、匯出、yt-dlp 更新、備份
- [x] arm64：buildx 建置並用模擬跑起來（Mac 上實際部署由使用者執行）
- [x] deploy.md 寫部署步驟

## 部署整理（2026-10-05）

- [x] README 重寫（精簡：架構、NAS / AI 的 docker compose、AI 在 Windows host 執行、基本用法）；舊 README 移到 docs/v1.md
- [x] CI 自動建置映像推到 ghcr.io（`.github/workflows/images.yml`；NAS amd64 + arm64、AI amd64）
- [x] compose 改用 ghcr.io 的映像，設定放 `.env`
- [ ] 第一次推上 main 後確認 Actions 跑完、映像拉得下來（套件 visibility 可能要手動設成 Public）

## 檢討

### 階段 0（2026-10-03）

- 完成條件已驗證：`go vet`、`go test -race`（含假的 worker 對 stub NAS 的全部任務種類、版本不符、取消、失敗）、
  `npm run check` + `npm run build`、`pytest` 都通過；`kara-nas` 實際啟動，`/healthz`、SPA、內嵌前端截圖、Ctrl+C 關閉都正常
- 和規格不同的地方（已寫回 docs/v2）：
  - `go.mod` 放在 repo 根目錄（go:embed 不能讀上層目錄的 versions.json）
  - `reading` 任務只回傳自動讀音、不收手動讀音（快取 key 才能和手動讀音無關），手動讀音由 NAS 合併


### 階段 1（2026-10-03 完成；restore 於 10-04 補上）

- 驗證：25 個套件的測試全部通過（`go test -race ./...`）；黃金測試涵蓋歌詞、括號讀音、語言、difflib、原始歌詞對應、
  標題辨識、指紋（Go ↔ Python）、拖曳排序、shift_timing、讀音合併與編輯器檢視、匯出檔名；
  端對端測試 `api.TestEndToEnd`：假的 yt-dlp + 假的 worker 跑完下載 → 去人聲 → 對時 → 燒錄 → 檢查 → 匯出 → 備份；
  實際執行檔冒煙測試（--init 檢查、worker 連線、API 文件）
- 和規格不同、已寫回 docs/v2 的地方：
  - 成品指紋多了 `reading`（日文假名由 worker 燒錄時算）；字型用 id = sha256:index（.ttc 一個檔案有好幾個字型）
  - render 的樣式參數只傳字幕大小；RenderStage 多記 `content`（判斷手改的 ASS 還能不能沿用）
  - alignment.json 多記 lyrics / language / method / model（AI 重對前檢查歌詞、資料備份還原用）
  - 新歌的人聲是無損 FLAC，對時輸入和 v1 不同（搬遷過來的歌不受影響）
  - 正在處理的歌不能手動調時間或改歌詞（409），避免處理完蓋掉
- 踩到的坑記在 tasks/lessons.md（xh 的 stdin、寫檔工具的 \u 跳脫、PyInstaller 子程序）

### 階段 3（2026-10-04）

- 驗證：`npm run check` 0 錯誤 0 警告；`go test ./...` 全過；Playwright 腳本逐項操作（新增 / 改名 / 刪除資料夾、
  拖曳進資料夾與回最上層、停在收合資料夾自動展開、Shift 連續勾選、批次略過、確認 → 匯出 → 改名 → 匯出 → 取消確認 → 匯出、
  播放畫面鍵盤與即時字幕、只重對這句、點兩下改字 → 確認失效並標出改了第幾句、儲存並製作伴唱帶、AI 離線時的假名提示、NAS 斷線重連）
- 互動測試抓到並修好的問題：
  - 改名後匯出狀態沒更新（匯出檔名只在 GET /library 時重算）→ 每次送出 song 事件前重算，連帶受影響的同名編號
  - 取消確認後看不出匯出資料夾還有舊檔 → `.export.json` 記「檔案 → 歌曲」，新增「待移除」狀態，匯出按鈕的數字含待移除
  - worker 上傳的檔案是 0600，硬連結到匯出資料夾後 SMB 的其他使用者讀不到 → 上傳完改成 0644
  - worker 說 bye 之後「最後連線」顯示成 08:06（零值時間）→ 另外記 gone，last_seen 保留真實時間
  - 「儲存並製作伴唱帶」「更新伴唱帶」「完整歌詞」送出空的歌曲 id（關閉後 props 變空）→ 元件建立時記下 id，App 用 {#key}

### 階段 5（2026-10-04）

- 在開發環境實際跑 NAS 映像（amd64）+ 正式 AI 映像（GPU）：曲庫沒掛上時拒絕啟動、`--init`、PUID / PGID（行程與寫出的檔案擁有者）、
  貼網址附歌詞放進資料夾 → 下載（容器內 yt-dlp + deno）→ 去人聲 → 對時 → NVENC 燒錄（映像內建字型）→ 檢查、
  確認 → 匯出（硬連結，同一個 inode）、資料備份 commit + push、SSH 金鑰權限、`docker stop` 1.2 秒
- arm64 映像用 buildx 建置、在模擬下啟動並通過 /healthz（Mac 上實際部署由使用者執行）
- 實測發現並處理：空的 named volume 會沿用映像的擁有者（只影響測試；bind mount 不會）；PUID 寫不進曲庫時入口腳本說明原因；
  data/ repo 沒設定 git 身分時 commit 失敗 → 映像設系統層級的預設身分（repo 自己的設定優先）
- 映像大小：下載約 270 MB、解壓約 660 MB（ffmpeg / ffprobe 靜態版佔 280 MB）

