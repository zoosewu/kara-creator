# 資料格式

v1 的紀錄檔裡有絕對路徑（例如 `karaoke.json` 的 `videos.*.source`）和檔案修改時間（例如對時 key 的 `vocals_mtime_ns`）。
搬到另一台電腦或另一顆硬碟，路徑和修改時間都會變，整個曲庫就會變成需更新，甚至整首重新對時、手動調整被蓋掉。
v2 的紀錄**只用相對路徑和內容雜湊**，整個資料夾搬到哪裡都一樣。

> Q8（定案）：每首歌一個資料夾。

## 資料夾結構

根目錄由 `--library`（環境變數 `KARA_LIBRARY`）指定，例如 `/Volumes/Media/kara`：

```
<library>/
  library.json            曲庫結構：資料夾、每首歌所在的資料夾與順序；全域設定
  songs/
    <歌曲 id>/
      song.json           這首歌的一切紀錄（見下）
      source.<ext>        來源影片或音訊（下載的或手動放入的；保留原副檔名）
      lyrics.txt          歌詞（格式同 v1）
      instrumental.<ext>  伴奏：和來源同格式，影像軌直接複製
      vocals.flac         人聲（只存音訊，Q7）
      alignment.json      逐字時間軸（格式同 v1，見下）
      qa.json             對時檢查結果
      karaoke.ass         字幕（AI 產生；使用者可以用 Aegisub 修改，Q15）
      karaoke.mp4         伴唱帶（伴奏 + 字幕）
      original.mp4        原曲 + 字幕（targets 有 original 時，Q13）
  inbox/                  手動放入的影音檔（取代 v1 直接丟進 output/downloads/）
  export/                 依曲庫整理的成品（給 karaoke 播放軟體讀）
    .export.json          匯出工具自己管理的檔案清單
  fonts/                  字型檔：預設的開源字型 + 使用者自訂（可以另外掛載）
  cache/                  可以整個刪掉的衍生檔
    work/                 給 AI 的暫存輸入（抽出的 wav、speech.wav），以 sha256 命名
    readings.jsonl        讀音快取
```

資料備份（git repo）的位置另外指定（`--data`，預設 `<library>/data`），格式見「資料備份」。

### 歌曲 id

- YouTube：影片 id（11 碼，例如 `dQw4w9WgXcQ`），和 v1 資料備份的 id 相同
- 其他網站：`<extractor 小寫>-<影片 id>`，不安全的字元換成 `_`
- 手動放入：`local-` + sha1(`檔名:大小`)[:8]（和 v1 相同，放回同一個檔案就是同一個 id）

id 決定資料夾名稱，之後不會改變。歌名、資料夾、順序都只是 `library.json` 和 `song.json` 裡的資料，不會搬動檔案。

## song.json

