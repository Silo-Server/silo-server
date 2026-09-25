/**
 * Per-stream bitrate limits (`max_remote_stream_bitrate_kbps` and
 * `max_local_stream_bitrate_kbps` on access groups and user overrides). The API
 * stores whole kbps (1 kbps = 1,000 bit/s; ffmpeg receives `-maxrate <n>k`) with
 * 0 meaning unlimited, but admins read and type them in Mbps.
 */

/** Preset caps offered ahead of a custom value, highest first. */
export const STREAM_BITRATE_LIMIT_PRESETS_KBPS = [
  40000, 20000, 10000, 8000, 5000, 3000, 2000, 1000,
] as const;

/** Caps below this force very low-quality transcodes, so the forms warn. */
export const LOW_STREAM_BITRATE_LIMIT_KBPS = 1000;

export function isStreamBitrateLimitPreset(kbps: number): boolean {
  return (STREAM_BITRATE_LIMIT_PRESETS_KBPS as readonly number[]).includes(kbps);
}

export function isLowStreamBitrateLimit(kbps: number): boolean {
  return kbps > 0 && kbps < LOW_STREAM_BITRATE_LIMIT_KBPS;
}

/** kbps as an exact Mbps number string: 8000 → "8", 1500 → "1.5", 250 → "0.25". */
export function streamBitrateLimitMbpsText(kbps: number): string {
  return String(kbps / 1000);
}

export function formatStreamBitrateLimit(kbps: number): string {
  return kbps === 0 ? "Unlimited" : `${streamBitrateLimitMbpsText(kbps)} Mbps`;
}

// What the custom Mbps box accepts: up to three decimals, "." or "," as separator.
const MBPS_TEXT = /^\s*(\d+([.,]\d{0,3})?|[.,]\d{1,3})\s*$/;

/**
 * Parses a typed custom cap in Mbps into kbps. Returns null unless the text is
 * a positive number the API can store exactly (1 kbps resolution), so nothing
 * is rounded silently. 0 is rejected too: it means unlimited, which has its
 * own choice, and a half-typed "0." must not save as no cap at all.
 */
export function parseStreamBitrateMbps(text: string): number | null {
  if (!MBPS_TEXT.test(text)) return null;
  const kbps = Math.round(Number(text.trim().replace(",", ".")) * 1000);
  return Number.isSafeInteger(kbps) && kbps > 0 ? kbps : null;
}
