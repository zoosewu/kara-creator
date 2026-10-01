# 分離：參數直接轉給 scripts/separate.py，並使用專案的虛擬環境。
#
#   .\separate.ps1                  # 處理所有尚未分離的下載項目
#   .\separate.ps1 "D:\music\a.mp4" # 處理指定檔案
#   .\separate.ps1 --list
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$Arguments
)

$python = Join-Path $PSScriptRoot ".venv\Scripts\python.exe"
if (-not (Test-Path $python)) {
    Write-Error "找不到虛擬環境：$python`n請依 README 建立 .venv"
    exit 1
}

& $python (Join-Path $PSScriptRoot "scripts\separate.py") @Arguments
exit $LASTEXITCODE
