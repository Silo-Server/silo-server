import type { AdminSession } from "@/api/types";
import { t } from "@/i18n";
import type { MessageKey } from "@/locales/keys";

interface AdaptationReasonText {
  label: MessageKey;
  description: MessageKey;
}

// The server's closed adaptation_reasons vocabulary (playback.AdaptationReasons).
// A code a newer server adds and this build does not know is left out.
const ADAPTATION_REASON_TEXT: Record<string, AdaptationReasonText> = {
  video_codec_unsupported: {
    label: "web.admin.activity.reason.videoCodecUnsupported.label",
    description: "web.admin.activity.reason.videoCodecUnsupported.description",
  },
  video_capability_unconfirmed: {
    label: "web.admin.activity.reason.videoCapabilityUnconfirmed.label",
    description: "web.admin.activity.reason.videoCapabilityUnconfirmed.description",
  },
  dynamic_range_unsupported: {
    label: "web.admin.activity.reason.dynamicRangeUnsupported.label",
    description: "web.admin.activity.reason.dynamicRangeUnsupported.description",
  },
  resolution_limit: {
    label: "web.admin.activity.reason.resolutionLimit.label",
    description: "web.admin.activity.reason.resolutionLimit.description",
  },
  bandwidth_limit: {
    label: "web.admin.activity.reason.bandwidthLimit.label",
    description: "web.admin.activity.reason.bandwidthLimit.description",
  },
  buffering: {
    label: "web.admin.activity.reason.buffering.label",
    description: "web.admin.activity.reason.buffering.description",
  },
  bandwidth_cap: {
    label: "web.admin.activity.reason.bandwidthCap.label",
    description: "web.admin.activity.reason.bandwidthCap.description",
  },
  server_bitrate_limit: {
    label: "web.admin.activity.reason.serverBitrateLimit.label",
    description: "web.admin.activity.reason.serverBitrateLimit.description",
  },
  quality_selected: {
    label: "web.admin.activity.reason.qualitySelected.label",
    description: "web.admin.activity.reason.qualitySelected.description",
  },
  subtitle_burn_in: {
    label: "web.admin.activity.reason.subtitleBurnIn.label",
    description: "web.admin.activity.reason.subtitleBurnIn.description",
  },
  direct_play_failed: {
    label: "web.admin.activity.reason.directPlayFailed.label",
    description: "web.admin.activity.reason.directPlayFailed.description",
  },
  audio_codec_unsupported: {
    label: "web.admin.activity.reason.audioCodecUnsupported.label",
    description: "web.admin.activity.reason.audioCodecUnsupported.description",
  },
  audio_channels_unsupported: {
    label: "web.admin.activity.reason.audioChannelsUnsupported.label",
    description: "web.admin.activity.reason.audioChannelsUnsupported.description",
  },
  device_workaround: {
    label: "web.admin.activity.reason.deviceWorkaround.label",
    description: "web.admin.activity.reason.deviceWorkaround.description",
  },
  audio_track_selection: {
    label: "web.admin.activity.reason.audioTrackSelection.label",
    description: "web.admin.activity.reason.audioTrackSelection.description",
  },
  container_unsupported: {
    label: "web.admin.activity.reason.containerUnsupported.label",
    description: "web.admin.activity.reason.containerUnsupported.description",
  },
  subtitle_unsupported: {
    label: "web.admin.activity.reason.subtitleUnsupported.label",
    description: "web.admin.activity.reason.subtitleUnsupported.description",
  },
};

export interface AdaptationReason {
  code: string;
  label: string;
  description: string;
}

/** Resolve a single canonical adaptation reason code to localized copy, or null if unknown. */
export function getAdaptationReasonByCode(code: string): AdaptationReason | null {
  const text = Object.hasOwn(ADAPTATION_REASON_TEXT, code)
    ? ADAPTATION_REASON_TEXT[code]
    : undefined;
  return text ? { code, label: t(text.label), description: t(text.description) } : null;
}

/** Resolve an ordered list of canonical adaptation reason codes to localized entries. */
export function getAdaptationReasonsFromCodes(
  codes?: readonly string[] | null,
): AdaptationReason[] {
  return (codes ?? []).flatMap((code) => {
    const resolved = getAdaptationReasonByCode(code);
    return resolved ? [resolved] : [];
  });
}

/**
 * Why a session converts or repackages the file instead of playing it as it
 * is, in the server's order (video causes first). Empty for direct play and
 * for sessions the server reported no reasons for.
 */
export function getAdaptationReasons(session: AdminSession): AdaptationReason[] {
  return getAdaptationReasonsFromCodes(session.adaptation_reasons);
}
