# 在 Windows 上直接執行 AI worker（不用 Docker）時的環境準備。可以重複執行；更新程式（git pull）後也再執行一次。
#
#   .\setup.ps1
#
# 需要：NVIDIA 顯示卡（CUDA 13 驅動）、Python 3.14（winget install Python.Python.3.14）。
param(
    [string]$PythonVersion = "3.14",
    [string]$Cuda = "cu130"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"   # 關掉進度條，Invoke-WebRequest 會快很多
$root = $PSScriptRoot

# fnm（Node.js 版本管理）每個 shell 都在 AppData\Local\fnm_multishells 建一個連結點並加進 PATH；
# pip 經過它時 Windows 會判定是「未受信任的掛接點」而失敗（WinError 448）。安裝時先從 PATH 拿掉（只影響這次執行）。
$env:PATH = ($env:PATH -split [IO.Path]::PathSeparator | Where-Object { $_ -and $_ -notmatch 'fnm_multishells' }) -join [IO.Path]::PathSeparator

# ---- ffmpeg（gyan.dev 的 essentials 版，含 NVENC）：tools\ffmpeg\bin ----------------
$ffmpegBin = Join-Path $root "tools\ffmpeg\bin"
if (Test-Path (Join-Path $ffmpegBin "ffmpeg.exe")) {
    Write-Host "ffmpeg 已存在，略過"
} else {
    Write-Host "安裝 ffmpeg"
    $zip = Join-Path ([IO.Path]::GetTempPath()) "ffmpeg-essentials.zip"
    Invoke-WebRequest "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip" -OutFile $zip
    $tmp = Join-Path ([IO.Path]::GetTempPath()) "ffmpeg-essentials"
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
    Expand-Archive $zip $tmp
    $top = Get-ChildItem $tmp -Directory | Select-Object -First 1
    New-Item -ItemType Directory -Force (Join-Path $root "tools\ffmpeg") | Out-Null
    Copy-Item (Join-Path $top.FullName "bin") (Join-Path $root "tools\ffmpeg") -Recurse -Force
    Copy-Item (Join-Path $top.FullName "LICENSE") (Join-Path $root "tools\ffmpeg") -Force
    Remove-Item $tmp, $zip -Recurse -Force
}

# ---- Python 環境（.venv）：PyTorch CUDA 版 + kara-worker（editable，git pull 後直接生效）------
$python = Join-Path $root ".venv\Scripts\python.exe"
if (-not (Test-Path $python)) {
    Write-Host "建立 Python $PythonVersion 環境"
    py "-$PythonVersion" -m venv (Join-Path $root ".venv")
    & $python -m pip install --upgrade pip
    & $python -m pip install torch==2.11.0 torchaudio==2.11.0 --index-url "https://download.pytorch.org/whl/$Cuda"
}
Write-Host "安裝 kara-worker 與相依套件"
& $python -m pip install -e $root -c (Join-Path $root "constraints.txt")
if ($LASTEXITCODE -ne 0) { throw "安裝失敗" }

Write-Host ""
Write-Host "完成。啟動：.\worker.ps1 --nas http://NAS的位址:8765 --name 這台的名稱 [--token 密碼]"
