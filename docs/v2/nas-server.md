# NAS 伺服器（Go）

## 目標

- **閒置時幾乎不用資源**：常駐記憶體目標 < 40 MB；閒置時**不碰硬碟**（不輪詢掃描，NAS 硬碟才能休眠）
- 單一執行檔：前端打包後 `go:embed` 進去；只依賴外部的 `ffmpeg` / `ffprobe`、`yt-dlp`、`deno`、`git`
- 第三方 Go 套件盡量少：建議只用 huma v2（REST + OpenAPI）、fsnotify（檔案監看）；路由用標準函式庫（Go 1.22 起的 `net/http` pattern）
- 不需要資料庫：資料就是 data.md 定義的 JSON 檔，啟動時讀進記憶體

## 啟動參數

```
kara-nas --library /Volumes/Media/kara [--data <路徑>] [--listen :8765] [--worker-token …]
         [--tools <放 yt-dlp、deno 的資料夾>] [--open-browser]
kara-nas openapi            # 輸出 OpenAPI 規格（docs/openapi.json，CI 檢查是否過期）
kara-nas restore [--make]   # 從資料備份重建曲庫（v1 scripts/restore.py）
```

每個參數都有對應的環境變數（`KARA_LIBRARY`、`KARA_DATA`、`KARA_LISTEN`、`KARA_WORKER_TOKEN`、`KARA_TOOLS`），container 用。
預設監聽所有網路介面（NAS 的用途就是給區域網路用，不需要登入，決策 10）。

關閉（SIGINT / SIGTERM）：停止接新請求 → 取消下載（yt-dlp 子程序終止）與 NAS 本機的 ffmpeg → 把排隊中的工作寫到磁碟 → 1 秒內結束。
進行中的 AI 任務不等，worker 下一次心跳會收到 404 自己丟掉。

## 模組（nas/internal/）

各模組對應的 v1 程式，**行為以 v1 程式碼為準**。純規則的部分要通過黃金測試（見 deploy.md）：

| 模組 | 對應 v1 | 黃金測試 |
| --- | --- | --- |
| `lyrics` | `songtool/lyrics.py`（parse、serialize、paren_readings、paren_to_ruby、detect_language）；`ui/server.py` 的 `_from_plain`（用 difflib 對應新舊句子） | ✔ 要逐字相同。`_from_plain` 用到 Python `difflib.SequenceMatcher(autojunk=False).get_opcodes()`，Go 要實作同樣的演算法 |
| `titles` | `songtool/titles.py` | ✔ |
| `library` | `songtool/catalog.py`（place 的編號規則、刪資料夾時內容移到上一層、display_info） | ✔ |
| `export` | `songtool/export.py`（檔名規則、同名編號、非法字元換成全形底線） | ✔ |
| `timing` | `songtool/karaoke.py` 的 `shift_timing`（連鎖往前 / 往後推、壓縮）、`retime` 的套用部分（上一句尾音截到 anchor） | ✔ |
| `planner` | `karaoke.status`、`title_card`、`burn_translations`、`language_of`、`subtitle_ratio` 的語意 + data.md 的新指紋 | 用情境測試 |
| `download` | `songtool/download.py` | 用假的 yt-dlp 測試 |
| `inbox` | `songtool/local.py` | ✔（id 演算法） |
| `backup` | `songtool/backup.py` | — |
| `scheduler` | `songtool/jobs.py` | 用假的 worker 測試 |

## 下載

- 用 yt-dlp 官方的單一執行檔（macOS：`yt-dlp_macos`；Linux：`yt-dlp_linux` / `yt-dlp_linux_aarch64`），放 `--tools`。
  它內含 Python，**NAS 上不必安裝 Python**。需要 deno（解 YouTube 的 JS）與 ffmpeg（合併影音）
- 流程：
  1. `yt-dlp -J --no-playlist <網址>` 取得資訊 → 算出歌曲 id → 已經有這首就略過（同 v1）
  2. 下載到 `songs/<id>/.downloading/`：格式同 v1（`VIDEO_FORMAT` 優先 H.264 + AAC，合併成 mp4；只要音訊時用 `AUDIO_FORMAT`，不轉檔）；
     用 `--newline --progress-template` 解析進度；取消時終止子程序（`.part` 留著下次續傳）
  3. 完成後搬成 `source.<ext>`，寫 song.json（含 sha256、ffprobe 的寬高）
