# v2 的 AI worker：主動連到 NAS 伺服器領任務（docs/v2/worker-protocol.md）。
#
#   .\worker.ps1 --nas http://mac-mini.local:8765            # 名稱預設是電腦名稱
#   .\worker.ps1 --nas http://mac-mini.local:8765 --name pc-4070 --token 密碼
#
# 環境用 setup.ps1 建好的 .venv（和 v1 相同）；v1 的 ai.ps1 在搬家完成前保留。
$ErrorActionPreference = "Stop"
$python = Join-Path $PSScriptRoot ".venv\Scripts\python.exe"
& $python (Join-Path $PSScriptRoot "ai\worker.py") @args
exit $LASTEXITCODE
