import type { AdminSession } from "@/api/types";
import { formatCodecLabel } from "@/lib/mediaFormat";

export function formatDecisionLabel(decision?: string): string {
  switch (decision) {
    case "direct":
      return "Direct";
    case "copy":
      return "Copy";
    case "remux":
      return "Remux";
    case "hls":
      return "HLS";
    case "transcode":
      return "Transcode";
    default:
      return "Unknown";
  }
}

export function normalizeContainerDecision(playMethod?: string): string {
  switch (playMethod?.trim()) {
    case "direct":
      return "direct";
    case "remux":
      return "remux";
    case "transcode":
    case "hls":
      return "hls";
    default:
      return "";
  }
}

export function normalizeStreamDecision(decision?: string): string {
  switch (decision?.trim()) {
    case "direct":
      return "direct";
    case "copy":
    case "remux":
      return "copy";
    case "transcode":
      return "transcode";
    default:
      return "";
  }
}

/**
 * Classify a session into a single activity "method" bucket for aggregation and
 * filtering:
 *   - video is re-encoded            -> "transcode" (video transcode)
 *   - only audio is re-encoded       -> "audio"     (audio transcode)
 *   - streams only repackaged/copied -> "remux"     (incl. video-copy HLS)
 *   - nothing touched                -> "direct"
 *   - nothing known                  -> "unknown"
 * The server computes this reduction as effective_play_method (the
 * authoritative value, shared with the other admin clients); the local
 * fallback below covers servers that don't emit the field yet.
 */
export function classifyActivityMethod(session: AdminSession): string {
  if (session.effective_play_method) {
    return session.effective_play_method;
  }
  const videoDecision = normalizeStreamDecision(session.video_decision || session.play_method);
  const audioDecision = normalizeStreamDecision(
    session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method),
  );
  if (videoDecision === "transcode") {
    return "transcode";
  }
  // An empty video decision means the stream state is unknown (empty or
  // unrecognized play_method on a legacy row); don't invent an audio
  // transcode from the bare transcode_audio flag.
  if (audioDecision === "transcode" && videoDecision !== "") {
    return "audio";
  }
  if (videoDecision === "direct" && audioDecision === "direct") {
    return "direct";
  }
  if (videoDecision === "copy" || audioDecision === "copy") {
    return "remux";
  }
  return "unknown";
}

// Display order for the activity method buckets. Escalates by cost and keeps the
// audio-transcode tag AFTER the video-transcode tag in the Play Method line and
// the Server Activity popover; unknown sorts last.
const ACTIVITY_METHOD_ORDER = ["direct", "remux", "transcode", "audio", "unknown"];

function activityMethodRank(method: string): number {
  const index = ACTIVITY_METHOD_ORDER.indexOf(method);
  return index === -1 ? ACTIVITY_METHOD_ORDER.length : index;
}

/** Sort comparator for activity method keys, audio last. Falls back to
 * alphabetical for anything outside the known order. */
export function compareActivityMethods(a: string, b: string): number {
  const diff = activityMethodRank(a) - activityMethodRank(b);
  return diff !== 0 ? diff : a.localeCompare(b);
}

export interface ActivityMethodMeta {
  /** Human label ("Direct Play"); the bucket key itself is the short tag. */
  label: string;
  /** Solid swatch class for distribution bars and legend dots. */
  swatchClass: string;
  /** Tinted badge classes for the per-row method tag. */
  badgeClass: string;
}

// Single source for the bucket -> label/color mapping. Every surface that
// renders method tags, distribution bars, or legend dots derives from this so
// the Admin Activity page, the dashboard stream cards, and the Server Activity
// popover cannot drift apart.
const ACTIVITY_METHOD_META: Record<string, ActivityMethodMeta> = {
  direct: {
    label: "Direct Play",
    swatchClass: "bg-success",
    badgeClass: "bg-success/10 text-success border-success/15",
  },
  remux: {
    label: "Remux",
    swatchClass: "bg-info",
    badgeClass: "bg-info/10 text-info border-info/15",
  },
  transcode: {
    label: "Transcode",
    swatchClass: "bg-warning",
    badgeClass: "bg-warning/10 text-warning border-warning/15",
  },
  audio: {
    label: "Audio Transcode",
    swatchClass: "bg-destructive",
    badgeClass: "bg-destructive/10 text-destructive border-destructive/15",
  },
};

