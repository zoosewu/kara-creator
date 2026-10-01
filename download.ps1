# 下載：參數直接轉給 scripts/download.py，並使用專案的虛擬環境。
#
#   .\download.ps1 "https://www.youtube.com/watch?v=..."
#   .\download.ps1 --batch urls.txt
#   .\download.ps1 --list
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$Arguments
)

$python = Join-Path $PSScriptRoot ".venv\Scripts\python.exe"
if (-not (Test-Path $python)) {
    Write-Error "找不到虛擬環境：$python`n請依 README 建立 .venv"
    exit 1
}

& $python (Join-Path $PSScriptRoot "scripts\download.py") @Arguments
exit $LASTEXITCODE
