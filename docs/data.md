# 資料格式

紀錄**只用相對路徑和內容雜湊**，不記絕對路徑和修改時間：整個曲庫搬到另一台電腦或另一顆硬碟都一樣，
不會因為路徑或修改時間變了，就判斷成需要重做（甚至整首重新對時、蓋掉手動調整）。每首歌一個資料夾。

## 資料夾結構

根目錄由 `--library`（環境變數 `KARA_LIBRARY`）指定，例如 `/Volumes/Media/kara`：

```
<library>/
  library.json            曲庫結構：資料夾、每首歌所在的資料夾與順序；全域設定
  songs/
    <歌曲 id>/
      song.json           這首歌的一切紀錄（見下）
      source.<ext>        來源影片或音訊（下載的或手動放入的；保留原副檔名）
      lyrics.txt          歌詞（格式見根目錄 README「使用」與歌詞編輯器的說明）
      instrumental.<ext>  伴奏：和來源同格式，影像軌直接複製
      vocals.flac         人聲（只存音訊、無損）
      alignment.json      逐字時間軸（見下）
      qa.json             對時檢查結果
      karaoke.ass         字幕（AI 產生；使用者可以用 Aegisub 修改，見「手改字幕」）
      karaoke.mp4         伴唱帶（伴奏 + 字幕）
      original.mp4        原曲 + 字幕（targets 有 original 時）
  inbox/                  手動放入的影音檔（放進來就會匯入曲庫）
  export/                 已確認的成品，依曲庫整理（給卡拉 OK 播放軟體讀；見「確認與匯出」）
    .export.json          匯出工具自己管理的檔案清單（{"files": {相對路徑: 歌曲 id}}）
  fonts/                  自訂字型（預設字型在 NAS 映像裡）
  cache/                  可以整個刪掉的衍生檔
    work/                 給 AI 的暫存輸入（抽出的 wav、speech.wav），以 sha256 命名
    readings.jsonl        讀音快取
```

資料備份（git repo）的位置另外指定（`--data`，預設 `<library>/data`），格式見「資料備份」。

### 歌曲 id

- YouTube：影片 id（11 碼，例如 `dQw4w9WgXcQ`）
- 其他網站：`<extractor 小寫>-<影片 id>`，不安全的字元換成 `_`
- 手動放入：`local-` + sha1(`檔名:大小`)[:8]（放回同一個檔案就是同一個 id，資料備份還原時才對得上）

id 決定資料夾名稱，之後不會改變。歌名、資料夾、順序都只是 `library.json` 和 `song.json` 裡的資料，不會搬動檔案。

## song.json

所有寫入都用「寫暫存檔再改名」，寫到一半當掉也不會留下壞掉的檔案。檔案一律記 `{name, size, mtime_ns, sha256}`：
大小與修改時間都沒變就沿用 sha256，否則重算（修改時間只用來省掉重算，不進指紋）。欄位：

```jsonc
{
  "version": 2,
  "id": "dQw4w9WgXcQ",
  "source": {
    "kind": "url",                    // url | local
    "url": "https://www.youtube.com/watch?v=…",   // local 時省略
    "extractor": "Youtube",
    "video_id": "dQw4w9WgXcQ",
    "title": "影片原始標題",           // local 時是檔名（不含副檔名）
    "uploader": "…", "channel": "…", "track": "…", "artists": ["…"],   // 標題辨識用
    "duration": 213.4,
    "mode": "video",                  // video | audio
    "original_name": "手動放入時的原始檔名.mp4",   // 只有 local
    "file": { "name": "source.mp4", "size": 12345678, "mtime_ns": 0, "sha256": "…" },
    "width": 1920, "height": 1080,    // ffprobe；純音訊時省略
    "added_at": "2026-10-03T12:00:00+08:00",
    "meta_version": 1
  },
  "info": {                           // 使用者設定
    "title": "", "artist": "",        // 空字串 = 自動（歌詞檔的 # title / # artist > 標題辨識）
    "language": "",                   // 空字串 = 依歌詞文字判斷；zh / nan / yue / ja / en
    "note": "",                       // 標題畫面第三行
    "link": "",                       // 手動放入的影片補上的原始連結
    "translation": true,              // 歌詞有翻譯時是否燒進伴唱帶
    "targets": ["instrumental"],      // 要做哪些成品：instrumental（伴唱帶）/ original（原曲＋字幕，UI 還沒有入口）
    "approved": { "fingerprint": "…", "at": "…", "texts": ["確認當時每句的歌詞"] },   // 目前的確認；沒確認過或取消確認時為 null
    "approval_history": [ { "fingerprint": "…", "at": "…", "texts": [ … ] } ]          // 曾經確認過的紀錄（最近 20 次）
  },
  "stages": {
    "separate": {
      "key": "…",                     // 指紋，見下
      "instrumental": { "name": "instrumental.mp4", "size": 0, "mtime_ns": 0, "sha256": "…" },
      "vocals":       { "name": "vocals.flac",      "size": 0, "mtime_ns": 0, "sha256": "…" },
      "worker": "pc-4070", "done_at": "…"
    },
    "align": {                        // 對時本身存在 alignment.json；這裡只記狀態
      "key": "…", "worker": "…", "done_at": "…"
    },
    "render": {
      "instrumental": {               // 每個 target 一份
        "key": "…",
        "ass": { "name": "karaoke.ass", "size": 0, "mtime_ns": 0, "sha256": "…", "manual": false },
        "video": { "name": "karaoke.mp4", "size": 0, "mtime_ns": 0, "sha256": "…" },
        "worker": "…", "done_at": "…"
      }
    },
    "qa": { "key": "…", "worker": "…", "done_at": "…" }
  }
}
```

