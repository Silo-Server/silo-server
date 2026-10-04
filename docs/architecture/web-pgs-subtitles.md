# Web PGS subtitles

The web player draws embedded PGS (Blu-ray bitmap) subtitles itself instead of
asking the server to burn them into a transcode. Paths are
repository-relative.

## Contract

The web player declares `embedded_bitmap: true` on every delivery class
(`web/src/player/client-context-v3.ts`). The server then keeps the plan's
delivery and publishes the embedded PGS track as a lossless `.sup` sidecar
(`subtitle.mode: "render"`), the same way it publishes embedded text tracks
(see [playback-protocol-v3.md](playback-protocol-v3.md) §8).
`embedded_bitmap` covers embedded PGS only. The server still burns in embedded
DVD (VobSub) and DVB bitmaps, and refuses external bitmap files
(`subtitle_burn_in_source_unsupported`). `sidecar_bitmap` stays `false`.

Auto-select and the initial start ask the same question through
`subtitleNeedsBurnIn` (`web/src/player/utils/subtitleCodecs.ts`): embedded
PGS does not need the server, so it is not ranked as a burn-in and a refused
start is not blamed on it.

## Rendering

- `web/src/player/utils/pgs.ts` parses the `.sup` stream: segments, palettes
  (YCbCr to RGBA, BT.709 for HD and BT.601 for SD), run-length objects,
  windows, object cropping and composition states. It parses incrementally
  and looks up the display set active at a media time.
- Palettes and objects persist through an epoch, so a display set can place
  objects an earlier one defined. A display set whose objects or palette the
  parser never saw is dropped, never treated as a clear.
- `web/src/player/hooks/usePGSSubtitles.ts` fetches positioned windows
  (`windowed=1&position=&duration=`) through the same window planner as the
  text-subtitle fetcher (`web/src/player/utils/subtitleWindows.ts`), so seeks
  far into a file do not download the whole track. An extend window keeps the
  parser's epoch state; a fresh window after a seek starts a new parser, so a
  screen whose epoch began before the window shows from the next epoch start.
- A screen stays up until the next display set replaces it, but never past the
  loaded windows: when no window is on the wire, nothing is drawn beyond the
  last one that finished. Screens more than one window behind the playhead are
  dropped.
- Each composition is drawn on a canvas that spans the player, scaled from the
  PGS video size onto the picture rectangle (`web/src/player/utils/videoFit.ts`)
  for both contain and cover fits. The plane matches the picture along its
  uncropped side, so subtitles authored in a letterbox or pillarbox stay
  there. Objects stay inside the player, shrinking when a Fill crop leaves
  less width than they need, and lower-half objects stay above the control
  bar while it is up, whatever the text position setting says.
- The subtitle delay and the stream's timeline origin apply as they do for text
  cues.
- A plan whose subtitle mode is still `burn_in` is left to the server, and the
  overlay stays off so the subtitle is not drawn twice.
