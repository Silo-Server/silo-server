# Bitrate and resolution ladder

One table pairs a bitrate budget with the largest output resolution it encodes
well. It lives in `internal/playback/quality_ladder.go` (`bitrateLadder`).

## The table

The floors are H.264 bitrates at 30 fps or less. They follow Apple's HLS authoring
ladder and Jellyfin's `ResolutionNormalizer`.

| Class | 16:9 box  | Floor     |
| ----- | --------- | --------- |
| 2160p | 3840x2160 | 20 Mbps   |
| 1080p | 1920x1080 | 5 Mbps    |
| 720p  | 1280x720  | 2 Mbps    |
| 540p  | 960x540   | 1.2 Mbps  |
| 480p  | 854x480   | below that |

`LadderClassForBitrate` normalizes a budget before looking it up:

- above 30 fps it divides by the square root of `fps / 30`;
- HEVC and VP9 output divide it by 0.6, and AV1 by 0.5.

## Invariants

- **Never enlarge.** `FitLadderBox` keeps the source's aspect ratio inside the
  class box. A 3840x1600 film at 1080p is 1920x800, not 2592x1080. Decide "unchanged"
  by comparing the fitted size with the source size, never by class labels. The
  one exception is streaming's source-preserving route: a source whose height
  fits the class is sent as-is when its bitrate allows, even when it is wider
  than the box (a 2560x1080 film at 1080p). Once it is re-encoded, it is fitted.
- **Never exceed the source's bitrate.** A transcode's cap is the smaller of the
  budget and the source's bitrate converted by codec efficiency.
- **Device bounds step down, never up.** The class drops until the device's decoder
  takes the frame the encoder writes (an odd fit is evened first). A download
  answers `quality_unavailable` when no decoder takes even the smallest class.
  Streaming keeps H.264 as its universal output: an H.264 encode, scaled or a
  same-size conversion, steps down until an attested H.264 decoder takes its
  frame, stops at the smallest class if none does, and keeps its bitrate within
  that decoder's limit. As for downloads, a decoder takes a class only when its
  bitrate limit earns that class, and hardware decoders are preferred only when
  one takes the source's frame rate. A reported rate holds at the decoder's
  largest size, so a smaller class may exceed it within the same pixel rate:
  a 4K30 decoder takes 1080p60, and a 1080p30 decoder takes 720p60.
- **Transcodes stop at 2160p.** A source taller than the top class that keeps
  its own frame on the original route is fitted into the 2160p box when it has
  to be re-encoded.
- **Caps are ceilings.** Every encoder treats the cap as `-maxrate`, not as a
  constant-bitrate target (`appendCappedVBRArgs`).
- **Downloads store the class, not the height.** An artifact records the class
  label (`1080p`) for manifests and access checks. `DownloadScaleResolution`
  derives the exact encoder height from the file when the job runs.

## Consumers

- Download presets: `playback.ResolveDownloadTranscodeTarget`, reached from
  `downloads.DownloadQualityResolver`. Presets and their labels are in
  [docs/downloads-api.md](../downloads-api.md).
- Automatic streaming quality: `playback.ResolveQualityPolicyV3` for `auto`, the
  plain resolution labels, and bandwidth caps; see
  [playback-protocol-v3.md](playback-protocol-v3.md). Explicit menu rungs
  (`ladderRungsV3`) are user choices and keep their own table.
- jellycompat: `compatTargetResolutionForBitrate` turns a Jellyfin client's
  maximum streaming bitrate into the encoder height.
