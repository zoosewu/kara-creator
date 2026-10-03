# 啟動歌曲伺服器（網頁 UI，http://127.0.0.1:8765/），會自動開啟瀏覽器。Ctrl+C 結束。
# 去人聲、對時等 AI 處理交給 AI 伺服器：另外執行 ai.ps1（預設在同一台電腦）。
#
#   .\ui.ps1
#   .\ui.ps1 --port 9000 --no-browser
#   .\ui.ps1 --lan                   # 開放區域網路裡的其他裝置（手機、其他電腦）連線
#   .\ui.ps1 --ai http://192.168.1.20:8770   # AI 伺服器在另一台電腦（--ai-token 對應它的 --token）
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$Arguments
)

$python = Join-Path $PSScriptRoot ".venv\Scripts\python.exe"
if (-not (Test-Path $python)) {
    Write-Error "找不到虛擬環境：$python`n請依 README 建立 .venv"
    exit 1
}

& $python (Join-Path $PSScriptRoot "ui\server.py") @Arguments
exit $LASTEXITCODE
