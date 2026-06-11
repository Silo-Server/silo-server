/** Codecs that indicate ASS/SSA format subtitles with rich styling support. */
const ASS_CODECS = new Set(["ass", "ssa"]);

/** Returns true if the given subtitle codec is ASS/SSA format. */
export function isASSCodec(codec: string | undefined): boolean {
  if (!codec) return false;
  return ASS_CODECS.has(codec.toLowerCase());
}

/**
 * Codecs that indicate PGS (Blu-ray bitmap) subtitles. Rendered client-side
 * by libpgs from a .sup stream served by the backend.
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