- 同時最多下載 2 首（同 v1）
- 啟動時替舊紀錄補抓歌曲資訊（v1 `refresh_metadata`，`meta_version`）
- Q10：yt-dlp 自動更新：啟動時與每天檢查一次。更新前把目前的執行檔留成 `yt-dlp.prev`；UI 的「系統」顯示版本，
  可以一鍵退回上一版（新版下載失敗時用）

## 手動放入（inbox）

- 監看 `<library>/inbox/`（fsnotify；macOS 是 kqueue），**不輪詢**
- 規則同 v1 `local.py`：大小 5 秒沒變才匯入；略過 `.part`、`.ytdl`、`.temp`、`.fNNN.ext`；id = `local-` + sha1(`檔名:大小`)[:8]，重複放入會略過並記錄
- 直接放在 inbox 的檔案、或放在 inbox 子資料夾裡的第一個影音檔都可以；匯入時**搬到** `songs/<id>/source.<ext>`（原檔名記在 `original_name`），子資料夾變空就刪掉
- 歌名與歌手從檔名辨識（同 v1：「歌手 - 歌名.mp4」最準）

## 排程器

### 工作（job）與任務（task）

- **工作**：使用者層級的要求，例如「這 12 首製作伴唱帶」。v1 是一首歌一件工作，v2 一樣：批次操作建立多件工作
- 一件工作 = 一首歌 + 步驟（`download`、`separate`、`karaoke`、`check`、`retime`）+ 選項（`force`、`realign`、`line`、`mode`、`targets`）
- 工作執行時一步一步問 planner「下一個動作是什麼」：
  - NAS 本機動作（下載、抽音軌、封裝、轉 speech.wav、套用結果）在 NAS 的 goroutine 做；匯出由使用者按「匯出」時才做
  - AI 動作建立**任務**放進任務佇列，等 worker 領走；完成後套用結果，再問下一步
- **同一首歌同時只有一件工作在跑**（後來的排隊等前一件結束），所以不會有兩台 worker 同時改同一首歌
- 不同首歌的工作同時進行：下載最多 2 首；NAS 本機的 ffmpeg 動作最多 2 個；AI 任務由有幾台 worker 決定

### 優先順序（Q3）

| 等級 | 任務 | 通道 |
| --- | --- | --- |
| 即時 | `reading` | interactive |
| 互動 | 播放畫面的 AI 重對（`align_from`、`align_line`）、單獨的對時檢查、使用者在播放畫面等待的重新燒錄 | heavy |
| 批次 | 下載後一路製作、批次製作 / 重新處理 | heavy |

- 同一等級先到先做
- **已經在跑的任務不會被中斷**：等級只決定 worker 下一次 lease 時拿到哪一件
- 派工（worker 來 lease 時）：在這個 worker 能做的任務中取最高等級；同等級裡**優先選輸入檔已在這台快取裡的**，
  但等超過 2 分鐘的任務不再挑快取（避免一直被跳過）
- worker 版本不同、被停用、不支援這個任務種類（例如只開 interactive 的 worker）就不派

### 持久化

v1 的佇列只在記憶體，重新啟動就清空。v2 把**還沒結束的工作**和最近的紀錄（最多 200 件、7 天）寫到 `<library>/cache/jobs.json`，
重新啟動後繼續排隊；執行到一半的本機動作重做（每一步都看紀錄檔判斷，重做是安全的）。

### 收尾

同 v1：
- 佇列清空（所有工作都結束）時收尾一次
- 曲庫有變動（改歌名、資料夾、歌詞、時間…）後安靜 30 秒收尾一次（連續操作只跑一次）

收尾 = 資料備份（寫 `data/`，有變動就 `git commit` + `git push`；push 失敗只記錄）。收尾的紀錄顯示在處理佇列的「收尾」。

Q5：v1 的收尾 hook（`hooks/on_idle.ps1`）**不移植**。它原本用來把匯出同步到 NAS，v2 的匯出本來就在 NAS 上。

## REST API（`/api/v1`）

網頁前端和外部服務**共用同一套 API**（不再分內部 `/api` 和公開 `/api/v1`）。用 huma 定義，OpenAPI 從程式碼產生，
每個欄位都寫說明。慣例沿用 v1：一首歌一個 id；GET 讀、POST 建立、PATCH 部分更新、PUT 取代、DELETE 刪除 / 取消；
花時間的處理都是工作（回 202 + `Location`）；錯誤一律 `{"detail": "說明"}`（RFC 9457 的 problem+json 也可以，但 detail 必須是給人看的繁中說明）。

