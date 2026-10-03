# kara-creator v2 重構規格

> 狀態：**實作中**（2026-10-03 在 Windows 開發機上與使用者討論後寫成；同日在 container 內定案 Q1–Q15 並開工）。
> 開工前先讀完本目錄。規格沒寫到、或之後新增的「待討論」項目，先和使用者討論，不要自行決定。
> v1 不再加功能（使用者決定直接做 v2），只當作 v2 行為的參考實作與黃金測試的答案來源。

## 目標

把專案拆成兩個可以分開部署的程式，並且能在 container 執行：

| | NAS 伺服器（原「歌曲伺服器 / UI」） | AI 伺服器 |
| --- | --- | --- |
| 語言 | **Go**（後端）+ **Svelte**（前端），盡可能輕量 | **Python**（沿用現有的 Demucs / Whisper / CTC 程式） |
| 跑在 | 使用者的 NAS：**Mac mini M1**（arm64，macOS） | 有 NVIDIA 顯示卡的電腦（現在是 Windows + RTX 4070 Ti SUPER） |
| 職責 | 網頁 UI（只負責瀏覽、操作）、曲庫、下載、**排程**、紀錄檔與狀態判斷、匯出、資料備份、字型檔、讀音快取 | 去人聲、對時、對時檢查、**假名 / 讀音**、**產生 ASS 字幕與燒錄** |
| 執行方式 | container 映像（Mac 上用已經在跑的 OrbStack；arm64 / amd64） | `ai.ps1`（只執行 Python）或 container 映像（CUDA） |
| 數量 | 一台 | **很多台**，都向同一台 NAS 領工作 |

## 已定案的決策（2026-10-03）

1. NAS 伺服器改用 Go + Svelte，盡可能輕量；網頁 UI 只負責瀏覽和操作，不做任何重運算
2. **AI 伺服器主動向 NAS 領工作**（pull）。NAS 不需要知道 AI 的位址，可以接很多台 AI
3. AI 伺服器**一台只服務一台 NAS**
4. AI 伺服器開**兩個通道**：重工作（GPU，一次一件）與即時工作（假名 / 讀音，CPU，可以和重工作同時跑）
5. **假名 / 讀音交給 AI 伺服器**算；**NAS 做快取**，PC 關機時看過的歌詞照樣有假名
6. **字型檔放在 NAS**。AI 伺服器缺字型時向 NAS 索取；預設字型用開源字型（Noto Sans CJK），也可以掛載自訂字型
7. **燒錄在 AI 伺服器**（NVENC）。NAS 不需要字型，也不量字寬、不產生 ASS
8. **下載還是在 NAS 上**（yt-dlp 只用網路）
9. 紀錄檔**換新格式**，不依賴絕對路徑和修改時間；舊資料用一次性的搬遷工具轉換
10. 不需要登入，全部只在區域網路使用
11. 沒有外部服務在用 REST API：**改成最佳設計**，網頁前端和外部服務共用同一套 API
12. `ai.ps1` 只負責執行 Python；AI 伺服器同時支援本機執行和 container

## 文件

| 檔案 | 內容 |
| --- | --- |
| [architecture.md](architecture.md) | 整體架構、repo 目錄結構、元件職責、各種流程（製作伴唱帶、AI 重對、編輯歌詞）怎麼在兩邊之間跑 |
| [data.md](data.md) | NAS 上的資料夾結構、新的紀錄格式、指紋演算法、狀態判斷、搬遷工具 |
| [worker-protocol.md](worker-protocol.md) | NAS ↔ AI 伺服器協定：領工作、租約、檔案、字型、版本、取消、兩個通道、各種工作的參數與結果 |
| [nas-server.md](nas-server.md) | Go 伺服器：模組、排程器、下載、REST API、即時事件、匯出、備份 |
| [frontend.md](frontend.md) | Svelte 前端：畫面與功能清單（要和現有 UI 功能一致）、和 API 的對應 |
| [deploy.md](deploy.md) | container 映像（Mac 用 OrbStack）、開發環境、測試策略 |

## 施工順序

每一階段做完都要能用，出問題可以退回。現有的 Python 版（`ui/`、`songtool/`）在 v2 驗證完之前**保留可用**，
它也是 Go 版行為的參考實作（黃金測試的答案來源）。

| 階段 | 內容 | 完成條件 |
| --- | --- | --- |
| 0 | repo 結構、`versions.json`（兩邊共用的版本）、CI 骨架、假的 AI worker（測試用） | `go build`、`npm run build`、`pytest` 都能跑 |
| 1 | **Go NAS 核心**：資料格式、曲庫、歌詞解析、標題辨識、下載、手動放入、狀態判斷、排程器、worker 協定（NAS 端）、匯出、備份、REST API + 事件 | 用假的 worker 跑完「下載 → 去人聲 → 對時 → 燒錄 → 檢查」整條流程；歌詞解析、標題辨識通過黃金測試 |
| 2 | **Python AI worker**：pull 協定、兩個通道、各種工作（含 `reading`、`render`）、字型索取、快取 | 真的 GPU 上和 NAS 串起來；同一首歌的對時、檢查結果和 v1 逐字相同 |
| 3 | **Svelte 前端**：功能和現有 UI 一致（見 frontend.md 的清單） | 清單逐項打勾；無頭瀏覽器截圖驗證 |
| 4 | **搬遷工具**（Python，在舊資料所在的 Windows 上執行）+ 試算報告 | 試算報告逐首列出搬遷前後狀態，使用者確認 |
| 5 | **打包**：NAS 映像（arm64 / amd64）、AI 映像（CUDA）、compose 範例 | 在 Mac mini 上實際跑起來 |
| 6 | **正式搬家**，移除 v1 的 `ui/` 與只有 v1 用到的 Python 模組，更新 README / AGENTS | 使用者確認 |

