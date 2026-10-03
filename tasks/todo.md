# v2 施工待辦

規格：[docs/v2/](../docs/v2/README.md)。Q1–Q15 已於 2026-10-03 定案（見 docs/v2/README.md）。

## 準備

- [x] Q1–Q15 結論寫回 docs/v2；移除 hook（Q5）
- [x] 開發環境：Go 1.27.1、Node 24 LTS、Python 3.14（`.venv-linux`，CPU 版 PyTorch 2.11）、Chromium（Playwright 已有）
- [ ] ffmpeg（最新靜態版，`~/.local/bin`）

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

- [ ] `tools/golden.py` + `lyrics`：parse / serialize / 括號讀音 / detect_language
- [ ] `difflib`：移植 Python `SequenceMatcher(autojunk=False).get_opcodes()` + `_from_plain`
- [ ] `titles`：標題辨識
- [ ] `fingerprint`：data.md 的指紋（Python 版也要，搬遷工具用）
- [ ] `store`：`song.json`、`library.json`、原子寫入、`--init`、外接硬碟沒掛上時拒絕啟動
- [ ] `library`：資料夾、順序（v1 `catalog.place`）、display_info
- [ ] `media`：ffprobe / ffmpeg 包裝（抽音軌、封裝、speech.wav、vocals.flac）
- [ ] `timing`：`shift_timing`、AI 重對結果的套用
- [ ] `readings`：讀音快取 + 手動 / 自動讀音合併（v1 `reading.furigana`）
- [ ] `planner`：狀態判斷、下一步
- [ ] `scheduler` + `workerapi`：工作、任務、優先順序、租約、改派、取消、持久化；用假的 worker 測
- [ ] `download`（假的 yt-dlp 測試）、`inbox`
- [ ] `export`：clone → 硬連結 → 複製
- [ ] `backup`、`restore`
- [ ] `fonts`：字型目錄、family 名稱
- [ ] `api`：huma REST + SSE + `openapi` 子命令
- [ ] 完成條件：假的 worker 跑完「下載 → 去人聲 → 對時 → 燒錄 → 檢查」；黃金測試通過

## 檢討

### 階段 0（2026-10-03）

- 完成條件已驗證：`go vet`、`go test -race`（含假的 worker 對 stub NAS 的全部任務種類、版本不符、取消、失敗）、
  `npm run check` + `npm run build`、`pytest` 都通過；`kara-nas` 實際啟動，`/healthz`、SPA、內嵌前端截圖、Ctrl+C 關閉都正常
- 和規格不同的地方（已寫回 docs/v2）：
  - `go.mod` 放在 repo 根目錄（go:embed 不能讀上層目錄的 versions.json）
  - `reading` 任務只回傳自動讀音、不收手動讀音（快取 key 才能和手動讀音無關），手動讀音由 NAS 合併

