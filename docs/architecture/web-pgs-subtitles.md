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
`sidecar_bitmap` stays `false`: the server has no sidecar representation for
external bitmap files, DVD (VobSub) or DVB bitmaps, so those are still burned
in.

## Rendering

- `web/src/player/utils/pgs.ts` parses the `.sup` stream: segments, palettes
  (YCbCr to RGBA, BT.709 for HD and BT.601 for SD), run-length objects,
  windows, object cropping and composition states. It parses incrementally
  and looks up the display set active at a media time.
- `web/src/player/hooks/usePGSSubtitles.ts` fetches positioned windows
  (`windowed=1&position=&duration=`) through the same window planner as the
  text-subtitle fetcher (`web/src/player/utils/subtitleWindows.ts`), so seeks
  far into a file do not download the whole track.
- Each composition is drawn on a canvas that spans the player, scaled from the
  PGS video size onto the picture rectangle (`web/src/player/utils/videoFit.ts`)
  for both contain and cover fits. Subtitles authored in the letterbox stay
  there. Compositions in the lower half rise above the control bar like text
  cues.
- The subtitle delay and the stream's timeline origin apply as they do for text
  cues.
- A plan whose subtitle mode is still `burn_in` is left to the server, and the
  overlay stays off so the subtitle is not drawn twice.
