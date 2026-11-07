# Synthetic H.264 fixture

`h264.ts` is six seconds of solid green, 640×480 at 10 fps. It contains no
camera recordings or account data. Generated with:

```sh
ffmpeg -f lavfi -i color=c=green:s=640x480:r=10 -t 6 \
  -c:v libx264 -preset ultrafast -g 20 -pix_fmt yuv420p -f mpegts h264.ts
```

The container smoke test remuxes this fixture using the exact runtime FFmpeg.
This catches missing demuxers/muxers and musl worker-thread stack overflows.
The fixture is excluded from the Docker build context and runtime image.