const UNKNOWN_ACTIVITY_METHOD_META: ActivityMethodMeta = {
  label: "Unknown",
  swatchClass: "bg-muted-foreground",
  badgeClass: "bg-surface text-muted-foreground border-border",
};

/** Presentation (label + colors) for an activity method bucket. */
export function activityMethodMeta(method: string): ActivityMethodMeta {
  return ACTIVITY_METHOD_META[method] ?? UNKNOWN_ACTIVITY_METHOD_META;
}

/**
 * Badge classes for a per-stream decision value (direct/copy/remux/hls/
 * transcode). Copy and HLS are repackaging, so they share the remux tint.
 */
export function decisionBadgeClass(decision: string): string {
  switch (decision) {
    case "copy":
    case "hls":
      return activityMethodMeta("remux").badgeClass;
    default:
      return activityMethodMeta(decision).badgeClass;
  }
}

/**
 * Report whether a session comes from a Jellyfin-ecosystem client, surfaced as
 * the orthogonal "JF" pill next to the method tag. The server owns the client
 * identification (its token list lives beside its client-labeling rules) and
 * emits is_jellyfin_client; sessions from servers without the field simply get
 * no pill.
 */
export function isJellyfinSession(session: AdminSession): boolean {
  return session.is_jellyfin_client === true;
}

export function formatPlaybackDecisionSummary(session: AdminSession): string {
  const videoDecision = normalizeStreamDecision(session.video_decision || session.play_method);
  const audioDecision = normalizeStreamDecision(
    session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method),
  );

  if (videoDecision && videoDecision === audioDecision) {
    return videoDecision;
  }
  if (videoDecision === "transcode" || audioDecision === "transcode") {
    return "transcode";
  }
  if (videoDecision === "copy" || audioDecision === "copy") {
    return "copy";
  }
  return videoDecision || audioDecision || session.play_method || "";
}

export function formatTranscodeModeSummary(session: AdminSession): string | null {
  const videoDecision = normalizeStreamDecision(session.video_decision || session.play_method);
  const audioDecision = normalizeStreamDecision(
    session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method),
  );
  if (videoDecision !== "transcode" && audioDecision !== "transcode") {
    return null;
  }
  if (videoDecision !== "transcode") {
    return "Audio SW";
  }

  const hwAccel = session.transcode_hw_accel?.trim().toLowerCase();
  switch (hwAccel) {
    case "qsv":
      return "HW QSV";
    case "vaapi":
      return "HW VAAPI";
    case "none":
      return "SW";
    case "auto":
      return "HW/SW pending";
    case "":
    case undefined:
      return "HW/SW unknown";
    default:
      return `HW ${hwAccel.toUpperCase()}`;
  }
}

export function formatSessionBitrate(kbps?: number | null): string | null {
  if (!kbps || kbps <= 0) {
    return null;
  }
  if (kbps >= 1000) {
    return `${(kbps / 1000).toFixed(1)} Mbps`;
  }
  return `${Math.round(kbps)} kbps`;
}

export function getSessionClientLabel(session: AdminSession): string {
  const explicitLabel = session.client_label?.trim();
  if (explicitLabel) {
    return explicitLabel;
  }

  const clientName = session.client_name?.trim();
  const clientVersion = session.client_version?.trim();
  if (clientName && clientVersion) {
    return `${clientName} ${clientVersion}`;
  }
  return clientName || "";
}

export function formatSourceContainerSummary(session: AdminSession): string {
  return formatContainer(session.source_container) || "Unknown source";
}

export function formatDeliveredContainerSummary(session: AdminSession): string {
  switch (normalizeContainerDecision(session.play_method)) {
    case "direct":
      return formatSourceContainerSummary(session);
    case "remux":
      return "Remux";
    case "hls":
      return "HLS";
    default:
      return formatSourceContainerSummary(session);
  }
}

export function formatContainerDetail(session: AdminSession): string {
  const source = formatSourceContainerSummary(session);
  switch (normalizeContainerDecision(session.play_method)) {
    case "direct":
      return "Original container";
    case "remux":
      return `${source} → Remux`;
    case "hls":
      return `${source} → HLS`;
    default:
      return "—";
  }
}

export function formatVideoSummary(session: AdminSession): string {
  return (
    [formatCodec(session.source_video_codec), session.source_video_resolution?.trim()]
      .filter(Boolean)
      .join(" · ") || "Unknown source"
  );
}

