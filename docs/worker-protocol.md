# NAS ↔ AI 伺服器協定

AI 伺服器（以下稱 worker）**主動連到 NAS**。NAS 不需要知道 worker 的位址，worker 那台也不必開防火牆。
所有端點都在 NAS 的 `/worker/v1` 底下，HTTP + JSON。

```
kara-worker --nas http://mac-mini.local:8765 --name pc-4070 [--token …] [--cache D:\kara-ai-cache]
.\worker\worker.ps1 --nas http://mac-mini.local:8765 --name pc-4070   # Windows 直接執行
```

container 用環境變數：`KARA_NAS`、`KARA_WORKER_NAME`、`KARA_WORKER_TOKEN`、`KARA_CACHE`、`KARA_CHANNELS`、`KARA_DEVICE`。
程式在 `worker/`（套件 `kara_worker`，結構見 worker/README.md）。

## 身分

- worker 啟動時自報 `name`（預設電腦名稱）和一個隨機的 `instance` id（每次啟動不同）
- 可選的共用 token：NAS 的 `--worker-token`（環境變數 `KARA_WORKER_TOKEN`）有設的話，worker 要帶 `Authorization: Bearer <token>`
- UI 上可以停用某台 worker（依 name）：停用的 worker 領不到任務

## 版本

`versions.json`（repo 根目錄，兩邊共用）：

```json
{ "protocol": 1, "separate": 1, "align": 7, "qa": 4, "reading": 1, "render": 1 }
```

- `align`：對時方法（`align.py`）改了就加一（**會讓所有歌整首重新對時、手動調整被取代，先問使用者**）
- `qa`：對時檢查的規則（`qa.py`）改了就加一（所有歌重新檢查）
- `reading`：讀音規則（MeCab 字典版本、`reading.py` 的規則）改了就加一
- `render`：ASS 產生（`subtitles.py`）或燒錄參數改了就加一（所有成品變成需更新，只重燒）
- `separate`：Demucs 模型或分離方法改了就加一
- worker 每次連線都帶上自己的版本；**和 NAS 不完全相同就拒絕**（HTTP 409，回應說明哪幾項不同），UI 顯示「版本不同，請更新」

## 端點

| 方法與路徑 | 用途 |
| --- | --- |
| `POST /worker/v1/hello` | 啟動時打招呼：name、instance、versions、硬體資訊（GPU 名稱、VRAM、是否有 NVENC）、支援的任務種類、快取裡已有的檔案（sha256 清單，最多最近 500 個）。回應 NAS 的版本、設定（心跳間隔、租約長度） |
| `POST /worker/v1/lease` | 領任務（long-poll，最多等 25 秒）。body：`{instance, channel: "heavy" \| "interactive", cached: [最近新增的 sha256…]}`。有任務回 200 + 任務；沒有回 204 |
| `POST /worker/v1/tasks/{id}/progress` | 心跳兼回報：`{instance, progress: 0–1 或 null, logs: ["…"]}`。回應 `{cancel: bool}`——NAS 要取消時在這裡告訴 worker |
| `GET /worker/v1/blobs/{sha256}` | 下載輸入檔（支援 Range，斷線可以續傳）。只能取「派給這個 worker 的任務」列出的輸入檔 |
| `PUT /worker/v1/tasks/{id}/files/{name}?instance=…` | 上傳結果檔；NAS 邊收邊算 sha256，回傳 sha256 與大小。只接受任務規定的檔名 |
| `POST /worker/v1/tasks/{id}/complete` | 完成：`{instance, result: {…}, files: {name: sha256}}`。NAS 核對檔案 sha256 後套用結果 |
| `POST /worker/v1/tasks/{id}/fail` | 失敗：`{instance, error: "給人看的繁中說明", retryable: bool}` |
| `GET /worker/v1/fonts/{sha256}` | 下載字型檔（worker 缺字型時索取） |
| `POST /worker/v1/bye` | 正常結束前通知（交還手上的任務，NAS 立刻改派） |

