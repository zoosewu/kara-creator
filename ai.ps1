# 啟動 AI 伺服器（去人聲、對時、對時檢查；預設 http://127.0.0.1:8770/）。Ctrl+C 結束。
# 歌曲伺服器（ui.ps1）會把這些工作交給它，兩個都要開著。
#
#   .\ai.ps1
#   .\ai.ps1 --lan --token 密碼     # 在另一台有顯示卡的電腦執行，開放區域網路連線
#                                    # 歌曲伺服器那邊：.\ui.ps1 --ai http://這台的位址:8770 --ai-token 密碼
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$Arguments
)

$python = Join-Path $PSScriptRoot ".venv\Scripts\python.exe"
if (-not (Test-Path $python)) {
    Write-Error "找不到虛擬環境：$python`n請先執行 setup.ps1"
    exit 1
}

& $python (Join-Path $PSScriptRoot "ai\server.py") @Arguments
exit $LASTEXITCODE
