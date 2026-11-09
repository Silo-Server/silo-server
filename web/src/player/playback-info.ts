import {
  audioTrackHasAtmos,
  audioTrackLabel,
  dolbyVisionLabel,
  formatBitrate,
  formatCodecLabel,
  formatFileSize,
  formatMbpsFromKbps,
  formatSampleRate,
  mentionsAtmos,
} from "@/lib/mediaFormat";
import { videoRangeLabel } from "@/lib/videoRange";
import type { DeliveryV3, PlanV3 } from "./protocol-v3";
import {
  QUALITY_ORIGINAL_V3,
  normalizeQualityPreferenceV3,
  TRANSFORMATION_AUDIO_TO_AAC_V3,
  TRANSFORMATION_VIDEO_TO_H264_V3,
} from "./protocol-v3";
import type { PlayerAudioTrack, PlayerFileVersion, PlayerVideoTrack, QualityOption } from "./types";

export interface RuntimePlaybackStats {
  playerWidth?: number;
  playerHeight?: number;
  videoWidth?: number;
  videoHeight?: number;
  droppedFrames?: number | null;
  corruptedFrames?: number | null;
}

export interface PlaybackInfoRow {
  label: string;
  value: string;
}

export interface PlaybackInfoSection {
  title: string;
  rows: PlaybackInfoRow[];
}

interface BuildPlaybackInfoSectionsInput {
  streamUrl: string;
  plan: PlanV3;
  currentSourceVersion?: PlayerFileVersion;
  requestedVersion?: PlayerFileVersion;
  runtimeStats: RuntimePlaybackStats;
  /** True while the browser decode engine plays the stream instead of the media element. */
  browserEngine?: boolean;
}

export function buildPlaybackInfoSections({
  streamUrl,
  plan,
  currentSourceVersion,
  requestedVersion,
  runtimeStats,
  browserEngine = false,
}: BuildPlaybackInfoSectionsInput): PlaybackInfoSection[] {
  const videoTrack = currentSourceVersion ? pickVideoTrack(currentSourceVersion) : undefined;
  const audioTrack = currentSourceVersion ? pickAudioTrack(currentSourceVersion) : undefined;
  const requestedSource =
    requestedVersion &&
    currentSourceVersion &&
    requestedVersion.file_id !== currentSourceVersion.file_id
      ? formatRequestedSourceVersion(requestedVersion)
      : null;

  return [
    {
      title: "Player",
      rows: [
        { label: "Player", value: browserEngine ? "Browser decoder" : "HTML Video Player" },
        { label: "Play method", value: formatDelivery(plan.delivery) },
        { label: "Protocol", value: formatProtocol(streamUrl) },
        { label: "Stream type", value: formatStreamType(plan) },
        ...(requestedSource ? [{ label: "Auto-switched from", value: requestedSource }] : []),
      ],
    },
    {
      title: "Video Info",
      rows: [
        {
          label: "Player dimensions",
          value: formatDimensions(runtimeStats.playerWidth, runtimeStats.playerHeight),
        },
        {
          label: "Video resolution",
          value: formatDimensions(runtimeStats.videoWidth, runtimeStats.videoHeight),
        },
        {
          label: "Dropped frames",
          value: formatFrameCount(runtimeStats.droppedFrames),
        },
        {
          label: "Corrupted frames",
          value: formatFrameCount(runtimeStats.corruptedFrames),
        },
      ],
    },
    {
      title: "Playback Stream Info",
      rows: [
        {
          label: "Video codec",
          value: formatDeliveredVideoCodec(plan),
        },
        {
          label: "Audio codec",
          value: formatDeliveredAudioCodec(plan),
        },
      ],
    },
    {
      title: "Current Source File",
      rows: [
        {
          label: "Container",
          value: displayValue(currentSourceVersion?.container),
        },
        {
          label: "Size",
          value: formatFileSize(currentSourceVersion?.file_size, {
            iecUnits: true,
            fallback: "—",
          }),
        },
        {
          label: "Bitrate",
          value: formatMbpsFromKbps(currentSourceVersion?.bitrate),
        },
        {
          label: "Video codec",
          value: formatOriginalVideoCodec(currentSourceVersion, videoTrack),
        },
        {
          label: "Video bitrate",
          value: formatMbpsFromKbps(videoTrack?.bitrate),
        },
        {
          label: "Video range type",
          value: formatVideoRangeType(currentSourceVersion, videoTrack),
        },
        {
          label: "Color range",
          value: formatColorRange(videoTrack?.color_range),
        },
        {
          label: "Audio codec",
          value: formatOriginalAudioCodec(currentSourceVersion, audioTrack),
        },
        {
          label: "Audio bitrate",
          value: formatBitrate(audioTrack?.bitrate, "—"),
        },
        {
          label: "Audio channels",
          value: formatAudioChannels(currentSourceVersion, audioTrack),
        },
        {
          label: "Audio sample rate",
          value: formatSampleRate(audioTrack?.sample_rate, "—"),
        },
      ],
    },
  ];
}

