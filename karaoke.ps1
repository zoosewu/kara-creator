# KTV 字幕：參數直接轉給 scripts/karaoke.py，並使用專案的虛擬環境。
#
#   .\karaoke.ps1                   # 處理所有有歌詞的下載項目
#   .\karaoke.ps1 v-WcMQbXbKY       # 指定影片 id 或標題關鍵字
#   .\karaoke.ps1 --list            # 查看狀態與歌詞應放的位置
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$Arguments
)

$python = Join-Path $PSScriptRoot ".venv\Scripts\python.exe"
if (-not (Test-Path $python)) {
    Write-Error "找不到虛擬環境：$python`n請依 README 建立 .venv"
    exit 1
}

& $python (Join-Path $PSScriptRoot "scripts\karaoke.py") @Arguments
exit $LASTEXITCODE
