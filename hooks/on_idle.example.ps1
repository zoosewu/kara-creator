# 佇列清空（或曲庫有變動、安靜 30 秒後）會執行 hooks\on_idle.ps1。
# 把這個檔案複製成 on_idle.ps1 再修改；on_idle.ps1 不會進 git（裡面通常有你的伺服器位址）。
#
# 可用的環境變數：
#   SONG_HOOK_REASON     jobs_finished（佇列清空）/ library_changed（改了歌名、資料夾、歌詞或時間）
#   SONG_EXPORT_DIR      伴唱帶成品資料夾（output\export，結構和曲庫相同、檔名「歌手 - 歌名.mp4」）
#   SONG_OUTPUT_DIR      輸出根目錄
#   SONG_DATA_DIR        資料備份（data\）
#   SONG_HOOK_SUMMARY    這一輪的結果 JSON 檔（每件工作的歌名、步驟、狀態）
#   SONG_JOBS_DONE / SONG_JOBS_FAILED / SONG_JOBS_CANCELLED   這一輪各狀態的件數
#
# 輸出會顯示在 UI 處理佇列的「收尾」紀錄裡；結束代碼不是 0 時會標成失敗。超過 30 分鐘會被中斷。

$ErrorActionPreference = "Stop"

# 例一：用 WSL 的 rsync 同步伴唱帶到 NAS（.export.json 是程式的管理紀錄，不必同步）
# $src = (wsl wslpath -a ($env:SONG_EXPORT_DIR -replace '\\', '/')).Trim() + "/"
# wsl rsync -av --delete --exclude ".export.json" "$src" "user@nas:/volume1/karaoke/"

# 例二：用 Windows 內建的 robocopy 鏡像到網路磁碟
# robocopy $env:SONG_EXPORT_DIR "\\nas\karaoke" /MIR /XF .export.json /NFL /NDL /NP
# if ($LASTEXITCODE -lt 8) { $global:LASTEXITCODE = 0 }   # robocopy 的 1~7 都代表成功

Write-Output "hook：$env:SONG_HOOK_REASON（完成 $env:SONG_JOBS_DONE、失敗 $env:SONG_JOBS_FAILED）"
