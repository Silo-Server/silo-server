# Audio decoders for the browser decode engine

FFmpeg's AC-3, E-AC-3, DTS (including DTS-HD MA), TrueHD and MLP decoders,
compiled to WebAssembly for the web player's browser decode engine
(see `docs/architecture/web-decode-engine.md`).

- `bridge.c` is the C interface the engine's worker calls: open a decoder,
  send a packet, receive frames converted to planar float32 with their channel
  mask and timestamp.
- `build.sh` builds FFmpeg at a pinned release with only those decoders and
  links the bridge, using the `emscripten/emsdk` Docker image. It writes
  `web/src/player/engine/wasm/silo-audio-decoders.{mjs,wasm}` and the FFmpeg
  commit to `silo-audio-decoders.ffmpeg-revision`.

The outputs are committed, so `pnpm build` never needs Emscripten. Rebuild only
when changing the FFmpeg tag, the decoder set, or `bridge.c`, then run the
decoder tests:

```sh
web/wasm/audio-decoders/build.sh
cd web && pnpm vitest run src/player/engine/ffmpegAudioDecoder.test.ts
```

FFmpeg is LGPL-2.1-or-later; see `THIRD_PARTY_NOTICES.md`.
