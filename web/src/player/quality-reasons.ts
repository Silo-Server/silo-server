import { FEATURE_PLAN_QUALITY_REASONS_V3, type PlanV3, type QualityReasonV3 } from "./protocol-v3";

/** The menu's line for a reason this player cannot name. */
const UNKNOWN_REASON_TEXT = "Adjusted for this device";

/** The longest menu line that still takes the second reason. */
const MENU_LINE_MAX_LENGTH = 72;

/** Second reasons the menu adds: changes the viewer can see or hear. */
const COMPATIBILITY_CODES = new Set([
  "dynamic_range_converted",
  "audio_converted",
  "subtitle_burn_in",
]);

const VIDEO_CODEC_NAMES: Record<string, string> = {
  hevc: "HEVC",
  av1: "AV1",
  vp9: "VP9",
  mpeg2: "MPEG-2",
  vc1: "VC-1",
  h264: "H.264",
};

const DYNAMIC_RANGE_NAMES: Record<string, string> = {
  dolby_vision: "Dolby Vision",
  hdr10: "HDR10",
  hdr10_plus: "HDR10+",
  hlg: "HLG",
  sdr: "SDR",
};

const AUDIO_CODEC_NAMES: Record<string, string> = {
  truehd: "TrueHD",
  dts: "DTS",
  eac3: "Dolby Digital Plus",
  ac3: "Dolby Digital",
  aac: "AAC",
  flac: "FLAC",
  opus: "Opus",
};

/**
 * Quality-menu bitrate label: Mbps with collapsed integers ("8 Mbps", not
 * "8.0 Mbps") — a deliberately different display policy than the canonical
 * formatBitrate/formatMbpsFromKbps in @/lib/mediaFormat.
 */
export function formatQualityBitrate(kbps?: number): string {
  if (!isPositive(kbps)) return "";
  if (kbps >= 1000) {
    const mbps = kbps / 1000;
    return mbps % 1 === 0 ? `${mbps} Mbps` : `${mbps.toFixed(1)} Mbps`;
  }
  return `${kbps} kbps`;
}

/**
 * The resolution class a delivered frame reads as. Either dimension can earn
 * a class, so a scope frame keeps its own: 1920x800 is 1080p.
 */
export function resolutionClassLabel(width: number, height: number): string {
  if (width >= 3200 || height >= 2000) return "4K";
  if (width >= 1700 || height >= 1000) return "1080p";
  if (width >= 1200 || height >= 700) return "720p";
  if (width >= 800 || height >= 450) return "480p";
  return "360p";
}

/** The quality control's label while Auto plays: Auto and what it delivers. */
export function autoQualityLabel(plan: PlanV3): string {
  const recipe = plan.effective_recipe;
  const [width, height] = isPositive(recipe.height)
    ? [recipe.width ?? 0, recipe.height]
    : [plan.source.width ?? 0, plan.source.height];
  return isPositive(height) ? `Auto · ${resolutionClassLabel(width, height)}` : "Auto";
}

/**
 * The text for one reason, or null when this player does not know the code
 * or the reason lacks a figure its text needs.
 */
export function qualityReasonText(reason: QualityReasonV3): string | null {
  const rate = formatQualityBitrate(reason.kbps);
  switch (reason.code) {
    case "original":
      return "Original quality";
    case "user_selected":
      return "Quality you picked";
    case "network_learned_limit":
      return rate ? `Limited to ${rate} after buffering` : null;
    case "network_estimate":
      return rate ? `Your connection measured ${rate}` : null;
    case "client_cap":
      return rate ? `Limited to ${rate} by this device's settings` : null;
    case "server_cap":
      return rate ? `Limited to ${rate} by the server` : null;
    case "device_resolution_limit":
      return isPositive(reason.height)
        ? `This device plays up to ${resolutionClassLabel(0, reason.height)}`
        : null;
    case "alternate_version":
      return "Playing another version that fits";
    case "video_codec_unsupported":
      return reason.from
        ? `Converted: this device can't play ${tokenName(VIDEO_CODEC_NAMES, reason.from)} video`
        : null;
    case "dynamic_range_converted":
      return reason.from && reason.to
        ? `${tokenName(DYNAMIC_RANGE_NAMES, reason.from)} shown as ${tokenName(DYNAMIC_RANGE_NAMES, reason.to)}`
        : null;
    case "audio_converted":
      return reason.from && reason.to
        ? `${tokenName(AUDIO_CODEC_NAMES, reason.from)} audio converted to ${tokenName(AUDIO_CODEC_NAMES, reason.to)}`
        : null;
    case "subtitle_burn_in":
      return "Subtitles burned into the video";
    case "container_unsupported":
      return "Repackaged for this device";
    default:
      return null;
  }
}

/**
 * The quality menu's line under the checked row: the reason that decided the
 * quality, and the second reason when it is a visible or audible change and
 * the line stays short. Null when the server does not publish reasons.
 */
export function qualityReasonMenuLine(
  plan: PlanV3 | null,
  serverFeatures: readonly string[],
): string | null {
  const reasons = publishedReasons(plan, serverFeatures);
  if (!reasons) return null;
  const first = qualityReasonText(reasons[0]!) ?? UNKNOWN_REASON_TEXT;
  const second = reasons[1];
  const secondText =
    second && COMPATIBILITY_CODES.has(second.code) ? qualityReasonText(second) : null;
  const joined = secondText ? `${first} · ${secondText}` : first;
  return joined.length <= MENU_LINE_MAX_LENGTH ? joined : first;
}

/**
 * Playback info's Reason row: every reason, an unknown one by its code. Null
 * when the server does not publish reasons.
 */
export function qualityReasonStatsLine(
  plan: PlanV3 | null,
  serverFeatures: readonly string[],
): string | null {
  const reasons = publishedReasons(plan, serverFeatures);
  if (!reasons) return null;
  return reasons.map((reason) => qualityReasonText(reason) ?? reason.code).join(" · ");
}

function publishedReasons(
  plan: PlanV3 | null,
  serverFeatures: readonly string[],
): QualityReasonV3[] | null {
  if (!serverFeatures.includes(FEATURE_PLAN_QUALITY_REASONS_V3)) return null;
  const reasons = plan?.quality_reasons;
  return reasons && reasons.length > 0 ? reasons : null;
}

function tokenName(names: Record<string, string>, token: string): string {
  return names[token] ?? token.toUpperCase();
}

function isPositive(value: number | undefined): value is number {
  return typeof value === "number" && Number.isFinite(value) && value > 0;
}