| 端點 | 用途 |
| --- | --- |
| `GET /library` | 前端初次載入：資料夾、所有歌的摘要（含各階段狀態、疑慮數、目前的工作）、設定、worker 清單、工作清單 |
| `GET /songs` · `GET /songs/{id}` | 歌曲摘要 / 詳細（含 export 檔名、可播放的媒體清單） |
| `POST /songs` | 新增：`{url, folder, audio_only, lyrics, make}` → 下載工作（`make=true` 一路做到伴唱帶） |
| `PATCH /songs/{id}` | 修改 info（歌名、演唱者、語言、備註、連結、翻譯、targets） |
| `PUT /songs/{id}/approval` | 已確認 / 取消確認 |
| `GET /songs/{id}/lyrics?view=doc\|file\|annotated\|plain` · `PUT /songs/{id}/lyrics` | 歌詞的四種檢視（同 v1 `_lyrics_views`）；需要自動讀音的句子還沒算好時，回應標示 `pending_readings` |
| `POST /lyrics/convert` | 文字 ↔ 結構互轉（同 v1 `/api/lyrics/convert`），含括號讀音轉換 |
| `GET /songs/{id}/timing` · `PATCH /songs/{id}/timing/lines/{n}` | 每句時間；移動單句（`{delta}` 或 `{start}`），回傳一起被推動的句子 |
| `PATCH /songs/{id}/lines/{n}` | 播放畫面直接改一句的文字或演唱者（v1 的行為：改文字之後要重新對時） |
| `GET /songs/{id}/qa` | 對時檢查結果（只列有疑慮的句子） |
| `GET /media/{id}/{file}` | 媒體檔（支援 Range，給 `<video>` 播放與拖曳） |
| `GET/POST /folders` · `PATCH/DELETE /folders/{id}` | 資料夾 |
| `POST /library/place` | 拖曳：把歌或資料夾放到某個資料夾、排在某個項目前面（v1 `/api/place`、`/api/songs/move` 合併） |
| `POST /jobs` | 建立工作：`{songs: [id…], steps, force, realign, line, mode}`；批次時回傳建立了哪些、略過哪些（同 v1 前端的「略過 N 首」規則改到後端） |
| `GET /jobs` · `GET /jobs/{id}` · `GET /jobs/{id}/log?offset=` · `DELETE /jobs/{id}` | 工作清單、詳細、紀錄、取消 |
| `GET /workers` · `PATCH /workers/{name}` | AI 伺服器清單（名稱、GPU、版本是否相符、目前任務、最後連線時間）；停用 / 啟用 |
| `GET/PATCH /settings` | 全域設定：字幕大小、每種語言的字型 |
| `GET /fonts` · `GET /fonts/{sha256}` | 字型清單、字型檔（前端預覽用 `@font-face`） |
| `GET /system` | 版本（NAS、versions.json）、磁碟用量、yt-dlp 版本、資料備份狀態 |
| `POST /export` | 匯出：把「已確認」的伴唱帶同步到匯出資料夾（只在使用者按的時候） |
| `GET /events` | Server-Sent Events，見下 |

v1 的「開啟資料夾」（`os.startfile`）在 NAS 上沒有意義，改成在畫面上顯示路徑（可以複製），例如 SMB 分享路徑。

### 即時事件（取代 v1 每 1.5 秒輪詢）

`GET /api/v1/events`（SSE），事件：

| 事件 | 資料 |
| --- | --- |
| `song` | 一首歌的摘要（狀態有變時） |
| `library` | 資料夾或排序有變（前端重抓 `/library`） |
| `job` | 工作摘要（狀態、階段、進度） |
| `job.log` | `{id, lines}` 新的紀錄行 |
| `worker` | worker 上線、離線、開始 / 結束任務 |
| `readings` | `{song}`：這首歌補上了假名，編輯器重新取得 |
| `settings` | 設定有變 |

連線時先送一個 `hello`（含目前的版本號）；斷線重連後前端重抓 `/library`。每 25 秒送註解行保持連線。

## 給 AI 的端點（`/worker/v1`）

見 [worker-protocol.md](worker-protocol.md)。不放進 OpenAPI 規格（或放在另一份規格），因為只有 worker 使用。

## 資源檢查清單

- [ ] 閒置時 RSS < 40 MB（`ps -o rss`）
- [ ] 閒置 10 分鐘沒有任何檔案讀寫（macOS `fs_usage -f filesys kara-nas`）
- [ ] 前端開著、沒有變動時沒有 API 請求（只有 SSE 心跳）
- [ ] 上千首歌的曲庫，`/library` 回應 < 100 ms