/**
 * Turns the plan's quality ladder into menu entries.
 *
 * The server owns the ladder: this maps its fixed rungs onto the menu's shape.
 * `id` is the rung's own label, which is what a `quality_change` replan sends
 * back. The `auto` entry is prepended locally because it is a *preference*,
 * not a rung, and it is also how the viewer returns to the source: Auto plays
 * the original file whenever the device and connection can carry it. Older
 * servers still list that source as an "original" entry, which is dropped
 * here.
 */
export function qualityOptionsFromPlanV3(plan: PlanV3): QualityOption[] {
  const rungs = plan.available_qualities
    .filter((quality) => quality.label !== QUALITY_ORIGINAL_V3)
    .map((quality) => ({
      id: quality.label,
      label: quality.display_name || quality.label,
      sublabel: formatQualityBitrate(quality.bitrate_kbps),
      resolution: quality.height ? `${quality.height}p` : "",
      bitrateKbps: quality.bitrate_kbps ?? 0,
    }));

  // Without a fixed rung there is nothing to choose, so there is no menu —
  // audio-only plans and clients without HLS land here.
  if (rungs.length === 0) {
    return [];
  }

  return [
    {
      id: "auto",
      label: plan.bandwidth_limit ? "Auto · limited after buffering" : "Auto",
      sublabel: "",
      resolution: "",
      bitrateKbps: 0,
    },
    ...rungs,
  ];
}

/**
 * The menu entry the current plan delivers. An auto preference, including the
 * legacy source labels, lights Auto. Any other preference lights the entry the
 * server names as the plan's `active_quality`, or none when the plan delivers
 * no entry on this menu, so Auto stays selectable.
 */
export function resolveActiveQualityOptionId(
  options: QualityOption[],
  preference: string,
  activeQuality?: string,
): string | null {
  if (options.length === 0) return null;
  if (normalizeQualityPreferenceV3(preference) === "auto") return "auto";
  return options.find((option) => option.id === activeQuality)?.id ?? null;
}

/**
 * The label for a plain resolution preference ("1080p", "4K") when it plays
 * the source as-is and no menu row is lit; null for any other preference.
 */
export function plainResolutionLabel(preference: string): string | null {
  const height = qualityPreferenceHeight(normalizeQualityPreferenceV3(preference).toLowerCase());
  if (height === null) return null;
  return height === 2160 ? "4K" : `${height}p`;
}

/**
 * The next quality rung below what the viewer receives now, for a viewer whose
 * connection cannot keep up. The step is from the lower of the lit rung's
 * bitrate and the delivered one, since a cap can encode a lit rung below its
 * listed rate. With neither, a copy delivery steps to the top transcode rung.
 */
export function lowerQualityOption(
  options: QualityOption[],
  preference: string,
  activeQuality?: string,
  deliveredBitrateKbps?: number,
): QualityOption | null {
  const rungs = options
    .filter((option) => option.id !== "auto" && option.bitrateKbps > 0)
    .sort((a, b) => b.bitrateKbps - a.bitrateKbps);
  const activeId = resolveActiveQualityOptionId(options, preference, activeQuality);
  const active = rungs.find((option) => option.id === activeId);
  const ceiling = Math.min(
    active?.bitrateKbps ?? Infinity,
    isPositive(deliveredBitrateKbps) ? deliveredBitrateKbps : Infinity,
  );
  return rungs.find((option) => option.bitrateKbps < ceiling) ?? null;
}

function qualityPreferenceHeight(preference: string): number | null {
  switch (preference) {
    case "2160p":
    case "4k":
    case "uhd":
      return 2160;
    case "1080p":
    case "fhd":
      return 1080;
    case "720p":
    case "hd":
      return 720;
    case "480p":
    case "sd":
      return 480;
    default:
      return null;
  }
}

// Quality-menu bitrate label: Mbps with collapsed integers ("8 Mbps", not
// "8.0 Mbps") — a deliberately different display policy than the canonical
// formatBitrate/formatMbpsFromKbps in @/lib/mediaFormat.
function formatQualityBitrate(kbps?: number): string {
  if (!isPositive(kbps)) return "";
  if (kbps >= 1000) {
    const mbps = kbps / 1000;
    return mbps % 1 === 0 ? `${mbps} Mbps` : `${mbps.toFixed(1)} Mbps`;
  }
  return `${kbps} kbps`;
}

function formatRequestedSourceVersion(version: PlayerFileVersion): string {
  const parts = [
    version.resolution?.trim(),
    formatCodecLabel(version.codec_video),
    videoRangeLabel(version) || null,
  ].filter(Boolean);
  return parts.join(" ");
}

