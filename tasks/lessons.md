# 教訓

給之後接手的 AI 助手：踩過的坑與避免方法。

## 指令列

- **xh 在非互動的 shell 會把 stdin 當成 request body**，沒寫方法時自動變成 POST（伺服器回 405）。
  一律寫 `xh --ignore-stdin GET …`。
- 不要在指令裡單獨執行 `python`（沒有參數）：會進入互動模式等 stdin，整個指令卡住。
- 等程式結束用 `wait $PID`，不要用 `pgrep -f <路徑>`：`pgrep -f` 也會比對到執行 pgrep 的那個 shell 自己的指令列（2026-10-04 又犯一次：pkill 之後要等，用 `pkill -x 程式名` 或記下 PID）。

## 檔案編輯工具

- Write / Edit 工具會把內容裡的 `\uXXXX` 轉成真正的字元（例如 `"﻿"` 變成 BOM，Go 編譯失敗）。
  程式碼裡需要特殊字元的跳脫寫法時，寫完用 `rg` / `od -c` 確認，或用 `sd -F` 改成 `\u` 形式。
- Go 的 `"\x85"` 是**一個位元組**，不是 U+0085；Unicode 字元一律寫 `"\u0085"`。

## 外部程式

- yt-dlp 單一執行檔（PyInstaller）會再開子程序；`exec.CommandContext` 只結束最上層，子程序會留著並佔住輸出管線，
  讀輸出的程式就卡住。一律用 `nas/internal/proc.Command`（自成 process group、取消時整組結束、`WaitDelay`）。

## 測試

- 測試裡直接組 `config.Config` 不會套用 Parse 的預設值：路徑欄位是空字串時會寫到目前目錄。`app.New` 自己補預設值。
- SSE 這類長連線要在 `httptest.Server.Close()` 之前斷開，否則 Close 會一直等。

## 前端（Svelte 5）

- props 是即時的 getter：父元件把 `ui.editor` 清成 null 關掉對話框後，元件裡還在跑的 async 函式讀到的 `id` 也變成 null。
  「關閉後再送出請求」的元件，在建立時就把 id 記成常數（父元件用 `{#key}` 保證一個元件只對應一首歌）。
- 截圖前切換主題要等 CSS transition 跑完（按鈕有 `transition: background .15s`），否則會拍到白底白字的中間狀態，誤判成配色錯誤。
- 互動測試讀 toast 時要等文字「變了」再讀，否則會讀到上一個動作的訊息。

## 檔案權限

- `os.CreateTemp` 建立的檔案是 0600。會被硬連結 / clone 到其他地方給別人讀的檔案（匯出資料夾經 SMB 分享），寫完要 Chmod 0644。


## 溝通

- 和使用者的對話一律用繁體中文（台灣），包含長篇的階段報告；context 被壓縮後接續時也一樣，送出前先確認語言。

## Docker

- 空的 named volume 第一次掛載時，Docker 會把映像裡那個路徑的內容與**擁有者**複製進去（例如映像的 `/library` 是 root，volume 就變 root）。
  測試 PUID 時先在 volume 放一個檔案再 chown；bind mount 沒有這個行為。
- container 裡看不到主機的 `~/.gitconfig`：要 commit 的映像設系統層級的預設 `user.name` / `user.email`（repo 自己的設定仍然優先）。

## Shell（zsh）

- zsh 不會把 `$var` 拆成多個參數：`cmd $files` 會把整串當成一個參數。要拆就用陣列（`arr=(a b); cmd "${arr[@]}"`）或 Python 處理檔案清單。

## 重構

- 刪掉程式之前，先查有沒有正在跑的 container 用它當啟動指令（例如開發用 worker 的 `ai/worker.py`），刪了它會一啟動就結束。
- 「不改行為」的重構要先錄下重構前的輸出再動手（docs/deploy.md「worker 的 GPU 回歸比對」）；GPU 上不是位元確定的步驟（Demucs）
  先量「舊程式自己跑兩次」的差異當基準，不能直接要求逐位元相同。
- 對照新舊程式的雜訊（例如 ffmpeg 的 Broken pipe）時，用同一件任務分別跑新舊兩版比較，不要憑印象判斷是不是新增的。
