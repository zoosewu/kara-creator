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
$pip = @("-m", "pip", "install", "--no-warn-script-location")
if (-not (Test-Path $python)) {
    Write-Host "建立 Python $PythonVersion 環境"
    py "-$PythonVersion" -m venv (Join-Path $root ".venv")
    & $python @pip --upgrade pip
    if ($LASTEXITCODE -ne 0) { throw "更新 pip 失敗" }
}

# 跑一段 Python，回傳 (成功與否, 輸出)。外部程式寫到 stderr 時，Windows PowerShell 5 在 Stop 模式會當成例外，所以暫時放寬
function Invoke-Py([string]$code) {
    $prev = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $out = & $python -c $code 2>&1 | Out-String
    $ok = $LASTEXITCODE -eq 0
    $ErrorActionPreference = $prev
    return @($ok, $out.Trim())
}

# PyTorch 一定要是 CUDA 版、而且 torchaudio 載入得了：每次都檢查。上次安裝中斷，或被其他套件換成 PyPI 的 CPU 版時重裝
$check = Invoke-Py "import torch, torchaudio; print(torch.version.cuda or '')"
if (-not $check[0] -or -not $check[1]) {
    Write-Host "安裝 PyTorch（CUDA 版：$Cuda）"
    & $python @pip --force-reinstall torch==2.11.0 torchaudio==2.11.0 --index-url "https://download.pytorch.org/whl/$Cuda"
    if ($LASTEXITCODE -ne 0) { throw "安裝 PyTorch 失敗" }
}

Write-Host "安裝 kara-worker 與相依套件"
& $python @pip -e $root -c (Join-Path $root "constraints.txt")
if ($LASTEXITCODE -ne 0) { throw "安裝失敗" }

# 最後確認：PyTorch 是 CUDA 版、torchaudio 載入得了、看得到顯示卡
$check = Invoke-Py @"
import torch, torchaudio
assert torch.version.cuda, 'PyTorch 不是 CUDA 版'
gpu = torch.cuda.get_device_name(0) if torch.cuda.is_available() else '偵測不到顯示卡（請確認 NVIDIA 驅動支援 CUDA 13）'
print(f'PyTorch {torch.__version__}、torchaudio {torchaudio.__version__}：{gpu}')
"@
if (-not $check[0]) { throw "PyTorch 或 torchaudio 載入失敗：$($check[1])" }
Write-Host $check[1]

Write-Host ""
Write-Host "完成。啟動：.\worker.ps1 --nas http://NAS的位址:8765 --name 這台的名稱 [--token 密碼]"
