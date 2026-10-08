# 在 Windows 上直接執行 AI worker（環境先用 setup.ps1 準備好）。
#
#   .\worker.ps1 --nas http://mac-mini.local:8765            # 名稱預設是電腦名稱
#   .\worker.ps1 --nas http://mac-mini.local:8765 --name pc-4070 --token 密碼
#   .\worker.ps1 --help                                      # 其他參數
$ErrorActionPreference = "Stop"
$env:PATH = (Join-Path $PSScriptRoot "tools\ffmpeg\bin") + [IO.Path]::PathSeparator + $env:PATH
& (Join-Path $PSScriptRoot ".venv\Scripts\kara-worker.exe") @args
exit $LASTEXITCODE
