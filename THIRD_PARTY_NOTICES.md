# Third-Party Notices

This repository vendors a small amount of third-party and generated asset
material needed for Silo server builds.

## Jellyfin Web

Silo does not bundle Jellyfin Web in this repository or in the default runtime
image. Administrators can explicitly install Jellyfin Web as a separate
compatibility component with `silo compat-web install` or the admin settings UI.
The installer records the upstream source URL, tag, commit SHA, checksum,
license, and source/provenance metadata beside the installed assets.

## Collection Template Posters

`web/public/images/collection-templates/` contains Silo-generated collection
poster artwork. The artwork is intended to use generic original scenes and
avoid copyrighted movie/show posters, recognizable actors, franchise
characters, provider logos, and readable in-image third-party branding.

## FFmpeg audio decoders (WebAssembly)

`web/src/player/engine/wasm/silo-audio-decoders.wasm` and its JavaScript glue
are compiled from FFmpeg (https://ffmpeg.org/), licensed under the GNU Lesser
General Public License, version 2.1 or later. The build enables only the
AC-3, E-AC-3, DTS, TrueHD and MLP decoders from `libavcodec` and `libavutil`,
with no GPL components. The FFmpeg release tag and configure options are in
`web/wasm/audio-decoders/build.sh`, the exact upstream commit is recorded in
`web/src/player/engine/wasm/silo-audio-decoders.ffmpeg-revision`, and
`web/wasm/audio-decoders/bridge.c` is the only code linked against it. The
corresponding FFmpeg source is available from the FFmpeg project at that tag,
and the build script reproduces the binary from it.

## Mediabunny

The web player's browser decode engine depends on Mediabunny
(https://github.com/Vanilagy/mediabunny), licensed under the Mozilla Public
License 2.0, through the `mediabunny` npm package.