/**
 * Names the route the server chose. The plan's `delivery` is the only input:
 * whether video was re-encoded is a server decision, not something to infer
 * from codec strings.
 */
export function formatDelivery(delivery: DeliveryV3): string {
  switch (delivery) {
    case "original_http":
      return "Direct Play";
    case "server_remux_progressive":
    case "server_remux_hls":
      return "Direct Streaming";
    case "server_transcode_hls":
      return "Transcode";
  }
}

export function formatProtocol(streamUrl: string): string {
  try {
    const base = typeof window !== "undefined" ? window.location.href : "http://localhost";
    return new URL(streamUrl, base).protocol.replace(":", "");
  } catch {
    return "—";
  }
}

export function formatStreamType(plan: PlanV3): string {
  return plan.stream.protocol === "hls" ? "HLS" : "Progressive";
}

export function formatDimensions(width?: number, height?: number): string {
  if (!isPositive(width) || !isPositive(height)) {
    return "—";
  }
  return `${Math.round(width)}x${Math.round(height)}`;
}

export function formatFrameCount(value?: number | null): string {
  if (!Number.isFinite(value) || value == null || value < 0) {
    return "—";
  }
  return String(Math.round(value));
}

/**
 * What is on the wire for video, and how it got there.
 *
 * The codec comes from the plan's effective recipe and the qualifier from its
 * transformation list — the server states outright whether it re-encoded, so
 * nothing here compares codec strings to guess.
 */
export function formatDeliveredVideoCodec(plan: PlanV3): string {
  const base = formatCodecLabel(plan.effective_recipe.video_codec);
  if (base === "—") return base;
  if (plan.delivery === "original_http") return `${base} (direct)`;
  const transcoded = planTransforms(plan, TRANSFORMATION_VIDEO_TO_H264_V3);
  return `${base} (${transcoded ? "transcoded" : "copy"})`;
}

export function formatDeliveredAudioCodec(plan: PlanV3): string {
  const base = formatCodecLabel(plan.effective_recipe.audio_codec);
  if (base === "—") return base;
  if (plan.delivery === "original_http") return `${base} (direct)`;
  const transcoded = planTransforms(plan, TRANSFORMATION_AUDIO_TO_AAC_V3);
  return `${base} (${transcoded ? "transcoded" : "copy"})`;
}

function planTransforms(plan: PlanV3, name: string): boolean {
  return plan.transformations.some(
    (transformation) => transformation.executor === "server" && transformation.name === name,
  );
}

export function formatOriginalVideoCodec(
  version?: PlayerFileVersion,
  track?: PlayerVideoTrack,
): string {
  const codec = formatCodecLabel(track?.codec || version?.codec_video);
  if (codec === "—") {
    return "—";
  }
  return [codec, track?.profile].filter(Boolean).join(" ");
}

export function formatVideoRangeType(
  version?: PlayerFileVersion,
  track?: PlayerVideoTrack,
): string {
  if (track?.dolby_vision) {
    const dolbyVision = dolbyVisionLabel(track.dolby_vision);
    return track.video_range ? `${dolbyVision} (${track.video_range})` : dolbyVision;
  }
  if (track?.video_range) {
    return track.video_range;
  }
  if (version) {
    return videoRangeLabel(version) || "SDR";
  }
  return "—";
}

export function formatColorRange(value?: string): string {
  switch (value?.trim().toLowerCase()) {
    case "tv":
      return "Limited (tv)";
    case "pc":
      return "Full (pc)";
    case "unknown":
      return "Unknown";
    default:
      return "—";
  }
}

export function formatOriginalAudioCodec(
  version?: PlayerFileVersion,
  track?: PlayerAudioTrack,
): string {
  const title = track?.title || track?.embedded_title;
  // An Atmos track whose title does not say so, such as the codec's long
  // name the scanner gives an untagged track, shows its family label.
  if (track?.codec && audioTrackHasAtmos(track) && !mentionsAtmos(title)) {
    return audioTrackLabel(track);
  }
  if (title) {
    return title;
  }
  return formatCodecLabel(track?.codec || version?.codec_audio);
}

export function formatAudioChannels(version?: PlayerFileVersion, track?: PlayerAudioTrack): string {
  const channels = track?.channels ?? version?.audio_channels;
  return isPositive(channels) ? String(channels) : "—";
}

function pickVideoTrack(version: PlayerFileVersion): PlayerVideoTrack | undefined {
  return version.video_tracks?.[0];
}

function pickAudioTrack(version: PlayerFileVersion): PlayerAudioTrack | undefined {
  return version.audio_tracks?.find((track) => track.default) ?? version.audio_tracks?.[0];
}

function displayValue(value?: string): string {
  return value && value.trim() ? value : "—";
}

function isPositive(value?: number): value is number {
  return Number.isFinite(value) && value != null && value > 0;
}
