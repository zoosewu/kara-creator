# 待辦

設計見 [docs/](../docs/README.md)。完成的工作在 git 歷史裡，這裡只留還沒做的和最近的檢討。

## 還沒做

- [ ] 歌詞編輯器的台語 / 粵語每字一格輸入：用台語、粵語的歌實測
- [ ] Windows 直接執行 worker（`worker/setup.ps1`、`worker.ps1`）：保留但沒實測過；使用者部署遇到問題再討論（2026-10-04）
- [ ] `nas-server.md`「資源檢查清單」：在 Mac 上量閒置記憶體、閒置時不碰硬碟（需要使用者的 Mac）

## 階段 tag 合併、匯出原曲音訊（2026-10-08）

- [x] 曲庫的階段徽章：已完成的合併成一個「✓ 3/4」，滑鼠移上去列出每一項的狀態，顏色依完成程度（4/4 綠、越少越偏橘、0 灰）；沒完成的照舊分開
- [x] 設定加「匯出時也匯出原曲音訊」（`library.json` settings 的 `export_original`）
- [x] 原曲音訊：來源的音軌存成 m4a（AAC 直接複製、其他編碼轉 AAC 320k），放 `cache/original/<來源 sha256>.m4a`；
      匯出檔名「歌手 - 歌名_original.m4a」（同名編號照伴唱帶：「… (2)_original.m4a」）；只匯出已確認的歌
- [x] 開關關掉時，下次匯出把 `_original` 檔拿掉；匯出狀態（待匯出 / 已匯出）把原曲音訊算進去
- [x] 測試：export 的 Sync、端對端（開 → 匯出 → 有檔案；關 → 匯出 → 拿掉）、前端截圖；文件（data.md、frontend.md）

## 單一版本化（2026-10-06 ～ 10-08）

- [x] 刪除只有舊版用到的程式：舊的網頁 UI、命令列工具與 .ps1、推送式 AI 伺服器、`songtool/` 的 14 個模組、hook、搬遷工具、`requirements.txt`
- [x] 規格測試的產生器刪除，答案檔改名 `nas/testdata/spec/`，成為規格；`restore` 只讀現在的備份格式（拿掉舊格式、`--sources`、舊版歌詞雜湊）
- [x] AI worker 重構成 `worker/`（src layout 套件 `kara_worker`、pyproject、`kara-worker` 指令、ruff）；
      版本號統一由 versions.json 管；拿掉推送式 AI 伺服器的客戶端、舊的輸出資料夾與紀錄檔、Windows 字型表、檢查結果裡的舊快取欄位
- [x] Windows 直接執行的 `setup.ps1`、`worker.ps1` 搬進 `worker/`，只裝 worker 需要的
- [x] `docs/v2/` → `docs/`，改寫成單一版本的說明；AGENTS.md、README、CI（`ci.yml`）跟著改；Go 與前端註解不再提舊版
- [x] 驗證：見下方檢討

## 檢討

### 階段 tag 合併、匯出原曲音訊（2026-10-08）

- 驗證：export 的單元測試（同名編號、關掉後拿掉）、端對端（設定開 → 待匯出 → 匯出出現只有 AAC 音軌的 m4a → 已匯出；關 → 待匯出 → 拿掉）、
  開發曲庫實際匯出（YouTube 來源的 AAC 直接複製，2:16 的歌 2.2 MB）、瀏覽器截圖（亮 / 暗、提示文字、設定）
- `withJob` 每首歌都會讀設定：加了 `Store.Settings()`，不必每次複製整個曲庫
- 匯出結果改用「個檔案」數（開了原曲音訊時一首歌兩個檔案）

### 單一版本化（2026-10-08）

- worker 重構前後在 GPU 上逐字比對（docs/deploy.md「worker 的 GPU 回歸比對」）：三首歌（故郷、茉莉花、自己編的歌）的
  `align`、`align_from`、`align_line`、`qa`、`reading`、`render`（ASS）18 件**全部相同**；`separate` 的 SNR 和
  「舊程式自己跑兩次」在同一個範圍（故郷：重構前後 34.4 / 29.4 dB，舊程式兩次 33.5 / 28.4 dB）
- 輸出裡的 ffmpeg `Broken pipe` 是原本就有的（新舊程式跑同一件任務，各 5 行一樣）
- ruff 的 `zip()` 一律明確寫 `strict=False`（和原本行為相同；要不要改成 `strict=True` 需要逐一確認資料長度，不在這次範圍）
- 映像與 compose 的名稱（`kara-creator-ai`、`compose.ai.yml`）維持不變：使用者已經部署了，改名會讓舊名稱默默停止更新
