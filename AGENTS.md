# AGENTS.md — 給 AI 助手的專案說明

這份文件給協助開發這個專案的 AI 看：架構、資料格式、不能破壞的規則、使用者的偏好與測試方法。
使用方式（給人看的）請見 [README.md](README.md)。

## 專案在做什麼

本機的 KTV 伴唱帶工作室（Windows、NVIDIA GPU）：

1. **下載**：yt-dlp（需要 deno 解 YouTube 的 JS）→ `output/downloads/<標題> (<id>)/`
2. **去人聲**：Demucs htdemucs 兩軌 → `output/separated/`。**輸出格式與輸入相同**（mp4 進就 mp4 出，影像軌直接複製）
3. **對時**：歌詞逐字對齊到人聲 → `output/karaoke/<資料夾>/alignment.json`
4. **字幕與燒錄**：ASS 字幕（兩行 KTV 版面、逐字填色 `\kf`、日文假名、男女合唱顏色）→ ffmpeg 燒進伴奏影片
5. **對時檢查**：本機 Whisper 獨立聽寫比對，標出可能不準的句子（`qa.json`）
6. **輸出**：依曲庫的資料夾結構，把成品硬連結到 `output/export/<資料夾>/歌手 - 歌名.mp4`

另外有網頁 UI（`ui/`）、REST API（`/api/v1`）、資料備份（`data/` 私人 repo）、收尾 hook。

## 架構地圖

| 位置 | 內容 |
| --- | --- |
| `songtool/config.py` | 所有路徑；環境變數 `SONG_OUTPUT_DIR`、`SONG_LYRICS_DIR`、`SONG_DATA_DIR`、`SONG_HOOKS_DIR` 可改（測試用） |
| `songtool/download.py` | yt-dlp 下載、`list_downloads()`（每個下載資料夾有 `download.json`） |
| `songtool/local.py` | 手動放進 `output/downloads/` 的檔案自動登記；id = `local-` + sha1(檔名:大小)[:8]（放回同一個檔案就是同一個 id） |
| `songtool/separate.py` + `demucs_run.py` | 去人聲；Demucs 以子程序執行（可取消），存檔用 soundfile（新版 torchaudio 存檔需要 torchcodec，刻意避開） |
| `songtool/lyrics.py` | 歌詞檔格式的解析 / 寫回（見下方）、語言判斷、括號讀音轉換 |
| `songtool/reading.py` | 一句歌詞 → 變色單位 + 羅馬字讀音（日文 MeCab、中文拼音、台語 / 粵語靠手動標註）；`furigana()` 給編輯器 |
| `songtool/align.py` | 對時：Whisper（stable-ts）定句 → 人聲音量修正 → CTC（torchaudio MMS_FA）逐音精修。台語 / 粵語（`CTC_ONLY`）不經 Whisper，整首 CTC |
| `songtool/qa.py` | 對時檢查（規則 + 分段聽寫比對；台語 / 粵語只做規則） |
| `songtool/ass.py` | ASS 字幕產生（字寬用 Pillow 量、換算 libass 比例） |
| `songtool/karaoke.py` | 串起對時 → 字幕 → 燒錄 → 檢查；手動平移時間 `shift_timing`、AI 重對 `retime` |
| `songtool/catalog.py` | 曲庫（`output/library.json`）：巢狀資料夾、順序、手動歌名 / 演唱者 / 語言、已確認（成品字幕的 sha1，成品變了就失效）、手動放入影片補上的連結（`link`）、備註（`note`，標題畫面第三行）、是否燒上翻譯（`translation`） |
| `songtool/settings.py` | 全域設定（`output/settings.json`）：字幕大小等；`karaoke.subtitle_ratio()` 算出實際字高比例，100% 時和預設樣式相同 |
| `songtool/titles.py` | 從 yt-dlp 資訊與影片標題猜歌名 / 演唱者（純規則，**不用 LLM**） |
| `songtool/export.py` | 同步 `output/export/`（硬連結，只管自己放的檔案） |
| `songtool/jobs.py` | 工作佇列：下載（2 條）與 AI 處理（1 條）分開；佇列清空時呼叫 `on_idle`；收尾工作 lane = `system` |
| `songtool/backup.py` | 資料備份到 `data/`（songs.json、lyrics、timing），commit + push |
| `songtool/hooks.py` | 收尾 hook（`hooks/on_idle.ps1`）、變動後延遲觸發的 Debouncer |
| `ui/server.py` | FastAPI：網頁 UI 用的 `/api/*`、媒體檔、收尾流程、變動追蹤 middleware |
| `ui/api_v1.py` | REST API v1（給外部服務）的端點；docstring 第一行是規格裡的摘要、其餘是說明 |
| `ui/api_schema.py` | REST API v1 的資料模型（規格的資料結構；欄位說明與範例會進規格） |
| `docs/openapi.json` | 由 `scripts/openapi.py` 從程式碼產生的 OpenAPI 規格，**不要手改** |
| `ui/static/` | 前端，原生 HTML / CSS / JS，不需打包 |
| `scripts/*.py` + `*.ps1` | CLI：download / separate / karaoke / backup / restore |

## 資料格式

**歌詞檔** `lyrics/<影片id>.txt`（UTF-8）：

```text
# title: 歌名              # 開頭：title / artist 是歌曲資訊，其他 # 開頭的行是註解
[男] 歌詞一句              一行 = 畫面上一句；行首 [男] [女] [合] 標演唱者
> 中文翻譯                 上一句的翻譯（不參與對時；燒進伴唱帶與否看曲庫的 translation 設定）
漢字{よみ}                 讀音：標在前面連續的漢字上（羅馬字讀音依音節數只標最後幾個字）
{原字|よみ}                明確指定範圍
```

