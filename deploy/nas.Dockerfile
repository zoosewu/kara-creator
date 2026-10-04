# NAS 伺服器（Mac 的 OrbStack 與 Linux NAS 共用；linux/amd64、linux/arm64）。
#
#   docker build -f deploy/nas.Dockerfile -t kara-nas .
#   docker buildx build --platform linux/arm64 -f deploy/nas.Dockerfile -t kara-nas .   # Mac（M1）
#
# 前端與 Go 在建置機器的架構上編譯（交叉編譯，不必模擬）；只有最後一層是目標架構。
# 執行方式見 deploy/compose.nas.yml。

# ---- 前端 ----
FROM --platform=$BUILDPLATFORM node:24-trixie-slim AS web
WORKDIR /src/nas/web
COPY nas/web/package.json nas/web/package-lock.json ./
RUN npm ci
COPY nas/web ./
RUN npm run build

# ---- Go ----
FROM --platform=$BUILDPLATFORM golang:1.27-trixie AS go
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY versions.json versions.go ./
COPY nas ./nas
COPY --from=web /src/nas/internal/api/dist ./nas/internal/api/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/kara-nas ./nas/cmd/kara-nas

# ---- yt-dlp（單一執行檔；伺服器每天自己更新）與預設字型 ----
FROM debian:trixie-slim AS fetch
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*
RUN case "$TARGETARCH" in amd64) f=yt-dlp_linux ;; arm64) f=yt-dlp_linux_aarch64 ;; *) echo "不支援 $TARGETARCH" >&2; exit 1 ;; esac \
    && curl -fsSL -o /yt-dlp "https://github.com/yt-dlp/yt-dlp/releases/latest/download/$f" && chmod 755 /yt-dlp

# ---- 執行 ----
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git openssh-client tini tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && git config --system safe.directory '*' \
    && git config --system user.name kara-nas && git config --system user.email kara-nas@localhost \
    && mkdir -p /library /home/kara && chmod 777 /home/kara
COPY --from=mwader/static-ffmpeg:8.0 /ffmpeg /ffprobe /usr/local/bin/
COPY --from=denoland/deno:bin /deno /opt/kara/tools/deno
COPY --from=fetch /yt-dlp /opt/kara/tools/yt-dlp
# Noto Sans CJK Bold（OFL）：各語言的預設字型；曲庫的 fonts/ 有同一個檔案時用曲庫的
COPY --from=fetch /usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc /opt/kara/fonts/
COPY --from=go /out/kara-nas /usr/local/bin/kara-nas
COPY deploy/nas-entrypoint.sh /usr/local/bin/nas-entrypoint.sh

ENV KARA_LIBRARY=/library \
    KARA_TOOLS=/opt/kara/tools \
    KARA_FONTS=/opt/kara/fonts \
    KARA_LISTEN=:8765 \
    HOME=/home/kara
EXPOSE 8765
VOLUME ["/library"]
ENTRYPOINT ["tini", "--", "nas-entrypoint.sh"]
