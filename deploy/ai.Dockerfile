# AI 伺服器（kara-worker，CUDA）。模型與快取放 volume，重建映像不必重新下載。
#
#   docker build -f deploy/ai.Dockerfile -t ghcr.io/zoosewu/kara-creator-ai .
#   執行方式見 deploy/compose.ai.yml（CI 會自動建好推到 ghcr.io）
#
# PyTorch 的 cu130 wheel 自帶 CUDA 函式庫，底層用一般的 Ubuntu 即可（驅動由 nvidia-container-runtime 掛進來）。
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    NVIDIA_DRIVER_CAPABILITIES=compute,video,utility \
    UV_PYTHON_INSTALL_DIR=/opt/python \
    VIRTUAL_ENV=/opt/venv \
    PATH=/opt/venv/bin:/opt/ffmpeg/bin:$PATH \
    XDG_CACHE_HOME=/models \
    TORCH_HOME=/models/torch \
    HF_HOME=/models/huggingface \
    KARA_CACHE=/cache \
    PYTHONUNBUFFERED=1

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl xz-utils tini \
    && rm -rf /var/lib/apt/lists/*

# ffmpeg：BtbN 的靜態版（含 NVENC；燒錄時 h264_nvenc 不能用會自動退回 libx264）
RUN mkdir -p /opt/ffmpeg && curl -fsSL https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linux64-gpl.tar.xz \
    | tar -xJ -C /opt/ffmpeg --strip-components=1 && rm -rf /opt/ffmpeg/doc /opt/ffmpeg/man

# Python 3.14 + PyTorch（CUDA 13.0）+ 其他套件
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv
# PyTorch 單獨一層（最大、最少變動）：只改其他套件或程式時不必重新下載與上傳
RUN uv venv -p 3.14 /opt/venv \
    && uv pip install --no-cache torch==2.11.0 torchaudio==2.11.0 --index-url https://download.pytorch.org/whl/cu130
# 相依套件（照 worker/pyproject.toml）單獨一層：只改程式時沿用
COPY worker/pyproject.toml worker/constraints.txt /src/worker/
RUN uv pip install --no-cache -r /src/worker/pyproject.toml -c /src/worker/constraints.txt \
       --extra-index-url https://download.pytorch.org/whl/cu130 --index-strategy unsafe-best-match

# worker 本身（versions.json 在 repo 根目錄，打包時帶進套件）
COPY versions.json /src/
COPY worker /src/worker
RUN uv pip install --no-cache --no-deps /src/worker && rm -rf /src

VOLUME ["/cache", "/models"]
ENTRYPOINT ["tini", "--", "kara-worker"]
