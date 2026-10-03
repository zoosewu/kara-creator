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
