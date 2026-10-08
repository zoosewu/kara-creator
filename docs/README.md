# kara-creator 設計文件

> 規格沒寫到、或寫得模稜兩可的地方，**先和使用者討論**，不要自行決定後默默實作；討論出新結論時同步更新這個目錄。

## 兩個程式

| | NAS 伺服器 | AI 伺服器（worker） |
| --- | --- | --- |
| 語言 | **Go**（後端）+ **Svelte**（前端），盡可能輕量 | **Python**（Demucs / Whisper / CTC） |
| 程式 | `nas/` | `worker/`（套件 `kara_worker`） |
| 跑在 | 放曲庫的那台：**Mac mini M1**（OrbStack container）或 Linux NAS | 有 NVIDIA 顯示卡的電腦（container 或 Windows 直接執行） |
| 職責 | 網頁 UI（只負責瀏覽、操作）、曲庫、下載、**排程**、紀錄與狀態判斷、匯出、資料備份、字型檔、讀音快取 | 去人聲、對時、對時檢查、**假名 / 讀音**、**產生 ASS 字幕與燒錄** |
| 數量 | 一台 | **可以很多台**，都向同一台 NAS 領工作 |

## 文件

| 檔案 | 內容 |
| --- | --- |
| [architecture.md](architecture.md) | 整體架構、repo 目錄結構、各種流程（製作伴唱帶、AI 重對、編輯歌詞）怎麼在兩邊之間跑 |
| [data.md](data.md) | 曲庫的資料夾結構、紀錄格式、指紋、狀態判斷、確認與匯出、資料備份 |
| [worker-protocol.md](worker-protocol.md) | NAS ↔ AI 伺服器協定：領工作、租約、檔案、字型、版本、取消、兩個通道、各種任務的參數與結果 |
| [nas-server.md](nas-server.md) | Go 伺服器：模組、排程器、下載、REST API、即時事件、匯出、備份 |
| [frontend.md](frontend.md) | Svelte 前端：畫面與功能清單、和 API 的對應 |
| [deploy.md](deploy.md) | container 映像、部署、開發環境、測試方法 |
| [openapi.json](openapi.json) | REST API 規格（由程式產生：`go run ./nas/cmd/kara-nas openapi`） |

## 設計決策

| 主題 | 決定 | 細節 |
| --- | --- | --- |
| 分工 | NAS 不做任何重運算；網頁 UI 只負責瀏覽和操作。下載在 NAS（只用網路），燒錄在 AI 伺服器（NVENC） | architecture.md |
| 領工作 | **AI 伺服器主動向 NAS 領工作**（pull），NAS 不必知道 AI 的位址；一台 AI 只服務一台 NAS | worker-protocol.md |
| 通道 | AI 伺服器開兩個通道：重工作（GPU，一次一件）與即時工作（假名，CPU，可以同時跑）；協定保留只開即時通道的 worker | worker-protocol.md「通道」 |
| 排程 | 三級：即時（假名）> 互動（AI 重對、單獨檢查）> 批次。**已經在跑的任務不中斷**，等級只決定下一件派誰 | nas-server.md「排程器」 |
| 假名 | AI 伺服器算，**NAS 快取**：AI 伺服器關機時，看過的歌詞照樣有假名 | data.md「讀音快取」 |
| 字型 | 字型檔放在 NAS（映像內建 Noto Sans CJK，也可以放自訂字型），AI 伺服器缺字型時向 NAS 要。預設：日文 / 英文 Noto Sans CJK JP Bold、中文 / 台語 / 粵語 TC Bold、韓文 KR Bold | data.md「字型」 |
| 紀錄 | 不依賴絕對路徑和修改時間，用內容的指紋判斷要不要重做；每首歌一個資料夾 | data.md「指紋」 |
| 去人聲 | 人聲存 `vocals.flac`；伴奏和來源同格式、保留原影像軌 | data.md |
| 確認與匯出 | **只匯出已確認的歌，而且只在按「匯出」時同步**。更新分成「只需重燒」（確認保留）和「需重新對時」（確認失效，但留下確認紀錄並標出改了哪幾句）。確認綁內容：換字型、改字幕大小不必重新確認 | data.md「確認與匯出」 |
| 手改字幕 | 用 Aegisub 改過的 `.ass` 會保留，燒錄時送這份給 AI 伺服器 | data.md、worker-protocol.md `render` |
| 曲庫位置 | 外接硬碟（APFS）。匯出依序試 clone → 硬連結 → 複製 | data.md「匯出」 |
| 部署 | 兩邊都是 container（Mac 用 OrbStack）；AI 伺服器也可以在 Windows 直接執行 | deploy.md |
| yt-dlp | 啟動時與每天檢查一次並自動更新；**保留上一版**，UI 可以一鍵退回 | nas-server.md「下載」 |
| 連線 | 只在區域網路使用，不需要登入；AI 連線可以設共用 token，UI 可以停用某台 AI | worker-protocol.md「身分」 |
| API | 網頁前端和外部服務共用同一套 REST API（OpenAPI） | nas-server.md |
| 收尾 | 佇列清空或曲庫有變動後只做資料備份（不提供 hook） | nas-server.md「收尾」 |
| 原曲＋字幕 | 保留在 API（`targets`），UI 先不做入口 | data.md、worker-protocol.md |

## 不能破壞的規則

- 影片、伴奏、伴唱帶、歌詞（含對時檔與字幕）都有版權：只能放在使用者的曲庫與私人的資料 repo，絕不進這個公開的 repo。
  程式碼、測試資料、文件裡的範例歌詞要用自己編的句子
- 伴奏的輸出格式與輸入相同；去人聲保留原影像軌、不重新編碼
- 每個階段用指紋判斷是否做過，重複執行只處理新的或有變動的
- 對時方法的版本（`versions.json` 的 `align`）調高會讓所有歌整首重新對時、手動調整被取代：**除非使用者同意，不要調**
- 長時間處理要可以取消，子程序要確實結束；關閉程式要乾淨
- 檔名、歌名辨識、語言、讀音：只做規則與手動操作，**不用 LLM、不自動判斷語言**
- 在使用者的電腦（PC、Mac）上安裝任何套件或工具前先問
- 介面文字一律繁體中文（台灣），用字白話但不要太直白
