#!/bin/sh
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
lib_dir="$scratch/lib"
mkdir "$lib_dir"
printf legacy > "$lib_dir/libmfxhw64.so.1.35"
ln -s libmfxhw64.so.1.35 "$lib_dir/libmfxhw64.so.1"
printf modern > "$lib_dir/libmfx-gen.so.1.2.17"
printf dispatcher > "$lib_dir/libvpl.so.2"
printf driver > "$lib_dir/iHD_drv_video.so"
sh "$root/scripts/configure-intel-qsv-runtime.sh" auto amd64 "$lib_dir"
test -f "$lib_dir/libmfxhw64.so.1.35"
if sh "$root/scripts/configure-intel-qsv-runtime.sh" '' amd64 "$lib_dir"; then
 printf 'Empty runtime selection was accepted\n' >&2
 exit 1
fi
if sh "$root/scripts/configure-intel-qsv-runtime.sh" wrong amd64 "$lib_dir"; then exit 1; fi
test -f "$lib_dir/libmfxhw64.so.1.35"
if sh "$root/scripts/configure-intel-qsv-runtime.sh" vpl arm64 "$lib_dir"; then exit 1; fi
test -f "$lib_dir/libmfxhw64.so.1.35"
sh "$root/scripts/configure-intel-qsv-runtime.sh" vpl amd64 "$lib_dir"
test ! -e "$lib_dir/libmfxhw64.so.1.35"
test ! -L "$lib_dir/libmfxhw64.so.1"
test "$(cat "$lib_dir/libmfx-gen.so.1.2.17")" = modern
test "$(cat "$lib_dir/libvpl.so.2")" = dispatcher
test "$(cat "$lib_dir/iHD_drv_video.so")" = driver
# A change in upstream packaging cannot silently produce an image with no VPL.
rm "$lib_dir/libmfx-gen.so.1.2.17"
if sh "$root/scripts/configure-intel-qsv-runtime.sh" vpl amd64 "$lib_dir"; then exit 1; fi
printf 'Intel QSV runtime packaging selection passed\n'