`library.json`：

```jsonc
{
  "version": 2,
  "folders": [ { "id": "a1b2c3d4", "name": "日文", "parent": null, "order": 1 } ],
  "songs":   { "dQw4w9WgXcQ": { "folder": "a1b2c3d4", "order": 3 } },
  "settings": { "subtitle_scale": 1.0, "fonts": { "ja": "<字型 id>", "zh": "…" } }   // 字型 id = 字型檔 sha256:第幾個字型
}
```

順序的規則（拖曳插入只做最少的編號調整、留空號不動等）見 library 套件與它的規格測試。

## alignment.json



```jsonc
{
  "key": "…",                                  // 對時指紋（見下）
  "lyrics": "…",                               // 對時當下的歌詞指紋（AI 重對前確認歌詞沒改過；指紋本身是雜湊，拆不出來）
  "language": "ja", "method": 7, "model": "large-v3",   // 對時當下的語言、方法、模型（資料備份還原時判斷能不能沿用）
  "run": "3f9c…",                              // 整首對時的編號：整首重新對時才換（手動調整、AI 重對某幾句不換）；「已確認」綁它
  "lines": [ { "text": "…", "start": 1.234, "end": 2.345,
               "words": [ { "text": "…", "start": 1.234, "end": 1.5 } ] } ],
  "adjustments": [ … ],                        // 手動調整與 AI 重對的紀錄
  "restored": { "lyrics": "…", "language": "ja", "method": 7 }   // 從資料備份還原時：歌詞指紋、語言、方法都相同就沿用
}
```

整句文字以逐字內容為準（words 的 text 串起來）。

## 指紋

**所有指紋都是 sha256 的十六進位字串，輸入是下面明確定義的文字格式**（不要用 JSON 序列化當輸入：
Go 和 Python 的 JSON 輸出細節不同，浮點數格式也不同）。時間一律先四捨五入成**整數毫秒**：`floor(秒 × 1000 + 0.5)`。
實作：`nas/internal/fingerprint`。算法改了會讓所有歌被判定需要重做，所以固定成規格資料（`nas/testdata/spec/fingerprint.json`）。

```
H(欄位…)       = sha256( 每個欄位以 "\n" 串接，最後也加 "\n" )；每個欄位是 "名稱=值"
                  （值裡的 "\" 寫成 "\\"、換行寫成 "\n"；字幕大小寫成小數兩位，例如 1.00）
歌詞指紋        = sha256( 每句：句子文字 + 每個手動讀音依 start 排序「\x1f{start}\x1f{end}\x1f{reading}」，句與句以 "\x1e" 串接 )
                  （只看要唱的句子；演唱者、翻譯、註解、標題不算——它們不影響對時）
對時內容指紋    = sha256( 每句：「{start_ms}\x1f{end_ms}」再加每個字「\x1d{text}\x1f{start_ms}\x1f{end_ms}」，句與句以 "\x1e" 串接 )
清單            = 各項以 "\x1f" 串接（例如演唱者清單，沒標的句子為空字串）
```

