# AGENTS.md — 給 AI 助手的專案說明

這份文件給協助開發這個專案的 AI 看：架構、不能破壞的規則、使用者的偏好與測試方法。
使用方式（給人看的）見 [README.md](README.md)；設計細節見 [docs/](docs/README.md)，**動手前先讀相關的那幾份**。
進行中的工作在 [tasks/todo.md](tasks/todo.md)，踩過的坑在 [tasks/lessons.md](tasks/lessons.md)。

## 專案在做什麼

把 YouTube 影片（或自己的影音檔）做成 KTV 伴唱帶：**下載 → 去人聲 → 逐字對時 → 燒上逐字變色的字幕 → 對時檢查**，
在網頁上管理曲庫、編輯歌詞、邊看邊調時間，確認沒問題的歌匯出到給卡拉 OK 軟體用的資料夾。

兩個程式：

| | NAS 伺服器 `nas/`（Go + Svelte） | AI 伺服器 `worker/`（Python 套件 `kara_worker`） |
| --- | --- | --- |
| 跑在 | 放曲庫的 Mac mini（OrbStack container） | 有 NVIDIA 顯示卡的電腦（container 或 Windows 直接執行） |
| 職責 | 網頁 UI、REST API + SSE、曲庫與紀錄、下載（yt-dlp）、手動放入、狀態判斷、排程、匯出、資料備份、字型、讀音快取 | 去人聲（Demucs）、對時（Whisper + CTC）、對時檢查、假名、產生 ASS 與燒錄（NVENC） |

AI 伺服器主動連到 NAS 領任務（`/worker/v1`），沒有狀態：曲庫、檔案、「要不要重做」的判斷都只在 NAS。

## 架構地圖

| 位置 | 內容 |
| --- | --- |
| `nas/cmd/kara-nas` | 進入點：伺服器、`openapi`（產生 `docs/openapi.json`）、`restore`（從資料備份重建曲庫） |
| `nas/internal/app` | 把各模組組起來：歌曲狀態（`status.go`）、使用者動作（`actions.go`）、檔案監看、收尾 |
| `nas/internal/api` | REST API（huma，`ops.go` 定義端點，OpenAPI 從程式碼產生）、SSE、內嵌的前端 |
| `nas/internal/planner` | 每首歌每個階段的狀態與指紋（data.md「狀態判斷」「指紋」） |
| `nas/internal/pipeline`、`jobs`、`scheduler` | 處理步驟、使用者層級的工作佇列、AI 任務的派工與租約 |
| `nas/internal/lyrics`、`readings`、`titles`、`timing`、`library` | 歌詞、假名、標題辨識、時間調整、曲庫結構（純規則，有規格測試） |
| `nas/internal/download`、`inbox`、`export`、`backup`、`restore` | 下載、手動放入、匯出、資料備份、還原 |
| `nas/testdata/spec/` | 規格資料（輸入與應有的結果）。**規則要改時，程式和規格資料一起改** |
| `nas/web/` | Svelte 5 前端；API 型別由 `npm run types` 從 `docs/openapi.json` 產生 |
| `worker/src/kara_worker/` | `client.py`（協定）、`tasks.py`（任務）、`separate`、`align`、`qa`、`reading`、`subtitles`（演算法） |
| `versions.json` | 兩邊共用的演算法 / 協定版本；不同時 NAS 拒絕那台 worker |
| `deploy/` | NAS 與 AI 的 Dockerfile、compose；CI（`.github/workflows/images.yml`）自動建置推到 ghcr.io |

## 不能破壞的規則

- **影片、伴奏、伴唱帶、歌詞（含對時檔與字幕）都有版權**：只能在使用者的曲庫與私人的資料 repo，絕不進這個公開的 repo。
  程式碼、註解、測試資料、文件裡的範例歌詞與標題要用自己編的
