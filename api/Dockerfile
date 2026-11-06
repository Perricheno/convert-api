FROM golang:1.24-bookworm AS builder
WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -o /filee-service main.go
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl libicu72 libfontconfig1 libfreetype6 libgl1 libx11-6 libxrender1 libxext6 libreoffice-core libreoffice-writer libreoffice-calc libreoffice-impress pandoc ffmpeg ghostscript fonts-dejavu-core && rm -rf /var/lib/apt/lists/*
ARG FILEE_VERSION=2.3.0
ARG FILEE_SHA256=2f3cccc927fd9276acf0d08dfb16203d57cfd1e3025659403de5c6f247e1ea9b
RUN mkdir -p /opt/filee && curl -fsSL https://github.com/KnifeLemon/Filee/releases/download/v${FILEE_VERSION}/Filee-${FILEE_VERSION}-linux-x64.tar.gz -o /tmp/filee.tar.gz && echo "${FILEE_SHA256}  /tmp/filee.tar.gz" | sha256sum -c - && tar -xzf /tmp/filee.tar.gz -C /opt/filee --strip-components=1 && rm /tmp/filee.tar.gz
# Filee requires newer FFmpeg options than Debian bookworm's 5.x build.
# This is the pinned Linux engine from Filee's upstream engines.json.
ARG FFMPEG_URL=https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-09-30-13-08/ffmpeg-n9.0.2-17-g2a571b6068-linux64-gpl-9.0.tar.xz
ARG FFMPEG_SHA256=68ee646831adaae2495618346f3bba94ff207ff83bbd34d643e7004730d66269
RUN apt-get update && apt-get install -y --no-install-recommends xz-utils && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /opt/filee/engines/ffmpeg \
    && curl -fsSL "$FFMPEG_URL" -o /tmp/ffmpeg.tar.xz \
    && echo "$FFMPEG_SHA256  /tmp/ffmpeg.tar.xz" | sha256sum -c - \
    && tar -xJf /tmp/ffmpeg.tar.xz -C /opt/filee/engines/ffmpeg --strip-components=1 --wildcards '*/bin/ffmpeg' '*/bin/ffprobe' '*/LICENSE.txt' \
    && rm /tmp/ffmpeg.tar.xz
COPY --from=builder /filee-service /usr/local/bin/filee-service
RUN useradd -m -u 10001 converter
USER converter
ENV HOME=/home/converter
EXPOSE 8080
CMD ["filee-service"]
