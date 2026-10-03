# 架構

## 全貌

```
瀏覽器（手機 / 電腦）
   │  HTTP：/api/v1（REST）、/api/v1/events（SSE）、/media、/fonts、靜態網頁
   ▼
┌───────────────────── NAS 伺服器（Go，單一執行檔）─────────────────────┐
│ 網頁前端（Svelte，打包後用 go:embed 內嵌）                              │
│ REST API + SSE 事件                                                     │
│ 曲庫（資料夾、順序、歌名…）     歌詞解析 / 寫回      標題辨識（規則）    │
│ 下載（yt-dlp 執行檔，2 條）     手動放入（inbox/）   ffmpeg 輕量操作    │
│ 紀錄檔與狀態判斷（planner）     排程器（工作 → 任務 → 派給 AI）         │
│ worker 協定（AI 來領任務、取檔案、回報）   字型檔    讀音快取            │
│ 匯出（export/）   資料備份（data/ git）   收尾 hook                     │
└─────────────────────────────────────────────────────────────────────────┘
   ▲  HTTP：/worker/v1（AI 主動連線：領任務、取輸入檔、上傳結果、回報進度）
   │
┌─────────── AI 伺服器 #1（Python）──────────┐   ┌──── AI 伺服器 #2 …
│ 重工作通道（一次一件，GPU）                 │
│   separate / align / align_from /           │
│   align_line / qa / render                  │
│ 即時通道（CPU，可以同時跑）                 │
│   reading（假名、讀音）                     │
│ 本機快取：輸入檔（sha256）、字型、模型       │
└────────────────────────────────────────────┘
```

**原則**
- 曲庫、所有檔案、所有紀錄檔、所有「要不要重做」的判斷，**只在 NAS**
- AI 伺服器是**無狀態的運算節點**：拿到明確的任務參數與輸入檔，算出結果交回去，不知道曲庫長怎樣。
  它的本機快取只是為了少傳檔案，刪掉也不影響正確性
- NAS 上只做便宜的 ffmpeg 操作：探測媒體資訊、抽音軌、轉 16kHz 人聲、不重新編碼的封裝（`-c:v copy`）、yt-dlp 合併音訊和影像。
  **不重新編碼影片、不量字寬、不產生 ASS、不燒錄**

## repo 目錄結構（v2 完成後）

```
nas/                      Go module（NAS 伺服器）
  cmd/kara-nas/           main：參數、啟動、關閉
  internal/
    config/               路徑、參數、環境變數
    store/                資料夾結構、song.json、原子寫入、鎖
    library/              曲庫：資料夾、順序、歌曲資訊（v1 catalog.py）
    lyrics/               歌詞解析 / 寫回、括號讀音、語言判斷（v1 lyrics.py）
    titles/               從影片資訊猜歌名與演唱者（v1 titles.py）
    media/                ffprobe / ffmpeg 包裝
    download/             yt-dlp 執行檔包裝、download 資訊
    inbox/                手動放入的檔案（v1 local.py）
    fingerprint/          指紋演算法（data.md）
    planner/              狀態判斷：每首歌每個階段 done / outdated / pending，以及要排哪些任務
    scheduler/            工作、任務、優先順序、租約、派工
    workerapi/            /worker/v1 端點
    api/                  /api/v1 端點（huma，產生 OpenAPI）、SSE
    readings/             讀音快取（假名）
    fonts/                字型目錄、字型清單、預設字型
    export/               匯出（clone / 硬連結 / 複製）
    backup/               資料備份（git）
    hooks/                收尾 hook
  web/                    Svelte 前端（Vite），build 輸出到 nas/internal/api/dist 給 go:embed
ai/                       AI 伺服器（Python）
  worker.py               進入點：連上 NAS、兩個通道的迴圈
  tasks/                  各種任務的實作（呼叫 songtool 的演算法）
songtool/                 Python 演算法（AI 伺服器用；v1 的 UI 相關模組在階段 6 移除）
  align.py qa.py reading.py ass.py separate.py demucs_run.py lyrics.py（只留 Ruby 等 AI 需要的部分）…
migrate/                  搬遷工具（Python，在舊資料的 Windows 上執行）
versions.json             兩邊共用的演算法 / 協定版本（見 worker-protocol.md）
fonts/                    （不進 git）開發時的預設字型；安裝時下載
deploy/                   Dockerfile、compose 範例、launchd plist、安裝腳本
ai.ps1                    Windows：執行 ai/worker.py
docs/v2/                  本規格
docs/openapi.json         由 Go 產生的 OpenAPI 規格（`go run ./cmd/kara-nas openapi > …`，CI 檢查是否過期）
```

