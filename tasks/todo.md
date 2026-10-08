# 待辦

設計見 [docs/](../docs/README.md)。完成的工作在 git 歷史裡，這裡只留還沒做的和最近的檢討。

## 還沒做

- [ ] 歌詞編輯器的台語 / 粵語每字一格輸入：用台語、粵語的歌實測
- [ ] Windows 直接執行 worker（`worker/setup.ps1`、`worker.ps1`）：保留但沒實測過；使用者部署遇到問題再討論（2026-10-04）
- [ ] `nas-server.md`「資源檢查清單」：在 Mac 上量閒置記憶體、閒置時不碰硬碟（需要使用者的 Mac）

## 單一版本化（2026-10-06 ～ 10-08）

- [x] 刪除只有舊版用到的程式：舊的網頁 UI、命令列工具與 .ps1、推送式 AI 伺服器、`songtool/` 的 14 個模組、hook、搬遷工具、`requirements.txt`
- [x] 規格測試的產生器刪除，答案檔改名 `nas/testdata/spec/`，成為規格；`restore` 只讀現在的備份格式（拿掉舊格式、`--sources`、舊版歌詞雜湊）
- [x] AI worker 重構成 `worker/`（src layout 套件 `kara_worker`、pyproject、`kara-worker` 指令、ruff）；
      版本號統一由 versions.json 管；拿掉推送式 AI 伺服器的客戶端、舊的輸出資料夾與紀錄檔、Windows 字型表、檢查結果裡的舊快取欄位
- [x] Windows 直接執行的 `setup.ps1`、`worker.ps1` 搬進 `worker/`，只裝 worker 需要的
- [x] `docs/v2/` → `docs/`，改寫成單一版本的說明；AGENTS.md、README、CI（`ci.yml`）跟著改；Go 與前端註解不再提舊版
- [x] 驗證：見下方檢討

## 檢討

### 單一版本化（2026-10-08）

- worker 重構前後在 GPU 上逐字比對（docs/deploy.md「worker 的 GPU 回歸比對」）：三首歌（故郷、茉莉花、自己編的歌）的
  `align`、`align_from`、`align_line`、`qa`、`reading`、`render`（ASS）18 件**全部相同**；`separate` 的 SNR 和
  「舊程式自己跑兩次」在同一個範圍（故郷：重構前後 34.4 / 29.4 dB，舊程式兩次 33.5 / 28.4 dB）
- 輸出裡的 ffmpeg `Broken pipe` 是原本就有的（新舊程式跑同一件任務，各 5 行一樣）
- ruff 的 `zip()` 一律明確寫 `strict=False`（和原本行為相同；要不要改成 `strict=True` 需要逐一確認資料長度，不在這次範圍）
- 映像與 compose 的名稱（`kara-creator-ai`、`compose.ai.yml`）維持不變：使用者已經部署了，改名會讓舊名稱默默停止更新