## 租約與故障

- 領到任務等於取得**租約**（預設 60 秒）。每次 progress 心跳延長租約；worker 至少每 10 秒送一次心跳（就算沒有新進度）
- 租約到期（worker 當機、斷線、關機）→ NAS 把任務放回佇列，記一筆「worker X 失聯，改派」到工作紀錄。同一個任務最多改派 2 次，再失敗就讓工作失敗
- `fail` 的 `retryable=true`（例如顯示卡記憶體不足、暫時的 I/O 錯誤）→ 改派給別台（或稍後同一台）；`false`（例如歌詞讀音無法對齊）→ 工作直接失敗並顯示原因
- worker 之後才送來的 progress / complete，如果任務已經改派給別人，NAS 回 409，worker 丟掉結果
- NAS 重新啟動：排隊中的工作記在磁碟（見 nas-server.md），進行中的任務放回佇列；worker 的 progress 會收到 404，丟掉手上的任務

## 取消

- 使用者取消 → NAS 把任務標成取消中，worker 下一次心跳收到 `cancel: true`
- worker 能中斷的就馬上中斷：Demucs 子程序、ffmpeg 燒錄直接終止；Whisper / CTC 對時做不到中途打斷，做完那一步丟掉結果
- **NAS 不等 worker**：取消後工作立刻顯示已取消，worker 那邊的任務在它下一次心跳或完成時收尾

## 通道（兩條迴圈）

worker 開兩條迴圈，各自 long-poll `lease`：

| 通道 | 任務 | 並行 | 用到 |
| --- | --- | --- | --- |
| `heavy` | separate、align、align_from、align_line、qa、render | 一次一件 | GPU（模型常駐在行程裡，不必每次重新載入） |
| `interactive` | reading | 可以和 heavy 同時跑；本身一次一件 | CPU（MeCab、pykakasi、pypinyin） |

MeCab tagger 不是執行緒安全的：interactive 迴圈用自己的 tagger 實例（或加鎖）。

協定和 worker 的參數保留 `--channels interactive`（只開即時通道，不需要 GPU 與 PyTorch），但**先不打包、不部署**到 Mac 上；有需要再做。

## 檔案

- 所有輸入檔用 **sha256** 識別。worker 本機快取在 `--cache`（預設：Windows `%LOCALAPPDATA%\kara-ai\cache`；container 的 `/cache`）
  - `blobs/<sha256>`：輸入檔，7 天沒用到就刪
  - `fonts/<sha256>`：字型檔，不自動刪
  - 模型（Whisper、MMS、Demucs）照各套件預設的快取位置；container 版掛 volume
- 任務裡列出每個輸入的 sha256 與大小；worker 先查快取，沒有才 `GET /blobs/{sha256}`，下載完驗證 sha256
- NAS 派工時**優先派給快取裡已經有輸入檔的 worker**（hello 和 lease 時回報的清單）。對時、檢查、AI 重對常用同一份 speech.wav，所以同一首歌的後續任務大多會落在同一台
- 結果檔上傳完、`complete` 被接受後，worker 不必保留結果

## 字型

- render 任務的參數帶字型的 `sha256`、`family`（ASS 裡的 Fontname）、`index`（.ttc 裡第幾個）
- worker 沒有這個字型就 `GET /worker/v1/fonts/{sha256}` 下載到 `fonts/`
- 燒錄時用 ffmpeg subtitles 濾鏡的 **`fontsdir=`** 指到「只放這個任務字型」的暫存資料夾，libass 一定用到這個字型檔，
  不受 worker 那台電腦安裝了什麼字型影響。多台 worker 燒出來的畫面才會一致
- 量字寬（決定長句怎麼拆、假名放哪裡）也用同一個字型檔

## 任務種類

所有任務的共同欄位：