| 階段 | 指紋 = H(…) | 指紋變了代表 |
| --- | --- | --- |
| 去人聲 `separate` | `stage=separate`、`source=<來源 sha256>`、`model=htdemucs`、`stems=2`、`version=<versions.separate>` | 來源檔換了，或去人聲方法改了 |
| 對時 `align` | `stage=align`、`lyrics=<歌詞指紋>`、`vocals=<人聲 sha256>`、`model=large-v3`、`language=<語言>`、`version=<versions.align>` | 歌詞文字或讀音改了、人聲重新分離過、語言改了、對時方法改了 → **整首重新對時** |
| 成品 `render`（每個 target） | `stage=render`、`target`、`media=<伴奏或來源 sha256>`、`alignment=<對時內容指紋>`、`lyrics=<歌詞指紋>`、`singers=<清單>`、`translations=<清單，不燒時為空>`、`title=<歌名>`、`artist=<演唱者>`、`note=<備註>`、`scale=<字幕大小，小數兩位>`、`font=<字型 id（sha256:index）>`、`size=<寬>x<高>`、`version=<versions.render>`、`reading=<versions.reading>`（日文假名由 worker 燒錄時自己算）；**手動改過 ASS 時**只剩 `stage`、`target`、`media`、`ass=<那份 ASS 的 sha256>`、`size`、`version`（畫面完全由那份 ASS 決定，標題畫面也在裡面） | 任何會改變畫面的東西變了 → **只重新產生字幕與燒錄** |
| 對時檢查 `qa` | `stage=qa`、`alignment=<對時內容指紋>`、`lyrics=<歌詞指紋>`、`vocals=<人聲 sha256>`、`language`、`model`、`version=<versions.qa>` | 對時改了 → 重新檢查 |
| 已確認 | `stage=approve`、`lyrics=<歌詞指紋>`、`language`、`method=<versions.align>`、`run=<alignment.json 的對時編號>`；伴唱帶是 needs_align 時一律算失效 | **需要重新對時**才失效：只需重燒的更新（演唱者、翻譯、標題畫面、字型、字幕大小、手動調時間、AI 重對某幾句）都不影響確認 |

標題畫面：歌名來自手動設定、歌詞檔或標題辨識；標題辨識只能用整個影片標題（`source=fallback`）時不顯示標題畫面。

### 狀態判斷（planner）

```
download   有 source 而且檔案存在 → done
separate   沒紀錄 → pending；紀錄的 key ≠ 現在算出的 key，或檔案不見 → outdated；否則 done
lyrics     有 lyrics.txt → done，否則 missing
karaoke    沒歌詞 → no_lyrics；（任何 target 的）render 沒紀錄 → pending；
           align key 不符或去人聲不是最新 → needs_align（需重新對時）；
           render key 不符或成品檔不見 → needs_render（只需重燒）；否則 done
approval   沒確認過 → ""；確認的指紋和目前相同 → approved；否則 stale（重新對時後失效），
           並和最近一次確認時的歌詞比對，列出改了哪幾句（小改動不必整首重看）
qa         沒紀錄或 key 不符 → 沒檢查（UI 不顯示疑慮數）；否則顯示 qa.json 的統計
```

NAS 在記憶體裡保留所有歌的狀態，**只在有變動時重算該首歌**（API 寫入、工作完成、inbox 有新檔、檔案監看到 `songs/<id>/` 有變），
不輪詢掃描整個曲庫，NAS 的硬碟才能休眠。來源檔的 sha256 只在大小或修改時間變了才重算。

## 去人聲

- NAS 抽音軌：44.1kHz 立體聲 16-bit wav → 任務 `separate`。加 bitexact，同一個來源每次抽出來完全相同，worker 的快取才認得
- 取回 `vocals.wav`、`no_vocals.wav` 之後：
  - 伴奏：`no_vocals.wav` 和來源影像軌封裝成 `instrumental.<來源副檔名>`（影像 `-c:v copy`，音訊編碼依容器）
  - 人聲：轉成 `vocals.flac`（無損）
- 對時、檢查要的 16kHz 單聲道 wav（`speech.wav`）由 NAS 從 `vocals.flac` 轉出，放 `cache/work/`
  （加 bitexact，同一份人聲每次轉出來位元相同）

## 字型

- 自訂字型放 `<library>/fonts/`；預設字型在 NAS 映像的 `/opt/kara/fonts`（`--fonts` / `KARA_FONTS`），兩邊一起掃描，同一個檔案用曲庫的
- NAS 啟動時掃描字型目錄：讀每個字型檔（含 .ttc 裡的每個字型）的 family 名稱、粗細、sha256，列在 API `GET /api/v1/fonts`
- 預設字型：日文 Noto Sans CJK JP Bold、中文 / 台語 / 粵語 Noto Sans CJK TC Bold、韓文 Noto Sans CJK KR Bold、
  英文 Noto Sans CJK JP Bold（授權 SIL OFL）
- 一個字型用「字型檔 sha256:第幾個字型」識別（.ttc 一個檔案裡有好幾個，例如 Noto Sans CJK 的 JP、TC、KR）
- 設定裡每種語言選一個字型（存字型 id）；預設用上面的預設字型（依完整名稱 nameID 4 找，例如「Noto Sans CJK JP Bold」；ASS 的 Fontname 用 family「Noto Sans CJK JP」）
- 字型會出現在成品指紋裡：換字型 → 成品需更新（只重燒）
- 前端播放畫面的即時字幕用 `@font-face` 載入同一個字型檔（`GET /fonts/<sha256>`），預覽和成品外觀一致

