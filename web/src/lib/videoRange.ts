// Derives a compact dynamic-range badge label ("DV", "DV HDR10", "HDR10+",
// "HDR10", "HLG", "HDR") from a file version's probed video tracks. Mirrors
// the server-side vocabulary in internal/overlays/summary.go (normalizeHDR)
// so card overlays, detail badges, version pickers, and the player HUD all
// agree. The bare `hdr` boolean is kept as the last-resort fallback for
// files probed before Dolby Vision / video-range metadata existed.

/** Minimal structural shape shared by VersionVideoTrack and PlayerVideoTrack. */
export interface VideoRangeTrack {
  dolby_vision?: string;
  dv_profile?: number;
  hdr10_plus?: boolean;
  video_range_type?: string;
  color_transfer?: string;
}

/** Minimal structural shape shared by FileVersion and PlayerFileVersion. */
export interface VideoRangeSource {
  hdr?: boolean;
  video_tracks?: VideoRangeTrack[];
}

function trackHasDolbyVision(track: VideoRangeTrack): boolean {
  if (track.dolby_vision?.trim()) return true;
  if ((track.dv_profile ?? 0) > 0) return true;
  return (track.video_range_type?.trim() ?? "").startsWith("DOVI");
}

// Distinguishes HDR variants from the scanner-derived video_range_type enum
// (HDR10, HDR10Plus, HLG, DOVIWith*) with color_transfer as a fallback,
// matching the server's hdrTypeFromTracks.
function trackHdrType(track: VideoRangeTrack): string {
  const rangeType = track.video_range_type?.trim() ?? "";
  if (track.hdr10_plus || rangeType.includes("HDR10Plus")) return "HDR10+";
  if (rangeType === "HDR10" || rangeType.endsWith("WithHDR10")) return "HDR10";
  if (rangeType === "HLG" || rangeType.endsWith("WithHLG")) return "HLG";

  const transfer = track.color_transfer?.toLowerCase() ?? "";
  if (transfer.includes("smpte2084")) return "HDR10";
  if (transfer.includes("arib-std-b67")) return "HLG";
  return "";
}

/**
 * Display label for a file version's dynamic range: "DV", "DV HDR10",
 * "DV HDR10+", "DV HLG", "HDR10+", "HDR10", "HLG", "HDR" (boolean-only
 * fallback), or "" for SDR/unknown.
 */
export function videoRangeLabel(source: VideoRangeSource): string {
  let hasDV = false;
  let hdrType = "";
  for (const track of source.video_tracks ?? []) {
    if (trackHasDolbyVision(track)) hasDV = true;
    if (!hdrType) hdrType = trackHdrType(track);
  }

  if (hasDV) return hdrType ? `DV ${hdrType}` : "DV";
  if (hdrType) return hdrType;
  if (source.hdr) return "HDR";
  return "";
}

// Rollup preference when a badge summarizes several versions: any DV variant
// beats explicit HDR10+/HDR10/HLG, which beat the generic boolean "HDR".
const LABEL_RANK: Record<string, number> = {
  "DV HDR10+": 8,
  "DV HDR10": 7,
  "DV HLG": 6,
  DV: 5,
  "HDR10+": 4,
  HDR10: 3,
  HLG: 2,
  HDR: 1,
};

/** Best (most specific) dynamic-range label across a set of versions. */
export function bestVideoRangeLabel(sources: VideoRangeSource[]): string {
  let best = "";
  let bestRank = 0;
  for (const source of sources) {
    const label = videoRangeLabel(source);
    const rank = LABEL_RANK[label] ?? 0;
    if (rank > bestRank) {
      best = label;
      bestRank = rank;
    }
  }
  return best;
}