只有手動指定的讀音會寫進檔案；自動判斷的讀音是執行時算的。台語、粵語沒有自動讀音，全部手動標（台羅 / 粵拼，聲調符號與數字會被去掉）。

**alignment.json**：`{"key": {...對時條件...}, "lines": [{"text", "start", "end", "words": [{"text", "start", "end"}]}], "adjustments": [...]}`。
`key` 包含歌詞 sha1、人聲檔大小與時間、模型、語言、`align.VERSION`；任一改變就整首重新對時。
手動調整與 AI 重對直接改 `lines` 並記在 `adjustments`；`karaoke.json` 的 `alignment_sha1` 不同時只重產字幕與燒錄。
`restored` 欄位表示從資料備份還原：歌詞、語言、方法相同就沿用（人聲重新分離過也不重對）。

**紀錄檔**：各階段完成後才寫的 JSON（`download.json`、`separate.json`、`karaoke.json`、`qa.json`），用來判斷「做過了沒」。

## 不能破壞的規則

- **影片、伴奏、伴唱帶、歌詞（含對時檔與字幕）都有版權**：只能進私人的 `data/` repo，絕不進公開的程式 repo。
  程式碼、註解、文件裡的範例歌詞要用自己編的句子
- 輸出格式與輸入相同；去人聲保留原影像軌、不重新編碼
- 每個階段寫紀錄檔判斷是否做過，重複執行只處理新的或有變動的
- 調高 `align.VERSION` 會讓**所有**歌在下次製作時整首重新對時、使用者手動調整的時間被取代 —— 除非使用者同意，不要調
- 長時間處理要可以取消（`should_stop`），子程序要確實結束；關閉程式（Ctrl+C）要乾淨
- 檔名、歌名辨識、語言、讀音：只做規則與手動操作，**不用 LLM、不自動判斷語言**（使用者明確要求）

## 使用者的偏好

- 一律用**繁體中文（台灣）**溝通與撰寫介面文字；用字白話但不要太直白（例如「去人聲」「製作伴唱帶」）
- **安裝任何套件或工具前先問**；不要擅自安裝
- 介面：簡約、亮暗雙色；會啟動處理的按鈕（藍色外框）與只開畫面的連結要明顯區分
- 想討論方案時使用者會說「先不要動工」，這時只討論、不改程式
- 手動調時間只移單句；整段偏掉交給「AI 重對這句及之後全部」

## 開發與測試

- 環境：Python 3.14 + PyTorch 2.11（CUDA 13.0），`.venv\Scripts\python.exe`。`setup.ps1` 會建好全部
- **不要動使用者的曲庫**：測試時用環境變數指到暫存資料夾，另開一個 port：

  ```powershell
  $env:SONG_OUTPUT_DIR = "<暫存>\output"; $env:SONG_LYRICS_DIR = "<暫存>\lyrics"
  $env:SONG_DATA_DIR = "<暫存>\data"; $env:SONG_HOOKS_DIR = "<暫存>\hooks"
  .\.venv\Scripts\python.exe ui\server.py --no-browser --port 8766
  ```

  `SONG_CHANGE_DELAY`（秒）可以縮短「變動後收尾」的等待時間
- UI 的驗證：用無頭瀏覽器（Edge + DevTools 協定）操作頁面、執行 JS、截圖確認，而不只是看程式碼
- 對時品質要用真實歌曲驗證：看 `alignment.json` 的句首、零長度字、句內大間隔，必要時和獨立聽寫比對
- 對時演算法的修改要同時確認：現有的歌不會變差、人為製造的錯誤能被抓到
- 使用者的 UI 可能正在執行（`ui.ps1 --lan`）：改後端後提醒重新啟動；不要擅自關掉使用者的程式
- 在 Windows 的 Bash 工具裡用 heredoc 執行 Python 時，`\n`、`\\` 等反斜線會被轉換 —— 含反斜線的程式碼改用檔案編輯工具
- 歌詞、對時等資料會自動備份到 `data/` 並 push：測試時務必用 `SONG_DATA_DIR` 指到暫存資料夾

## 常見工作

- **加新的 API**：網頁 UI 用的放 `ui/server.py`（不會出現在規格）；給外部服務的放 `ui/api_v1.py`，
  回應一定要有 `response_model`（在 `ui/api_schema.py` 定義，欄位寫 description）、可能的錯誤寫進 `responses`、
  docstring 寫摘要與說明（一首歌一個 id、PATCH 部分更新、長時間處理回 202 + Location）。
  改完執行 `python scripts/openapi.py` 更新 `docs/openapi.json`（`--check` 可檢查是否過期）
- **改對時演算法**：`songtool/align.py`；先想清楚要不要調 `VERSION`（見上方規則）
- **加語言**：`catalog.LANGUAGES`、`reading.split`、`ass.DEFAULT_FONTS`、前端 `LANGUAGE_NAMES`
- **標題畫面**：`karaoke.title_card()` 回傳 [歌名, 演唱者]（有備註時才加第三項，避免舊紀錄全部變成需更新），`ass.build()` 產生
- **改字幕樣式**：`songtool/ass.py` 的 `Style`；樣式 key 改變會讓伴唱帶顯示需更新
- **待辦**：`TODO.md`；完成或新增功能後同步更新它與 README