- 伴奏的輸出格式與輸入相同；去人聲保留原影像軌、不重新編碼
- 用指紋判斷每個階段是否做過，重複執行只處理新的或有變動的；紀錄不記絕對路徑和修改時間
- 調高 `versions.json` 的 `align` 會讓**所有**歌整首重新對時、使用者手動調整的時間被取代 —— 除非使用者同意，不要調。
  其他版本號（`render`、`qa`、`reading`、`separate`、`protocol`）改了演算法、樣式或協定就要調
- 只匯出「已確認」的歌，而且只在使用者按「匯出」時同步；只需重燒的更新不影響確認（data.md「確認與匯出」）
- 長時間處理要可以取消，子程序要確實結束（NAS 用 `nas/internal/proc`）；關閉程式要乾淨
- 檔名、歌名辨識、語言、讀音：只做規則與手動操作，**不用 LLM、不自動判斷語言**（使用者明確要求）
- worker 的結果要穩定：改 worker 的程式但不打算改變結果時，用 docs/deploy.md「worker 的 GPU 回歸比對」確認逐字相同

## 使用者的偏好

- 一律用**繁體中文（台灣）**溝通與撰寫介面文字；用字白話但不要太直白（例如「去人聲」「製作伴唱帶」）
- 在使用者的電腦（PC、Mac）上**安裝任何東西前先問**；開發 container 內的工具可以直接安裝或更新
- 實際部署（Mac 的 NAS、PC 的 AI 伺服器）由使用者自己執行
- 介面：簡約、亮暗雙色；會啟動處理的按鈕（藍色外框）與只開畫面的連結要明顯區分
- 想討論方案時使用者會說「先不要動工」，這時只討論、不改程式
- 手動調時間只移單句；整段偏掉交給「AI 重對這句及之後全部」
- 文件保持精簡：README 只放架構、部署與基本用法，細節寫在 `docs/`

## 開發與測試

開發環境（DooD container、GPU、各種工具的版本）與測試方法見 [docs/deploy.md](docs/deploy.md)。

```sh
go vet ./... && go test -race ./...                       # NAS（含假的 worker 跑完整條流程）
cd nas/web && npm run check && npm run build              # 前端
cd worker && pytest && ruff check                         # worker（不需要模型的部分）
```

- **不要動使用者的曲庫**：測試用暫存的曲庫（`kara-nas --library <暫存> --init --listen :8799`），
  `KARA_CHANGE_DELAY=3s` 可以縮短「變動後收尾」的等待
- UI 的驗證：用無頭瀏覽器（Playwright）實際操作、截圖確認，而不只是看程式碼
- 對時品質要用真實的歌驗證（公有領域的歌，檔案放在 repo 外面）：看句首、零長度字、句內大間隔，必要時和獨立聽寫比對；
  對時演算法的修改要同時確認現有的歌不會變差、人為製造的錯誤能被抓到

## 常見工作

- **加新的 API**：`nas/internal/api/ops.go`（欄位寫 `doc`，錯誤的 `detail` 是給人看的繁中說明）→
  `go run ./nas/cmd/kara-nas openapi > docs/openapi.json` → `cd nas/web && npm run types`（CI 會檢查 openapi.json 是否過期）
- **改對時演算法**：`worker/src/kara_worker/align.py`；先想清楚要不要調 `versions.json` 的 `align`（見上方規則）
- **加語言**：`nas/internal/song/song.go` 的 `Languages`、`nas/internal/fonts` 的 `Defaults`、
  worker 的 `reading.split`、前端 `nas/web/src/lib/util.ts` 的 `LANGUAGE_NAMES`
- **標題畫面**：NAS 的 `planner`（`TitleCard`：[歌名, 演唱者(, 備註)]）決定內容，worker 的 `subtitles.build` 畫出來
- **改字幕樣式**：worker 的 `subtitles.Style`；要調 `versions.json` 的 `render`（所有伴唱帶變成只需重燒）
- **待辦**：`tasks/todo.md`；完成或新增功能後同步更新
