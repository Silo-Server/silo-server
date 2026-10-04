#!/usr/bin/env bash
# Rebuilds the browser engine's FFmpeg audio decoders as WebAssembly.
#
# Output: web/src/player/engine/wasm/silo-audio-decoders.{mjs,wasm}. Both are
# committed so the web build never needs Emscripten; rerun this script only to
# change the FFmpeg revision, the decoder set, or bridge.c.
#
# Requires Docker. Commands assume the repository root is the cwd:
#   web/wasm/audio-decoders/build.sh
set -euo pipefail

FFMPEG_TAG="${FFMPEG_TAG:-n8.1.3}"
EMSDK_IMAGE="${EMSDK_IMAGE:-emscripten/emsdk:6.0.11}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out_dir="$(cd "${here}/../../src/player/engine/wasm" 2>/dev/null && pwd || true)"
if [[ -z "${out_dir}" ]]; then
  mkdir -p "${here}/../../src/player/engine/wasm"
  out_dir="$(cd "${here}/../../src/player/engine/wasm" && pwd)"
fi

docker run --rm \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp \
  -e FFMPEG_TAG="${FFMPEG_TAG}" \
  -v "${here}:/src:ro" \
  -v "${out_dir}:/out" \
  "${EMSDK_IMAGE}" \
  bash -euo pipefail -c '
    cd /tmp
    git clone --quiet --depth 1 --branch "${FFMPEG_TAG}" https://github.com/FFmpeg/FFmpeg.git ffmpeg
    cd ffmpeg
    emconfigure ./configure \
      --target-os=none \
      --arch=x86_32 \
      --enable-cross-compile \
      --disable-asm \
      --disable-x86asm \
      --disable-inline-asm \
      --disable-programs \
      --disable-doc \
      --disable-debug \
      --disable-all \
      --disable-everything \
      --disable-autodetect \
      --disable-pthreads \
      --disable-runtime-cpudetect \
      --enable-avcodec \
      --enable-decoder=ac3,eac3,dca,truehd,mlp \
      --cc=emcc \
      --cxx=em++ \
      --ar=emar \
      --ranlib=emranlib \
      --nm=emnm \
      --extra-cflags="-DNDEBUG -O3 -flto -msimd128" \
      --extra-ldflags="-O3 -flto" >/dev/null
    emmake make -j"$(nproc)" >/dev/null
    emcc /src/bridge.c \
      libavcodec/libavcodec.a \
      libavutil/libavutil.a \
      -I. \
      -O3 -flto -msimd128 \
      -s MODULARIZE=1 \
      -s EXPORT_ES6=1 \
      -s EXPORT_NAME=createSiloAudioDecoders \
      -s ALLOW_MEMORY_GROWTH=1 \
      -s ENVIRONMENT=web,worker,node \
      -s FILESYSTEM=0 \
      -s MALLOC=emmalloc \
      -s SUPPORT_LONGJMP=0 \
      -s EXPORTED_RUNTIME_METHODS=HEAPU8,HEAPF32 \
      -s EXPORTED_FUNCTIONS=_malloc,_free \
      -o /out/silo-audio-decoders.mjs
    git rev-parse HEAD > /out/silo-audio-decoders.ffmpeg-revision
  '

echo "Built ${out_dir}/silo-audio-decoders.{mjs,wasm} from FFmpeg ${FFMPEG_TAG}"
