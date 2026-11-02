/** Codecs that indicate ASS/SSA format subtitles with rich styling support. */
const ASS_CODECS = new Set(["ass", "ssa"]);

/** Returns true if the given subtitle codec is ASS/SSA format. */
export function isASSCodec(codec: string | undefined): boolean {
  if (!codec) return false;
  return ASS_CODECS.has(codec.toLowerCase());
}

/**
 * Codecs that indicate PGS (Blu-ray bitmap) subtitles. Embedded PGS is drawn
 * client-side from the server's `.sup` sidecar; the other bitmap codecs are
 * burned into the video server-side when selected.
 */
const PGS_CODECS = new Set(["pgs", "pgssub", "hdmv_pgs_subtitle"]);

/** Returns true if the given subtitle codec is PGS format. */
export function isPGSCodec(codec: string | undefined): boolean {
  if (!codec) return false;
  return PGS_CODECS.has(codec.toLowerCase());
}

/** Bitmap (image-based) subtitle codecs; these carry no extractable text. */
const BITMAP_CODECS = new Set([
  ...PGS_CODECS,
  "dvd_subtitle",
  "dvdsub",
  "vobsub",
  "dvb_subtitle",
  "dvbsub",
  "dvb_teletext",
]);

/** Returns true if the given subtitle codec is bitmap-based (PGS/DVD/DVB). */
export function isBitmapCodec(codec: string | undefined): boolean {
  if (!codec) return false;
  return BITMAP_CODECS.has(codec.toLowerCase());
}

/**
 * Whether selecting the track on the web makes the server burn it into the
 * video. The web player declares `embedded_bitmap` (client-context-v3.ts), so
 * the server serves embedded PGS as a `.sup` sidecar that usePGSSubtitles
 * draws; other bitmap tracks, and any track the server publishes as
 * `burn_in_only`, need the server. Watch-detail tracks carry no delivery
 * yet, so the codec and source decide for them.
 */
export function subtitleNeedsBurnIn(track: {
  codec?: string;
  source?: "external" | "embedded" | "downloaded";
  burn_in_only?: boolean;
}): boolean {
  if (track.burn_in_only === true) return true;
  if (!isBitmapCodec(track.codec)) return false;
  const drawnByPlayer = isPGSCodec(track.codec) && (track.source ?? "embedded") === "embedded";
  return !drawnByPlayer;
}

/**
 * Returns a human-readable format label for display in the subtitle menu,
 * or null if the codec is unknown/unset.
 */
export function getSubtitleFormatLabel(codec: string | undefined): string | null {
  if (!codec) return null;
  switch (codec.toLowerCase()) {
    case "ass":
    case "ssa":
      return "ASS";
    case "srt":
    case "subrip":
      return "SRT";
    case "vtt":
    case "webvtt":
      return "VTT";
    case "pgs":
    case "hdmv_pgs_subtitle":
      return "PGS";
    case "dvd_subtitle":
      return "DVD";
    case "dvb_subtitle":
      return "DVB";
    default:
      return null;
  }
}

function normalizeFormatName(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]/g, "");
}

/** Returns true when a track title merely repeats its codec or display format. */
export function isSubtitleFormatLabel(
  label: string | undefined,
  codec: string | undefined,
): boolean {
  if (!label || !codec) return false;
  const normalizedLabel = normalizeFormatName(label);
  if (!normalizedLabel) return false;

  const formatLabel = getSubtitleFormatLabel(codec);
  return (
    normalizedLabel === normalizeFormatName(codec) ||
    (formatLabel !== null && normalizedLabel === normalizeFormatName(formatLabel))
  );
}