## 不能破壞的規則（沿用 AGENTS.md，v2 也適用）

- 影片、伴奏、伴唱帶、歌詞（含對時檔與字幕）都有版權：只能進私人的 `data/` repo，絕不進公開的程式 repo。
  程式碼、測試資料、文件裡的範例歌詞要用自己編的句子
- 伴奏的輸出格式與輸入相同；去人聲保留原影像軌、不重新編碼
- 每個階段寫紀錄檔判斷是否做過，重複執行只處理新的或有變動的
- 對時方法的版本（`versions.json` 的 `align`，即 v1 的 `align.VERSION`）調高會讓所有歌整首重新對時、手動調整被取代：
  **除非使用者同意，不要調**
- 長時間處理要可以取消，子程序要確實結束；關閉程式要乾淨
- 檔名、歌名辨識、語言、讀音：只做規則與手動操作，**不用 LLM、不自動判斷語言**
- 安裝任何套件或工具前先問使用者
- 介面文字一律繁體中文（台灣），用字白話但不要太直白

## Q1–Q15 的結論（2026-10-03 定案）

| # | 問題 | 結論 | 寫在 |
| --- | --- | --- | --- |
| Q1 | Mac mini 上原生執行還是 container | **container**：Mac 上本來就在跑 OrbStack，不必另外付出虛擬機的成本。不做 launchd 安裝 | deploy.md |
| Q2 | 影片放哪顆硬碟 | **外接硬碟，APFS**。匯出依序試 clone（reflink）→ 硬連結 → 複製；container 內實際能用哪一種要在 Mac 上驗證 | data.md「匯出」 |
| Q3 | 排程優先順序 | 三級：即時（假名）> 互動（AI 重對、單獨檢查）> 批次。**已經在跑的任務不會被中斷**，等級只決定下一件派誰 | nas-server.md「排程器」 |
| Q4 | AI 連線要不要 token | 可選的共用 token，預設不設；每台 AI 自報名稱，UI 可以停用某台 | worker-protocol.md「身分」 |
| Q5 | 收尾 hook | **不需要，移除 hook 功能**（v2 的匯出本來就在 NAS 上，不必再同步）。收尾只剩資料備份 | nas-server.md「收尾」 |
| Q6 | 「已確認」綁內容還是成品 | 綁**內容**（換字型、改字幕大小不必重新確認） | data.md「指紋」 |
| Q7 | 人聲只存音訊 | 是，`vocals.flac`；伴奏維持和來源同格式 | data.md「去人聲」 |
| Q8 | 每首歌一個資料夾 | 是 | data.md |
| Q9 | Mac 上跑只開即時通道的 worker | 先不做；協定保留 `--channels interactive` | worker-protocol.md「通道」 |
| Q10 | yt-dlp 更新 | 啟動時與每天檢查一次，自動更新；**保留上一版**，UI 可以一鍵退回（yt-dlp 偶爾會出有問題的版本） | nas-server.md「下載」 |
| Q11 | 預設字型 | 日文 / 英文 Noto Sans CJK JP Bold、中文 / 台語 / 粵語 Noto Sans CJK TC Bold、韓文 Noto Sans CJK KR Bold | data.md「字型」 |
| Q12 | 搬家後自動全部重燒 | 不自動，使用者用批次「製作伴唱帶」 | 搬遷流程 |
| Q13 | 原曲＋字幕 | 保留在 API（`targets`），UI 先不做入口 | data.md、worker-protocol.md |
| Q14 | v1 的 CLI | 不保留；需要時用 REST API | nas-server.md |
| Q15 | 保留 Aegisub 手改的 `.ass` | 保留：NAS 偵測到 `.ass` 被改過，燒錄時改送這份 `.ass` 給 AI | data.md、worker-protocol.md `render` |

## 給接手的 AI 助手

- 這份規格是和使用者逐項討論後寫成的。**規格沒寫到、或寫得模稜兩可的地方，先問使用者**，不要自己決定後默默實作
- 討論出新結論時，**同步更新這個目錄**（規格是之後工作的依據）
- 開發環境、測試資料、黃金測試的做法見 [deploy.md](deploy.md)
- v1 的行為細節以程式碼為準：`songtool/*.py`、`ui/server.py`、`ui/api_v1.py`、`ui/static/app.js`；使用方式見根目錄 README
