# 第一次使用（或換電腦）時執行：下載外部工具、建立 Python 環境、取回資料備份。
# 已經有的部分會略過，可以重複執行。
#
#   .\setup.ps1                 # 全部
#   .\setup.ps1 -SkipPython     # 只下載工具
#   .\setup.ps1 -DataRepo ""    # 不取回資料備份
param(
    [switch]$SkipPython,
    [string]$DataRepo = "git@github.com:zoosewu/kara-creator-data.git",
    [string]$PythonVersion = "3.14",
    [string]$Cuda = "cu130"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"   # 關掉進度條，Invoke-WebRequest 會快很多
$root = $PSScriptRoot
$tools = Join-Path $root "tools"

function Get-Zip([string]$url, [string]$name) {
    $zip = Join-Path ([IO.Path]::GetTempPath()) $name
    Write-Host "  下載 $url"
    Invoke-WebRequest $url -OutFile $zip
    return $zip
}

# ---- ffmpeg（gyan.dev 的 essentials 版本）：tools\ffmpeg\bin --------------------
$ffmpegBin = Join-Path $tools "ffmpeg\bin"
if (Test-Path (Join-Path $ffmpegBin "ffmpeg.exe")) {
    Write-Host "ffmpeg 已存在，略過"
} else {
    Write-Host "安裝 ffmpeg"
    $zip = Get-Zip "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip" "ffmpeg-essentials.zip"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) "ffmpeg-essentials"
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
    Expand-Archive $zip $tmp
    $top = Get-ChildItem $tmp -Directory | Select-Object -First 1
    New-Item -ItemType Directory -Force (Join-Path $tools "ffmpeg") | Out-Null
    Copy-Item (Join-Path $top.FullName "bin") (Join-Path $tools "ffmpeg") -Recurse -Force
    Copy-Item (Join-Path $top.FullName "LICENSE") (Join-Path $tools "ffmpeg") -Force
    Remove-Item $tmp, $zip -Recurse -Force
}

# ---- deno（yt-dlp 解析 YouTube 需要的 JavaScript runtime）：tools\deno ------------
$denoDir = Join-Path $tools "deno"
if (Test-Path (Join-Path $denoDir "deno.exe")) {
    Write-Host "deno 已存在，略過"
} else {
    Write-Host "安裝 deno"
    $zip = Get-Zip "https://github.com/denoland/deno/releases/latest/download/deno-x86_64-pc-windows-msvc.zip" "deno.zip"
    New-Item -ItemType Directory -Force $denoDir | Out-Null
    Expand-Archive $zip $denoDir -Force
    Remove-Item $zip -Force
}

# ---- Python 環境（.venv）：PyTorch CUDA 版 + requirements.txt --------------------
$python = Join-Path $root ".venv\Scripts\python.exe"
if ($SkipPython) {
    Write-Host "略過 Python 環境"
} elseif (Test-Path $python) {
    Write-Host ".venv 已存在，略過（要重建請先刪除 .venv）"
} else {
    Write-Host "建立 Python $PythonVersion 環境"
    py "-$PythonVersion" -m venv (Join-Path $root ".venv")
    & $python -m pip install --upgrade pip
    & $python -m pip install torch==2.11.0 torchaudio==2.11.0 --index-url "https://download.pytorch.org/whl/$Cuda"
    & $python -m pip install -r (Join-Path $root "requirements.txt") -c (Join-Path $root "constraints.txt")
}

# ---- 資料備份（私人 repo）：data\ ------------------------------------------------
$data = Join-Path $root "data"
if (-not $DataRepo) {
    Write-Host "略過資料備份"
} elseif (Test-Path (Join-Path $data ".git")) {
    Write-Host "data\ 已存在，略過（更新請在 data\ 裡 git pull）"
} else {
    Write-Host "取回資料備份 $DataRepo"
    git clone $DataRepo $data
    if ($LASTEXITCODE -ne 0) { Write-Warning "取回資料備份失敗（沒有權限或網路問題），之後可以再執行一次" }
}

Write-Host ""
Write-Host "完成。啟動 UI：.\ui.ps1（區域網路：.\ui.ps1 --lan）"
Write-Host "從資料備份重建曲庫：.\.venv\Scripts\python.exe scripts\restore.py --make"
