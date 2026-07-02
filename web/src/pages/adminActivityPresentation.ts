import type { AdminSession } from "@/api/types";
import { formatCodecLabel } from "@/player/playback-info";

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
