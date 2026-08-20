import {
  dolbyVisionLabel,
  formatBitrate,
  formatCodecLabel,
  formatFileSize,
  formatMbpsFromKbps,
  formatSampleRate,
} from "@/lib/mediaFormat";
import { videoRangeLabel } from "@/lib/videoRange";
import type { DeliveryV3, PlanV3 } from "./protocol-v3";
import {
  QUALITY_ORIGINAL_V3,
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
}

export function buildPlaybackInfoSections({
  streamUrl,
  plan,
  currentSourceVersion,
  requestedVersion,
  runtimeStats,
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
        { label: "Player", value: "HTML Video Player" },
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
 * The server owns the ladder: this maps its rungs onto the menu's shape and
 * adds nothing. `id` is the rung's own label, which is what a `quality_change`
 * replan sends back. The `auto` entry is prepended locally because it is a
 * *preference*, not a rung — no server rung names it, and picking it hands the
 * choice back to the planner.
 */
export function qualityOptionsFromPlanV3(plan: PlanV3): QualityOption[] {
  const rungs = plan.available_qualities.map((quality) => ({
    id: quality.label,
    label: qualityRungLabel(quality.label),
    sublabel: formatQualityBitrate(quality.bitrate_kbps),
    resolution: quality.height ? `${quality.height}p` : "",
    bitrateKbps: quality.bitrate_kbps ?? 0,
    isOriginal: quality.preserves_source,
  }));

  // A single source-preserving rung is not a choice, so there is no menu to
  // render — audio-only plans and clients without HLS land here.
  if (rungs.length <= 1) {
    return rungs;
  }

  return [
    { id: "auto", label: "Auto", sublabel: "", resolution: "", bitrateKbps: 0, isOriginal: false },
    ...rungs,
  ];
}

function qualityRungLabel(label: string): string {
  return label === QUALITY_ORIGINAL_V3 ? "Original" : label;
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
