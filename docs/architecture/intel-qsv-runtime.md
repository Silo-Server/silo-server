# Intel QSV runtime packaging

The runtime image installs `jellyfin-ffmpeg8`, which ships both Intel runtimes
under `/usr/lib/jellyfin-ffmpeg/lib`: the legacy Media SDK runtime
(`libmfxhw64.so*`) and the modern VPL GPU runtime (`libmfx-gen.so*`). FFmpeg
links only the `libvpl` dispatcher, which picks one of the two at run time.

The Dockerfile's `INTEL_QSV_RUNTIME` build argument chooses what the image keeps:

- `auto` (the default) leaves the package as installed. Older Intel graphics
  that only Media SDK supports keep working.
- `vpl` (amd64 only) deletes `libmfxhw64.so*`, so the dispatcher can only load
  the VPL runtime. It keeps the dispatcher, `libmfx-gen`, the iHD driver and
  their dependencies. On an isolated, non-first Intel Flex render device the
  dispatcher was measured selecting Media SDK 1.35, which then failed QSV device
  initialization; without it, the dispatcher selected VPL 2.17 and encoded.

`scripts/configure-intel-qsv-runtime.sh` does the selection and fails the build
on any other value, on `vpl` for another architecture, and when the package no
longer ships `libmfx-gen.so.*`, so a packaging change cannot produce an image
with no usable QSV runtime. The image's `io.silo.intel-qsv-runtime` label
records the choice. CI runs `scripts/tests/intel-qsv-runtime.sh` on every change;
it builds only the `auto` image.

The default stays `auto` because Intel's
[dispatcher hardware matrix](https://github.com/intel/libvpl#dispatcher-behavior-when-targeting-intel-gpus)
lists platforms that only Media SDK supports. Every QSV command still initializes
hardware through `tonemap.QSVInitDeviceArgs`; the build argument changes which
runtime library is present, not how devices are opened or assigned.
