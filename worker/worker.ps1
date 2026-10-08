# 在 Windows 上直接執行 AI worker（環境先用 setup.ps1 準備好）。
#
#   .\worker.ps1 --nas http://mac-mini.local:8765            # 名稱預設是電腦名稱
#   .\worker.ps1 --nas http://mac-mini.local:8765 --name pc-4070 --token 密碼
#   .\worker.ps1 --help                                      # 其他參數
$ErrorActionPreference = "Stop"
$env:PATH = (Join-Path $PSScriptRoot "tools\ffmpeg\bin") + [IO.Path]::PathSeparator + $env:PATH
$exe = Join-Path $PSScriptRoot ".venv\Scripts\kara-worker.exe"
if (-not (Test-Path $exe)) {
    Write-Host "還沒安裝 kara-worker：請先執行 $(Join-Path $PSScriptRoot 'setup.ps1')（第一次會下載 PyTorch，需要一點時間）" -ForegroundColor Yellow
    exit 1
}
& $exe @args
exit $LASTEXITCODE