所有寫入都用「寫暫存檔再改名」（v1 `manifest.write` 的做法）。檔案一律記 `{name, size, mtime_ns, sha256}`：
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
    "uploader": "…", "channel": "…", "track": "…", "artists": ["…"],   // 標題辨識用（v1 download.json 的欄位）
    "duration": 213.4,
    "mode": "video",                  // video | audio
    "original_name": "手動放入時的原始檔名.mp4",   // 只有 local
    "file": { "name": "source.mp4", "size": 12345678, "mtime_ns": 0, "sha256": "…" },
    "width": 1920, "height": 1080,    // ffprobe；純音訊時省略
    "added_at": "2026-10-03T12:00:00+08:00",
    "meta_version": 1
  },
  "info": {                           // 使用者設定（v1 catalog.Song 的欄位）
    "title": "", "artist": "",        // 空字串 = 自動（歌詞檔的 # title / # artist > 標題辨識）
    "language": "",                   // 空字串 = 依歌詞文字判斷；zh / nan / yue / ja / en
    "note": "",                       // 標題畫面第三行
    "link": "",                       // 手動放入的影片補上的原始連結
    "translation": true,              // 歌詞有翻譯時是否燒進伴唱帶
    "targets": ["instrumental"],      // 要做哪些成品：instrumental（伴唱帶）/ original（原曲＋字幕）（Q13）
    "approved": { "fingerprint": "…", "at": "…" }   // 沒確認過時為 null（Q6）
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
  "settings": { "subtitle_scale": 1.0, "fonts": { "ja": "<字型檔 sha256>", "zh": "…" } }
}
```

順序的規則（拖曳插入只做最少的編號調整、留空號不動等）照 v1 `catalog.place` 移植。

## alignment.json

和 v1 相同的 `lines` 結構，`key` 改成指紋字串：

```jsonc
{
  "key": "…",                                  // 對時指紋（見下）
  "lyrics": "…",                               // 對時當下的歌詞指紋（AI 重對前確認歌詞沒改過；指紋本身是雜湊，拆不出來）
  "lines": [ { "text": "…", "start": 1.234, "end": 2.345,
               "words": [ { "text": "…", "start": 1.234, "end": 1.5 } ] } ],
  "adjustments": [ … ],                        // 手動調整與 AI 重對的紀錄（同 v1）
  "restored": { "lyrics": "…", "language": "ja", "method": 7 }   // 從資料備份還原時：歌詞指紋、語言、方法都相同就沿用
}
```

整句文字以逐字內容為準（v1 `karaoke.line_text`）。

## 指紋

**所有指紋都是 sha256 的十六進位字串，輸入是下面明確定義的文字格式**（不要用 JSON 序列化當輸入：
Go 和 Python 的 JSON 輸出細節不同，浮點數格式也不同）。時間一律先四捨五入成**整數毫秒**：`floor(秒 × 1000 + 0.5)`。
實作：Go `nas/internal/fingerprint`、Python `migrate/fingerprint.py`，黃金測試確認兩邊相同。

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
| 成品 `render`（每個 target） | `stage=render`、`target`、`media=<伴奏或來源 sha256>`、`alignment=<對時內容指紋>`、`lyrics=<歌詞指紋>`、`singers=<清單>`、`translations=<清單，不燒時為空>`、`title=<歌名>`、`artist=<演唱者>`、`note=<備註>`、`scale=<字幕大小，小數兩位>`、`font=<字型 sha256>`、`size=<寬>x<高>`、`version=<versions.render>`；**手動改過 ASS 時**只剩 `stage`、`target`、`media`、`ass=<那份 ASS 的 sha256>`、`size`、`version`（畫面完全由那份 ASS 決定，標題畫面也在裡面） | 任何會改變畫面的東西變了 → **只重新產生字幕與燒錄** |
| 對時檢查 `qa` | `stage=qa`、`alignment=<對時內容指紋>`、`lyrics=<歌詞指紋>`、`vocals=<人聲 sha256>`、`language`、`model`、`version=<versions.qa>` | 對時改了 → 重新檢查 |
| 已確認（Q6） | `stage=approve`、`alignment`、`lyrics`、`singers`、`translations`、`title`、`artist`、`note` | 內容變了 → 需重新確認（換字型、改字幕大小不影響） |

標題畫面規則同 v1 `karaoke.title_card`：歌名來自手動設定、歌詞檔或標題辨識；標題辨識只能用整個影片標題（`source=fallback`）時不顯示標題畫面。

### 狀態判斷（planner）

```
download   有 source 而且檔案存在 → done
separate   沒紀錄 → pending；紀錄的 key ≠ 現在算出的 key，或檔案不見 → outdated；否則 done
lyrics     有 lyrics.txt → done，否則 missing
karaoke    沒歌詞 → no_lyrics；（任何 target 的）render 沒紀錄 → pending；
           align key 不符、或任何 target 的 render key 不符、或成品檔不見 → outdated；否則 done
