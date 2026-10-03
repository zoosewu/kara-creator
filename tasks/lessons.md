# 教訓

給之後接手的 AI 助手：踩過的坑與避免方法。

## 指令列

- **xh 在非互動的 shell 會把 stdin 當成 request body**，沒寫方法時自動變成 POST（伺服器回 405）。
  一律寫 `xh --ignore-stdin GET …`。
- 不要在指令裡單獨執行 `python`（沒有參數）：會進入互動模式等 stdin，整個指令卡住。
- 等程式結束用 `wait $PID`，不要用 `pgrep -f <路徑>`：`pgrep -f` 也會比對到執行 pgrep 的那個 shell 自己的指令列。

## 檔案編輯工具

- Write / Edit 工具會把內容裡的 `\uXXXX` 轉成真正的字元（例如 `"﻿"` 變成 BOM，Go 編譯失敗）。
  程式碼裡需要特殊字元的跳脫寫法時，寫完用 `rg` / `od -c` 確認，或用 `sd -F` 改成 `\u` 形式。
- Go 的 `"\x85"` 是**一個位元組**，不是 U+0085；Unicode 字元一律寫 `"\u0085"`。
