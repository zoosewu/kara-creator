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
│ 匯出（export/）   資料備份（data/ git）                                 │
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

## repo 目錄結構

```
go.mod                    Go module 在 repo 根目錄（go:embed 才拿得到根目錄的 versions.json）
versions.json             兩邊共用的演算法 / 協定版本（見 worker-protocol.md）
versions.go               內嵌 versions.json（package kara）
nas/                      NAS 伺服器（Go）
  cmd/kara-nas/           main：伺服器、openapi、restore
  cmd/fakeworker/         假的 AI worker（開發與測試用，回傳固定結果）
  internal/
    config/               參數與環境變數
    app/                  把各模組組起來：歌曲狀態、動作、檔案監看、收尾
    api/                  /api/v1 端點（huma，產生 OpenAPI）、SSE、內嵌的前端
    store/                資料夾結構、song.json、原子寫入、檔案鎖
    song/                 song.json 的結構
    library/              曲庫：資料夾、順序、歌曲資訊（library.json）
    lyrics/               歌詞解析 / 寫回、括號讀音、語言判斷、原始歌詞對應
    readings/             讀音（假名）快取、手動與自動讀音合併、歌詞編輯器的檢視
    titles/               從影片資訊猜歌名與演唱者
    timing/               alignment.json、手動調整時間、套用 AI 重對
    planner/              狀態判斷：每首歌每個階段的狀態，以及要排哪些任務
    pipeline/             去人聲、製作伴唱帶、檢查、AI 重對的步驟
    jobs/                 使用者層級的工作佇列（下載、AI 處理、收尾三條）
    scheduler/            AI 任務的佇列、優先順序、租約、派工；/worker/v1 端點
    workerproto/          worker 協定的資料結構（NAS 端與假的 worker 共用）
    fakeworker/           假的 AI worker 的實作
    download/             yt-dlp 執行檔包裝與自動更新
    inbox/                手動放入的檔案
    media/                ffprobe / ffmpeg 包裝
    fingerprint/          指紋（data.md）
    fonts/                字型目錄、預設字型
    export/               匯出（clone / 硬連結 / 複製）
    backup/               資料備份（git）
    restore/              從資料備份重建曲庫
    events/               SSE 事件的發佈
    proc/                 子程序（取消時整組結束）
    pystr/、difflib/      和 Python 相同語意的字串處理與 difflib（規格資料以 Python 語意定義）
  web/                    Svelte 前端（Vite），build 輸出到 nas/internal/api/dist 給 go:embed
  testdata/spec/          規格資料（歌詞解析、標題辨識、指紋、匯出檔名…的輸入與應有結果）
worker/                   AI 伺服器（Python 套件 kara_worker，見 worker/README.md）
  src/kara_worker/        協定、任務、演算法（去人聲、對時、檢查、讀音、字幕）
  tests/
  setup.ps1、worker.ps1   Windows 直接執行用
deploy/                   Dockerfile、compose、NAS container 的入口腳本
docs/                     本設計文件；openapi.json 由 Go 產生（CI 檢查是否過期）
```

## 流程

### 製作伴唱帶（新歌：網址 → 成品）

```
使用者按「製作伴唱帶」→ NAS 建立工作 J（步驟：download, separate, karaoke）
1. [NAS 下載通道] yt-dlp 下載到 songs/<id>/，寫 song.json 的 source（含檔案 sha256）
2. [NAS] planner：去人聲需要做 → NAS 抽音軌成 44.1kHz 立體聲 wav（暫存，算 sha256）→ 建立任務 separate
3. [AI 重通道] 領到 separate → 缺輸入檔就向 NAS 下載 → Demucs → 上傳 vocals.wav、no_vocals.wav → 完成
4. [NAS] 把 no_vocals 和來源影像軌封裝成伴奏（和來源同格式，`-c:v copy`），人聲存成 vocals.flac，寫紀錄
5. [NAS] planner：要對時（有歌詞）→ NAS 把人聲轉成 16kHz 單聲道 wav（speech.wav，算 sha256）→ 任務 align
6. [AI] align → 回傳 lines → [NAS] 寫 alignment.json
7. [NAS] planner：要產生成品 → 任務 render（輸入：伴奏影片、對時、歌詞句子與讀音、演唱者、翻譯、標題畫面、樣式、字型）
8. [AI] 缺字型就向 NAS 索取 → 量字寬、加假名、產生 ASS → 用 NVENC 燒錄（不行就 x264）→ 上傳 karaoke.ass、karaoke.mp4
9. [NAS] 寫成品紀錄 → 任務 qa（輸入 speech.wav、對時、歌詞）→ [AI] 回傳檢查結果 → [NAS] 寫 qa.json
10. [NAS] 工作 J 完成；佇列清空時收尾（資料備份）。使用者確認沒問題、按「匯出」後才放到 export/（data.md「確認與匯出」）
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

完全在 NAS 上算（timing 套件，規格測試見 timing_test.go），只改 alignment.json；之後成品變成只需重燒。

## 版本與相容

- `versions.json`（repo 根目錄）是唯一的來源：`protocol`、`align`、`qa`、`reading`、`render`、`separate`。
  Python 讀它；Go 在編譯時 `go:embed` 進去。改了演算法就改這裡的數字
- NAS 只派任務給版本完全相同的 AI；不同的 AI 在 UI 上標「版本不同，請更新」
- 指紋裡含有這些版本，所以版本一變，相關的成品就會顯示需更新（例如 `render` 改了只重燒，`align` 改了會整首重新對時——**改 `align` 前先問使用者**）
