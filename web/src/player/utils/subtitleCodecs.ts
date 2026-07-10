/** Codecs that indicate ASS/SSA format subtitles with rich styling support. */
const ASS_CODECS = new Set(["ass", "ssa"]);

/** Returns true if the given subtitle codec is ASS/SSA format. */
export function isASSCodec(codec: string | undefined): boolean {
  if (!codec) return false;
  return ASS_CODECS.has(codec.toLowerCase());
}

/**
 * Codecs that indicate PGS (Blu-ray bitmap) subtitles. Like all bitmap
 * codecs, PGS is burned into the video server-side when selected.
 */
const PGS_CODECS = new Set(["pgs", "hdmv_pgs_subtitle"]);

/** Returns true if the given subtitle codec is PGS format. */
export function isPGSCodec(codec: string | undefined): boolean {
  if (!codec) return false;
  return PGS_CODECS.has(codec.toLowerCase());
}

/** Bitmap (image-based) subtitle codecs; these carry no extractable text. */
const BITMAP_CODECS = new Set([...PGS_CODECS, "dvd_subtitle", "dvb_subtitle"]);

/** Returns true if the given subtitle codec is bitmap-based (PGS/DVD/DVB). */
export function isBitmapCodec(codec: string | undefined): boolean {
  if (!codec) return false;
  return BITMAP_CODECS.has(codec.toLowerCase());
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