階段 6 移除：`ui/`、`ui.ps1`、`scripts/`（除了仍需要的）、`download.ps1`、`separate.ps1`、`karaoke.ps1`、
v1 才用到的 `songtool/` 模組（`catalog.py`、`download.py`、`local.py`、`export.py`、`backup.py`、`hooks.py`、`jobs.py`、`karaoke.py` 的 UI 部分、`library.py`、`titles.py`、`settings.py`、`manifest.py`、`net.py`、`ai.py`）。
移除前確認搬遷工具不再需要它們（搬遷工具可以固定在某個 git tag 上執行）。

## 流程

### 製作伴唱帶（新歌：網址 → 成品）

```
使用者按「製作伴唱帶」→ NAS 建立工作 J（步驟：download, separate, karaoke）
1. [NAS 下載通道] yt-dlp 下載到 songs/<id>/，寫 song.json 的 source（含檔案 sha256）
2. [NAS] planner：去人聲需要做 → NAS 抽音軌成 44.1kHz 立體聲 wav（暫存，算 sha256）→ 建立任務 separate
3. [AI 重通道] 領到 separate → 缺輸入檔就向 NAS 下載 → Demucs → 上傳 vocals.wav、no_vocals.wav → 完成
4. [NAS] 把 no_vocals 和來源影像軌封裝成伴奏（和來源同格式，`-c:v copy`），人聲存成 vocals.flac（Q7），寫紀錄
5. [NAS] planner：要對時（有歌詞）→ NAS 把人聲轉成 16kHz 單聲道 wav（speech.wav，算 sha256）→ 任務 align
6. [AI] align → 回傳 lines → [NAS] 寫 alignment.json
7. [NAS] planner：要產生成品 → 任務 render（輸入：伴奏影片、對時、歌詞句子與讀音、演唱者、翻譯、標題畫面、樣式、字型）
8. [AI] 缺字型就向 NAS 索取 → 量字寬、加假名、產生 ASS → 用 NVENC 燒錄（不行就 x264）→ 上傳 karaoke.ass、karaoke.mp4
9. [NAS] 寫成品紀錄 → 任務 qa（輸入 speech.wav、對時、歌詞）→ [AI] 回傳檢查結果 → [NAS] 寫 qa.json
10. [NAS] 匯出到 export/，工作 J 完成；佇列清空時收尾（備份、hook）
```

同一首歌的任務依序執行（前一個完成才建立下一個）；不同首歌的任務可以同時派給不同的 AI。
任何一步的輸入已經在 AI 的快取裡就不重傳（見 worker-protocol.md「檔案」）。

### AI 重對（播放畫面）

```
使用者在第 k 句按「AI 重對這句及之後全部」→ NAS 建立互動優先的工作
→ 任務 align_from（speech.wav、全部歌詞句子與讀音、first=k、anchor=第 k 句目前的開頭）
→ [AI] 回傳第 k 句之後的新結果 → [NAS] 套用到 alignment.json（之前的句子不動、上一句尾音截到 anchor），記 adjustments
→ SSE 通知前端更新畫面
```

「只重對這句」同理，用 `align_line`。

### 編輯歌詞（假名）

```
前端打開歌詞編輯器 → GET /api/v1/songs/{id}/lyrics?view=…
→ NAS 解析歌詞；需要自動讀音的句子先查讀音快取
   全部命中 → 直接回應
   有缺的   → 建立即時任務 reading（只放缺的句子）→ 等最多 3 秒
               AI 回來了 → 寫快取、回應
               沒有在線的 AI 或逾時 → 回應中標示「這幾句的假名稍後補上」，任務留在佇列；
                                       AI 回來算完後透過 SSE 通知前端重新取
```

「標註原文」轉回結構時要判斷哪些讀音是手動標的（和自動讀音相同的不算手動），也用同一份快取。
沒有自動讀音時（AI 離線），轉換時把所有讀音都當成**手動**會讓檔案多出自動讀音，所以要**拒絕轉換並提示稍後再試**，不能猜。

### 手動調整時間

完全在 NAS 上算（v1 `karaoke.shift_timing` 的邏輯，Go 移植），只改 alignment.json；之後成品變成需更新。

## 版本與相容

- `versions.json`（repo 根目錄）是唯一的來源：`protocol`、`align`、`qa`、`reading`、`render`、`separate`。
  Python 讀它；Go 在編譯時 `go:embed` 進去。改了演算法就改這裡的數字
- NAS 只派任務給版本完全相同的 AI；不同的 AI 在 UI 上標「版本不同，請更新」
- 指紋裡含有這些版本，所以版本一變，相關的成品就會顯示需更新（例如 `render` 改了只重燒，`align` 改了會整首重新對時——**改 `align` 前先問使用者**）
