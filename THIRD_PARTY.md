# FFmpeg in the container

The runtime includes a reduced FFmpeg 9.0.2 build for RTSP/HTTP to HLS remuxing.
It contains no encoders and links OpenSSL dynamically. FFmpeg is distributed
under LGPL v3 or later; see `/usr/share/licenses/ffmpeg/COPYING.LGPLv3` in
the image. This project has not modified the upstream FFmpeg source.

Exact corresponding source:
https://github.com/FFmpeg/FFmpeg/archive/refs/tags/n9.0.2.tar.gz

SHA-256: `6e374ed621e48faa40639307dff48ba6fe574a509977956d2cce9669b7cc27e9`

The full build configuration and steps are in [Dockerfile](Dockerfile).
Rebuild the image with `docker build -t vdome-intercom .` to reproduce or
replace the FFmpeg executable. Other dependencies retain their own licenses.