```jsonc
{
  "id": "t_…", "kind": "align", "channel": "heavy", "priority": "interactive",
  "song": "dQw4w9WgXcQ",              // 只用來寫紀錄
  "inputs": { "audio": { "sha256": "…", "size": 123456 } },
  "params": { … },
  "outputs": ["…"]                    // render、separate 才有：允許上傳的檔名
}
```

| kind | inputs | params | result | 上傳的檔案 |
| --- | --- | --- | --- | --- |
| `separate` | `audio`：44.1kHz 立體聲 wav | `model`（htdemucs）、`stems`（2） | `{}` | `vocals.wav`、`no_vocals.wav` |
| `align` | `audio`：speech.wav（16kHz 單聲道） | `texts`、`rubies`（每句手動讀音 `[{start,end,reading}]`）、`language`、`model` | `{lines}` | — |
| `align_from` | `audio`：speech.wav | `texts`、`rubies`、`first`、`anchor`、`language`、`model` | `{lines}`：第 first 句（含）之後，整首的絕對時間 | — |
| `align_line` | `audio`：speech.wav | `text`、`rubies`、`t0`、`t1`（下一句開頭，可為 null）、`language` | `{line}` | — |
| `qa` | `audio`：speech.wav | `lines`、`texts`、`rubies`、`language`、`model` | `{doc: {lines}}`：每句的檢查結果（`index`、`status`、`reasons`、`offset`…） | — |
| `reading` | — | `language`、`texts` | `{lines: [[{start, end, ruby}…]…]}`：每句的**自動**讀音，位置以 code point 計 | — |
| `render` | `media`：伴奏影片（或原曲，看 target）；`ass`：手動改過的 ASS（選填） | 見下 | `{width, height, ass_sha256}` | `karaoke.ass`（沒有手動 ASS 時）、`video.mp4` |

`render` 的 params：

```jsonc
{
  "target": "instrumental",
  "lines": [ … ],                       // 對時結果（alignment.json 的 lines）
  "texts": ["…"], "rubies": [[…]],      // 歌詞句子與手動讀音（日文的假名由 worker 自己算，只加在逐字內容等於原文的句子上）
  "language": "ja",
  "singers": ["男", null, …],           // 句數和對時不符時為 []
  "translations": ["…", ""],            // 不燒時為 []
  "title_card": ["歌名", "演唱者", "備註"],   // 不顯示時為 [null, null]
  "scale": 1.0,                         // 字幕大小（1 = 100%）；worker 乘上預設樣式的字高比例
  "font": { "sha256": "…", "family": "Noto Sans CJK JP", "index": 0 },
  "encode": { "prefer": ["h264_nvenc", "libx264"] }   // 依序嘗試：NVENC p5 cq23 / x264 medium crf18；音訊 aac 320k
}
```

`reading` 也會被 `render` 內部用到（日文假名），worker 直接在行程內呼叫，不另外派任務。

`reading` 只看句子文字、**不看手動讀音**：讀音快取的 key 是「語言 + 句子 + 版本」，
結果必須和手動讀音無關才能快取。手動讀音和自動讀音的合併（手動優先、重疊的自動讀音丟掉、台語 / 粵語每字一格）
由 NAS 做（readings 套件，規格測試 `readings`）。

所有歌詞位置（`rubies`、`reading` 的 `start` / `end`）都以 Unicode code point 計，和 Python 字串索引相同；
Go 端要用 `[]rune` 換算，不能用 byte 位置。

## worker 的紀錄

worker 送出的 `logs` 是繁中訊息（`  . 去人聲中…`、`  [!] …`），NAS 原樣接到工作紀錄後面，並在前面標 worker 名稱。

## 錯誤訊息

worker 回報的 `error` 是給使用者看的繁中說明（例如「歌詞的讀音比人聲長度還多，無法對齊（歌詞是不是貼錯了？）」），
不要直接丟 Python traceback；traceback 印在 worker 自己的主控台。