qa         沒紀錄或 key 不符 → 沒檢查（UI 不顯示疑慮數）；否則顯示 qa.json 的統計
```

NAS 在記憶體裡保留所有歌的狀態，**只在有變動時重算該首歌**（API 寫入、工作完成、inbox 有新檔、檔案監看到 `songs/<id>/` 有變），
不像 v1 每次輪詢都掃整個曲庫，NAS 的硬碟才能休眠。來源檔的 sha256 只在大小或修改時間變了才重算。

## 去人聲

- NAS 抽音軌：44.1kHz 立體聲 16-bit wav（v1 `media.extract_wav`）→ 任務 `separate`
- 取回 `vocals.wav`、`no_vocals.wav` 之後：
  - 伴奏：`no_vocals.wav` 和來源影像軌封裝成 `instrumental.<來源副檔名>`（v1 `media.mux`，影像 `-c:v copy`，音訊編碼依容器）
  - 人聲：轉成 `vocals.flac`（Q7）
- 對時、檢查要的 16kHz 單聲道 wav（`speech.wav`）由 NAS 從 `vocals.flac` 轉出，放 `cache/work/`
  （ffmpeg 參數同 v1 `ai._speech_wav`，加 bitexact，同一份人聲每次轉出來位元相同）。
  已驗證過：用這份 16kHz wav 對時，結果和 v1 直接給人聲原檔**逐字相同**
- 注意：v1 的人聲和來源同格式（通常是 AAC，有損），v2 的人聲是 Demucs 輸出直接存成 FLAC（無損）。
  所以**新歌**的 speech.wav 和 v1 處理同一首歌時不同（品質較好，但對時結果不會和 v1 逐字相同）；
  **搬遷過來的歌**是把 v1 的人聲轉成 FLAC，解出來的聲音和 v1 相同，對時不受影響。
  階段 2 驗證「和 v1 逐字相同」時，要讓兩邊用同一份 speech.wav
- 抽音軌（給 Demucs）也加 bitexact，同一個來源每次抽出來完全相同，worker 的快取才認得

## 字型

- 字型檔放 `<library>/fonts/`（container 版可以把自訂字型資料夾掛載到這裡）
- NAS 啟動時掃描字型目錄：讀每個字型檔（含 .ttc 裡的每個字型）的 family 名稱、粗細、sha256，列在 API `GET /api/v1/fonts`
- 預設字型（Q11）：日文 Noto Sans CJK JP Bold、中文 / 台語 / 粵語 Noto Sans CJK TC Bold、韓文 Noto Sans CJK KR Bold、
  英文 Noto Sans CJK JP Bold。安裝時下載到 `fonts/`（不進 git；授權 SIL OFL，可以放進 NAS 映像）
- 設定裡每種語言選一個字型（存字型檔的 sha256）；預設用上面的預設字型
- 字型會出現在成品指紋裡：換字型 → 成品需更新（只重燒）
- 前端播放畫面的即時字幕用 `@font-face` 載入同一個字型檔（`GET /fonts/<sha256>`），預覽和成品外觀一致

## 匯出

規則同 v1 `export.py`：資料夾結構同曲庫、檔名「歌手 - 歌名.mp4」、同名加 (2)、只管理 `.export.json` 列的檔案。
放置方式依序嘗試：

1. **clone**（不佔空間、各自獨立）：macOS 原生用 `clonefile`；Linux / container 用 `FICLONE`（reflink）
2. **硬連結**
3. **複製**

Q2：曲庫在 Mac 的外接硬碟（APFS），NAS 伺服器跑在 OrbStack 的 container 裡（Q1）。
container 透過 bind mount 存取 APFS，clone 和硬連結能不能用要在 Mac 上實測（階段 5）；
程式每次都依序嘗試，失敗就換下一種，不必事先設定。`/library` 必須是同一個掛載點。

只匯出 `instrumental` 成品；成品需更新時照樣匯出舊的那份（和 v1 相同），重燒完再換成新的。

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

沿用 v1 的檔案（`songs.json`、`lyrics/<id>.txt`、`timing/<id>.json`、`settings.json`），`songs.json` 的 `version` 改成 2：

- 每首歌的欄位：`id`、`folder`、`order`、`info`（同 song.json 的 info，`approved` 只記指紋）、`display`（實際使用的歌名與演唱者）、
  `source`（同 song.json 的 source，去掉 `file`、`sha256` 以外的本機資訊；手動放入的記 `original_name` 與 `size`）、`lyrics`、`timing`
- `timing/<id>.json`：`lyrics_fingerprint`（歌詞指紋）、`language`、`model`、`method`（versions.align）、`lines`、`adjustments`
- 還原（`kara-nas restore`）：重新下載 / 等手動放入 → 歌詞相同（歌詞指紋）、語言相同、方法相同就沿用對時（寫 `restored`），
  不必重新對時；去人聲和成品照常重做
- 從 v1 格式的備份還原也要支援（讀 v1 的 `lyrics_sha1` 時，用 v1 的演算法驗證歌詞沒變：
  `sha1(json.dumps([[text, [{"start","end","reading"}…]]…], ensure_ascii=False))`，預設分隔符 `", "` 與 `": "`）

## 搬遷工具（v1 → v2）

Python，放在 `migrate/`，**在舊資料所在的 Windows 上執行**（用 v1 的 `songtool` 判斷舊狀態，最準）。
輸出到新的資料夾，**不修改舊資料**；確認後再把新資料夾整個複製到 Mac。

```
python migrate/v1_to_v2.py --out D:\kara-v2 --dry-run    # 只產生報告
python migrate/v1_to_v2.py --out D:\kara-v2              # 實際搬遷（複製或硬連結檔案）
```

每首歌：

| v1 | v2 | 狀態怎麼延續 |
| --- | --- | --- |
| `output/downloads/<資料夾>/` 的影片與 `download.json` | `source.<ext>`、`song.json` 的 source | 算 sha256 |
| `lyrics/<id>.txt`（或其他候選位置） | `lyrics.txt` | — |
| `output/library.json` 的歌曲欄位 | `song.json` 的 info；資料夾、順序到 `library.json` | — |
| `output/separated/…_instrumental.*`、`…_vocals.*` | `instrumental.<ext>`、`vocals.flac` | v1 判斷去人聲是最新的（`separate.is_current`）→ 寫入新的 separate key（視為最新） |
| `output/karaoke/…/alignment.json` | `alignment.json` | v1 對時 key 和目前的人聲、歌詞、語言、方法都相符 → 寫入新的 align key（**不會重新對時**，手動調整全部保留）；不符就照實標成需要重新對時 |
| `…/qa.json` | `qa.json` | v1 的 qa key 相符 → 寫入新的 qa key |
| `…_karaoke.mp4`、`.ass` | `karaoke.mp4`、`karaoke.ass` | 字型換了，**一律標成需更新**（render key 寫 `legacy-v1`）；檔案保留，匯出照常 |
| 已確認 | `info.approved` | v1 的確認是有效的（ass sha1 相符）→ 用新的「已確認」指紋（Q6）寫入 |
| 手動改過的 `.ass`（v1 偵測得到） | `karaoke.ass`，`manual: true` | Q15 |
| `output/settings.json` | `library.json` 的 settings | — |

試算報告：每首歌一行，列出 v1 狀態、v2 狀態、有沒有手動調整、會不會重新對時（**應該全部是「不會」，有的話列出原因**）、
來源檔大小。最後統計需要重燒的首數與估計時間。

試算結果要使用者確認後才實際搬遷；實際搬遷後再用 Go 的 planner 讀一次新資料夾，確認狀態和試算報告一致。