## 確認與匯出

**只匯出「已確認」的歌，而且只在使用者按「匯出」時才同步**（`POST /api/v1/export`），處理完不會自動匯出。

- 確認：在播放畫面按「確認沒問題」，或在曲庫批次標記。伴唱帶做好（karaoke 是 done）才能確認
- 更新分兩種：
  - **只需重燒**（演唱者、翻譯、標題畫面、字型、字幕大小、手動調時間、AI 重對某幾句）：確認保留
  - **需重新對時**（改了歌詞文字或讀音、重新去人聲、改了語言）：確認失效（stale），但留下確認紀錄（最近 20 次），
    並和最近一次確認時的歌詞比對、標出改了哪幾句，小改動不必整首重看
- 每首歌的摘要有 `exported`：exported（已匯出最新成品）／pending（已確認但還沒匯出，或成品更新了、改了名）／
  remove（取消確認了，匯出資料夾還有舊檔，下次匯出拿掉）／空（沒確認）。匯出按鈕上的數字是 pending + remove 的首數
- 按匯出時，取消確認、改名或換資料夾的歌，舊的匯出檔會一併拿掉

目的地用 `--export`（`KARA_EXPORT`）指定，預設 `<library>/export`。資料夾結構同曲庫、檔名「歌手 - 歌名.mp4」、
同名加 (2)（依所有歌排，編號才穩定）；只管理 `.export.json` 記的檔案（`{"files": {相對路徑: 歌曲 id}}`），不動使用者另外放的東西。
只匯出 `instrumental` 成品；匯出的是按下匯出當時的成品。

放置方式依序嘗試，失敗就換下一種，不必事先設定：

1. **clone**（不佔空間、各自獨立）：macOS 原生用 `clonefile`；Linux / container 用 `FICLONE`（reflink）
2. **硬連結**（不佔空間；曲庫裡的檔案權限是 644，SMB 的其他使用者讀得到）
3. **複製**

container 裡要讓 export 和曲庫在同一個掛載點（`/library`），clone / 硬連結才能用。

## 手改字幕

使用者用 Aegisub 改過 `karaoke.ass` 時，NAS 偵測到內容和程式產生的不同（`render.ass.manual`），
只要對時、歌詞、樣式都沒變（render 的內容指紋相同）就保留這份 ASS，燒錄時改送它給 AI 伺服器；
對時或歌詞改了，程式重新產生 ASS（手改的內容會被取代）。

## 讀音快取

`cache/readings.jsonl`，每行一筆：

```json
{"k": "<sha256(language \x1f 句子 \x1f versions.reading)>", "v": 1, "spans": [{"start": 0, "end": 2, "ruby": "きょう"}]}
```

- 值是 AI `reading` 任務對一句的回傳：只有自動讀音（見 worker-protocol.md）；手動讀音在 NAS 合併
- 啟動時全部讀進記憶體（量很小：上千句也只有幾百 KB）；新增時附加一行
- `v` 是當時的 `versions.reading`：改版後啟動時丟掉舊版的行並重寫檔案（寫到一半的行也一併丟掉）
- 目前只有日文有自動讀音；台語、粵語全部手動標，中文、英文不顯示讀音

## 資料備份（data/ git repo）

能重做一次的資料（歌單、影片連結、歌詞、對時）寫到 `data/`（預設 `<library>/data`，`--data` 可以改）。
它本身是另一個**私人** git repo：是 git repo 時自動 commit，有 remote 就 push（push 失敗只記錄，下次再試）。

- `songs.json`（`version` 2）每首歌的欄位：`id`、`folder`、`order`、`info`（同 song.json 的 info，`approved` 只記指紋）、
  `display`（實際使用的歌名與演唱者）、`source`（同 song.json 的 source，去掉 `file`、`sha256` 以外的本機資訊；
  手動放入的記 `original_name` 與 `size`）、`lyrics`、`timing`
- `lyrics/<id>.txt`、`settings.json`
- `timing/<id>.json`：`lyrics_fingerprint`（歌詞指紋）、`language`、`model`、`method`（versions.align）、`run`、`lines`、`adjustments`
- 還原（`kara-nas restore [--overwrite] [--replace 歌曲id=網址]`，伺服器要先關閉：曲庫有檔案鎖）：
  重新下載有連結的歌（手動放入的列出原始檔名與大小，放回 inbox 後再執行一次）→ 歌詞、語言、方法都和對時當下相同就沿用對時
  （寫 `restored`），不必重新對時；去人聲和成品照常重做
