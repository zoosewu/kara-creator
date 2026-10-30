# setup.ps1、worker.ps1 共用：模型、快取都放在 worker 資料夾裡（.gitignore 已排除），不放到 C 槽的使用者資料夾。
# 和 Docker 映像用同樣的環境變數（deploy/ai.Dockerfile）；已經自己設定過的不動。
#
#   models\whisper       Whisper（XDG_CACHE_HOME）
#   models\torch         Demucs、MMS 對時模型（TORCH_HOME）
#   models\huggingface   HF_HOME
#   cache\               worker 的輸入檔、字型、暫存（KARA_CACHE）
#   cache\pip            pip 下載的安裝檔（PIP_CACHE_DIR）
$karaDirs = [ordered]@{
    XDG_CACHE_HOME = "models"
    TORCH_HOME     = "models\torch"
    HF_HOME        = "models\huggingface"
    KARA_CACHE     = "cache"
    PIP_CACHE_DIR  = "cache\pip"
}
foreach ($name in $karaDirs.Keys) {
    if (-not [Environment]::GetEnvironmentVariable($name)) {
        Set-Item "env:$name" (Join-Path $PSScriptRoot $karaDirs[$name])
    }
}
