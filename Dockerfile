# syntax=docker/dockerfile:1
ARG ALPINE_VERSION=3.24

FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w -buildid=' -o /out/vdome ./cmd/server

# Only RTSP/HTTP -> HLS remuxing. No encoders, GUI, audio, docs or tools.
FROM alpine:${ALPINE_VERSION} AS ffmpeg
ARG FFMPEG_VERSION=9.0.2
ARG FFMPEG_SHA256=6e374ed621e48faa40639307dff48ba6fe574a509977956d2cce9669b7cc27e9
RUN apk add --no-cache build-base linux-headers openssl-dev pkgconf curl xz
WORKDIR /src
RUN curl -fL --connect-timeout 15 --max-time 90 --retry 3 "https://github.com/FFmpeg/FFmpeg/archive/refs/tags/n${FFMPEG_VERSION}.tar.gz" -o ffmpeg.tar.gz \
    && echo "${FFMPEG_SHA256}  ffmpeg.tar.gz" | sha256sum -c - \
    && tar -xzf ffmpeg.tar.gz --strip-components=1
# musl defaults to a small thread stack; FFmpeg demux workers need more.
RUN ./configure \
    --prefix=/opt/ffmpeg --disable-everything --disable-autodetect \
    --disable-doc --disable-debug --disable-x86asm --disable-ffplay --disable-ffprobe \
    --disable-avdevice --disable-swscale --disable-swresample \
    --disable-iconv --disable-zlib --disable-bzlib --disable-lzma \
    --enable-small --enable-network --enable-openssl --enable-version3 \
    --enable-protocol=file,pipe,tcp,udp,rtp,http,https,httpproxy,tls,crypto \
    --enable-demuxer=concat,rtsp,rtp,sdp,hls,mpegts,mov \
    --enable-muxer=hls,mpegts --enable-parser=h264,hevc,aac \
    --enable-bsf=h264_mp4toannexb,hevc_mp4toannexb,aac_adtstoasc \
    --extra-cflags='-Os -ffunction-sections -fdata-sections' \
    --extra-ldflags='-Wl,--gc-sections,-z,stack-size=2097152' \
    && make -j"$(getconf _NPROCESSORS_ONLN)" \
    && make install

FROM alpine:${ALPINE_VERSION} AS runtime
RUN apk add --no-cache ca-certificates libssl3 libcrypto3 \
    && addgroup -S -g 10001 vdome \
    && adduser -S -D -H -u 10001 -G vdome vdome \
    && mkdir -p /app/data /usr/share/licenses/ffmpeg \
    && chown 10001:10001 /app/data
WORKDIR /app
COPY --from=server /out/vdome /usr/local/bin/vdome
COPY --from=web /src/web/dist ./web/dist
COPY --from=ffmpeg /opt/ffmpeg/bin/ffmpeg /usr/local/bin/ffmpeg
COPY --from=ffmpeg /src/COPYING.LGPLv* /usr/share/licenses/ffmpeg/
COPY THIRD_PARTY.md /usr/share/licenses/ffmpeg/SOURCE.md
ENV VDOME_ADDR=0.0.0.0:8080 VDOME_ORIGIN=http://127.0.0.1:8080 VDOME_PROVIDER=live
USER 10001:10001
EXPOSE 8080
VOLUME ["/app/data"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["vdome"]
