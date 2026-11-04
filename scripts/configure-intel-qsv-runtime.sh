#!/bin/sh
# Image-build selection: keep the standard package for older Intel hardware;
# isolate modern Flex/DG2 VPL from the legacy Media SDK runtime when requested.
set -eu
qsv_runtime=${1-auto}
qsv_arch=${2:-}
qsv_lib_dir=${3:-/usr/lib/jellyfin-ffmpeg/lib}
case "$qsv_runtime" in
  auto) exit 0 ;;
  vpl) ;;
  *) printf '%s\n' 'INTEL_QSV_RUNTIME must be auto or vpl' >&2; exit 1 ;;
esac
if [ "$qsv_arch" != amd64 ]; then
  printf '%s\n' 'The VPL image option requires amd64 Intel graphics' >&2
  exit 1
fi
set -- "$qsv_lib_dir"/libmfx-gen.so.*
if [ ! -f "$1" ]; then
  printf '%s\n' 'The Jellyfin modern Intel VPL GPU runtime is missing' >&2
  exit 1
fi
# Keep the dispatcher, modern runtime, iHD driver and their dependencies.
# Removing symlinks as well as the versioned file prevents dispatcher fallback.
rm -f "$qsv_lib_dir"/libmfxhw64.so*