export function formatDeliveredVideoSummary(session: AdminSession): string {
  const decision = session.video_decision || session.play_method;
  if (decision !== "transcode") {
    return formatVideoSummary(session);
  }

  return (
    [formatCodec(session.target_video_codec), session.target_resolution?.trim()]
      .filter(Boolean)
      .join(" · ") || "Transcoding"
  );
}

export function formatVideoDetail(session: AdminSession): string {
  const decision = normalizeStreamDecision(session.video_decision || session.play_method);
  const requestedSource = formatRequestedVideoSource(session);
  const target = [formatCodec(session.target_video_codec), session.target_resolution?.trim()]
    .filter(Boolean)
    .join(" · ");

  if (hasRequestedSourceSwitch(session) && requestedSource) {
    const parts = [`Auto-switched from ${requestedSource}`];
    if (target) {
      parts.push(`Output → ${target}`);
    } else if (decision === "transcode") {
      parts.push("Transcoding");
    }
    return parts.join(" · ");
  }

  if (decision === "transcode") {
    return target ? `Output → ${target}` : "Transcoding";
  }
  if (decision === "copy") {
    return "Video stream copied";
  }
  if (decision === "direct") {
    return "No video conversion";
  }
  return "—";
}

export function formatAudioSummary(session: AdminSession): string {
  const lead = session.source_audio_title?.trim() || session.source_audio_language?.trim();
  const format = [
    formatCodec(session.source_audio_codec),
    formatChannelLayout(session.source_audio_channels),
  ]
    .filter(Boolean)
    .join(" ");
  return [lead, format].filter(Boolean).join(" · ") || "Unknown source";
}

export function formatDeliveredAudioSummary(session: AdminSession): string {
  const decision =
    session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method);
  if (decision !== "transcode") {
    return formatAudioSummary(session);
  }

  return (
    [
      formatCodec(session.target_audio_codec || "aac"),
      formatChannelLayout(session.source_audio_channels),
    ]
      .filter(Boolean)
      .join(" ") || "Audio transcode"
  );
}

export function formatAudioDetail(session: AdminSession): string {
  const decision = normalizeStreamDecision(
    session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method),
  );
  if (decision === "transcode") {
    const target = [
      formatCodec(session.target_audio_codec || "aac"),
      formatChannelLayout(session.source_audio_channels),
    ]
      .filter(Boolean)
      .join(" ");
    return target ? `→ ${target}` : "Audio transcode";
  }
  if (decision === "copy") {
    return "Audio stream copied";
  }
  if (decision === "direct") {
    return "No audio conversion";
  }
  return "—";
}

function hasRequestedSourceSwitch(session: AdminSession): boolean {
  return (
    session.requested_media_file_id > 0 &&
    session.media_file_id > 0 &&
    session.requested_media_file_id !== session.media_file_id
  );
}

function formatRequestedVideoSource(session: AdminSession): string | null {
  const resolution = session.requested_video_resolution?.trim();
  const codec = formatCodec(session.requested_video_codec);
  const value = [codec, resolution].filter(Boolean).join(" · ");
  return value || null;
}

function formatCodec(codec?: string): string | null {
  const trimmed = codec?.trim();
  return trimmed ? formatCodecLabel(trimmed) : null;
}

function formatContainer(container?: string): string | null {
  const trimmed = container?.trim();
  return trimmed ? trimmed.toUpperCase() : null;
}

export function getPlaybackSessionTitle(session: AdminSession): string {
  if (session.series_name && session.season_number != null && session.episode_number != null) {
    return session.episode_name || `S${session.season_number}E${session.episode_number}`;
  }
  return session.media_title || `File #${session.media_file_id}`;
}

export function getPlaybackSessionSubtitle(session: AdminSession): string | null {
  if (session.series_name && session.season_number != null && session.episode_number != null) {
    const episode = `S${session.season_number}E${session.episode_number}`;
    return session.series_name ? `${episode} · ${session.series_name}` : episode;
  }
  if (session.media_type === "movie") {
    return "Movie";
  }
  if (session.media_type === "series") {
    return "Series";
  }
  return null;
}

function formatChannelLayout(channels?: number | null): string | null {
  if (!channels || channels <= 0) {
    return null;
  }
  if (channels === 1) {
    return "1.0";
  }
  if (channels === 2) {
    return "2.0";
  }
  if (channels === 6) {
    return "5.1";
  }
  if (channels === 8) {
    return "7.1";
  }
  return `${channels}ch`;
}
