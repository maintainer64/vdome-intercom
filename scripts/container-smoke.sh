#!/bin/sh
set -eu
cd -- "$(dirname -- "$0")/.."
image="${1:-vdome-intercom:local}"
docker run --rm --entrypoint ffmpeg "$image" -version
docker run --rm --entrypoint sh "$image" -ec '
  ! command -v node
  ! command -v go
  ! command -v gcc
  test "$(id -u)" = 10001
'
docker run --rm -i --entrypoint sh "$image" -ec '
  ffmpeg -hide_banner -loglevel error -nostdin -f mpegts -i pipe:0 \
    -map 0:v:0 -an -c:v copy -f hls -hls_time 2 -hls_list_size 4 \
    -hls_flags delete_segments+temp_file+independent_segments \
    -hls_segment_filename /tmp/segment-%09d.ts /tmp/index.m3u8
  grep -q "#EXTINF:" /tmp/index.m3u8
  test -s /tmp/segment-000000000.ts
  echo "Runtime H.264 to HLS smoke test passed"
' < internal/media/testdata/h264.ts
