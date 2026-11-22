import type { AdminSession } from "@/api/types";
import { codeNumber, englishText, t, type TextValues } from "@/i18n";
import type { MessageKey } from "@/locales/keys";
import { formatCodecLabel } from "@/lib/mediaFormat";
import { formatDecimal } from "@/lib/numberFormat";
import { formatClock } from "@/pages/adminDownloadPreparationPresentation";

export function formatDecisionLabel(decision?: string): string {
  switch (decision) {
    case "direct":
      return t("web.admin.activity.decision.direct");
    case "copy":
      return t("web.admin.activity.decision.copy");
    case "remux":
      return t("web.admin.activity.decision.remux");
    case "hls":
      // A protocol name, the same in every language.
      return "HLS";
    case "transcode":
      return t("web.admin.activity.decision.transcode");
    default:
      return t("web.admin.activity.decision.unknown");
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
 *   - only audio is re-encoded       -> "direct_stream"
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
    return "direct_stream";
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
// direct-stream tag between remux and video transcode, matching Jellyfin's
// lowest-to-highest server-work ordering; unknown sorts last.
const ACTIVITY_METHOD_ORDER = ["direct", "remux", "direct_stream", "transcode", "unknown"];

function activityMethodRank(method: string): number {
  const index = ACTIVITY_METHOD_ORDER.indexOf(method);
  return index === -1 ? ACTIVITY_METHOD_ORDER.length : index;
}

/** Sort comparator for activity method keys. Falls back to alphabetical for
 * anything outside the known order. */
export function compareActivityMethods(a: string, b: string): number {
  const diff = activityMethodRank(a) - activityMethodRank(b);
  return diff !== 0 ? diff : a.localeCompare(b);
}

export interface ActivityMethodMeta {
  /** Human label ("Direct Play"); the bucket key itself is the short tag. */
  label: string;
  /** Explain the whole-session scope without implying every stream is direct. */
  description: string;
  /** Solid swatch class for distribution bars and legend dots. */
  swatchClass: string;
  /** Tinted badge classes for the per-row method tag. */
  badgeClass: string;
}

interface ActivityMethodStyle {
  label: MessageKey;
  description: MessageKey;
  swatchClass: string;
  badgeClass: string;
}

// Single source for the bucket -> label/color mapping. Every surface that
// renders method tags, distribution bars, or legend dots derives from this so
// the Admin Activity page, the dashboard stream cards, and the Server Activity
// popover cannot drift apart. Labels are message keys, resolved per call.
const ACTIVITY_METHOD_META: Record<string, ActivityMethodStyle> = {
  direct: {
    label: "web.common.activity.method.direct.label",
    description: "web.common.activity.method.direct.description",
    swatchClass: "bg-success",
    badgeClass: "bg-success/10 text-success-label border-success/15",
  },
  remux: {
    label: "web.common.activity.method.remux.label",
    description: "web.common.activity.method.remux.description",
    swatchClass: "bg-info",
    badgeClass: "bg-info/10 text-info-label border-info/15",
  },
  direct_stream: {
    label: "web.common.activity.method.directStream.label",
    description: "web.common.activity.method.directStream.description",
    swatchClass: "bg-warning",
    badgeClass: "bg-warning/10 text-warning-label border-warning/15",
  },
  transcode: {
    label: "web.common.activity.method.transcode.label",
    description: "web.common.activity.method.transcode.description",
    swatchClass: "bg-destructive",
    badgeClass: "bg-destructive/10 text-destructive-label border-destructive/15",
  },
};

const UNKNOWN_ACTIVITY_METHOD_META: ActivityMethodStyle = {
  label: "web.common.activity.method.unknown.label",
  description: "web.common.activity.method.unknown.description",
  swatchClass: "bg-muted-foreground",
  badgeClass: "bg-surface text-muted-foreground border-border",
};

/** Presentation (label + colors) for an activity method bucket. */
export function activityMethodMeta(method: string): ActivityMethodMeta {
  const style =
    (Object.hasOwn(ACTIVITY_METHOD_META, method) ? ACTIVITY_METHOD_META[method] : undefined) ??
    UNKNOWN_ACTIVITY_METHOD_META;
  return {
    label: t(style.label),
    description: t(style.description),
    swatchClass: style.swatchClass,
    badgeClass: style.badgeClass,
  };
}

/**
 * Badge classes for a component decision value (direct/copy/remux/hls/
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
  // Every other label in this function names the video encoder's HW/SW mode,
  // which does not exist when only audio is re-encoded: "Audio SW" read as a
  // client-side software capability instead of "the audio stream is being
  // re-encoded". Name the work, not an acceleration mode.
  if (videoDecision !== "transcode") {
    return t("web.admin.activity.transcodeMode.audioTranscode");
  }

  const hwAccel = session.transcode_hw_accel?.trim().toLowerCase();
  switch (hwAccel) {
    case "qsv":
      return t("web.admin.activity.transcodeMode.hardware", { accel: "QSV" });
    case "vaapi":
      return t("web.admin.activity.transcodeMode.hardware", { accel: "VAAPI" });
    case "videotoolbox":
      return t("web.admin.activity.transcodeMode.hardware", { accel: "VideoToolbox" });
    case "none":
      return t("web.admin.activity.transcodeMode.software");
    case "auto":
      return t("web.admin.activity.transcodeMode.pending");
    case "":
    case undefined:
      return t("web.admin.activity.transcodeMode.unknown");
    default:
      return t("web.admin.activity.transcodeMode.hardware", { accel: hwAccel.toUpperCase() });
  }
}

/** Labels for a confirmed HDR-to-SDR executor in compact and detailed views. */
export interface ToneMapSummary {
  /** "HW Tone map" / "SW Tone map" in English. */
  badge: string;
  /** "Hardware" / "Software" in English. */
  detail: string;
  mode: "hardware" | "software";
}

/** Format a confirmed HDR-to-SDR executor without guessing from legacy data. */
export function formatToneMapSummary(session: AdminSession): ToneMapSummary | null {
  const videoDecision = normalizeStreamDecision(session.video_decision || session.play_method);
  if (videoDecision !== "transcode") {
    return null;
  }

  switch (session.tone_map_mode?.trim().toLowerCase()) {
    case "hardware":
      return {
        badge: t("web.admin.activity.toneMap.hardwareBadge"),
        detail: t("web.admin.activity.toneMap.hardware"),
        mode: "hardware",
      };
    case "software":
      return {
        badge: t("web.admin.activity.toneMap.softwareBadge"),
        detail: t("web.admin.activity.toneMap.software"),
        mode: "software",
      };
    default:
      return null;
  }
}

// Bitrate units (Mbps, kbps) are written the same way in every language. A
// no-break space keeps the unit on its number's line when a narrow cell wraps.
export function formatSessionBitrate(kbps?: number | null): string | null {
  if (!kbps || kbps <= 0) {
    return null;
  }
  if (kbps >= 1000) {
    return `${formatDecimal(kbps / 1000, 1)}\u00a0Mbps`;
  }
  return `${Math.round(kbps)}\u00a0kbps`;
}

export interface SessionBitrate {
  /** The rate the viewer receives now. */
  live: string | null;
  /** The source file's bitrate. */
  source: string | null;
  /** The ceiling a transcode encodes to. */
  limit: string | null;
  /** Delivered rate as a share of the source's, 0 to 1; null when either is unknown. */
  share: number | null;
}

export function getSessionBitrate(session: AdminSession): SessionBitrate {
  const live = session.stream_bitrate_kbps ?? 0;
  const source = session.source_bitrate_kbps ?? 0;
  return {
    live: formatSessionBitrate(live),
    source: formatSessionBitrate(source),
    limit: formatSessionBitrate(session.target_bitrate_kbps),
    share: live > 0 && source > 0 ? Math.min(1, live / source) : null,
  };
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

/**
 * The exact client identity — version, build, and non-release channel — for
 * surfaces that can afford the width (expanded session details, tooltips).
 * `getSessionClientLabel` stays the compact list label; this one must never
 * replace it in a fixed-width row.
 */
export function getSessionClientLabelFull(session: AdminSession): string {
  // The server omits client_label_full when it would repeat client_label, and
  // older servers never send it at all — both degrade to the compact label.
  return session.client_label_full?.trim() || getSessionClientLabel(session);
}

export interface ActivityRouteNode {
  key: string;
  kind: "transcode" | "proxy" | "server" | "legacy";
  /** The node's role: "Transcode", "Proxy", "Server" or "Node" in English. */
  label: string;
  name: string;
}

/** Formats a message id: `t` for display, `englishText` for search and sort keys. */
type FormatText = (id: MessageKey, values?: TextValues) => string;

function namedRouteNode(
  format: FormatText,
  kind: "transcode" | "proxy",
  name: string | undefined,
  id: number | undefined,
): ActivityRouteNode {
  const label =
    kind === "transcode"
      ? format("web.admin.activity.route.transcode")
      : format("web.admin.activity.route.proxy");
  const trimmedName = name?.trim();
  const unknownName =
    kind === "transcode"
      ? format("web.admin.activity.route.unknownTranscodeNode")
      : format("web.admin.activity.route.unknownProxyNode");
  return {
    key: `${kind}:${id ?? trimmedName ?? "unknown"}`,
    kind,
    label,
    name:
      trimmedName ||
      (id !== undefined
        ? format("web.admin.activity.route.nodeId", { id: String(id) })
        : unknownName),
  };
}

function reportingServerRouteNode(format: FormatText, session: AdminSession): ActivityRouteNode {
  const reportedName = session.reporting_node?.trim();
  if (!reportedName || reportedName.toLowerCase() === "local") {
    return {
      key: "server:local",
      kind: "server",
      label: format("web.admin.activity.route.server"),
      name: format("web.admin.activity.route.localServer"),
    };
  }
  return {
    key: `server:${reportedName}`,
    kind: "server",
    label: format("web.admin.activity.route.server"),
    name: reportedName,
  };
}

/**
 * Return registered playback nodes in work-to-viewer order. Routes without a
 * registered node identify the API server, while rows from
 * older servers fall back to their legacy node fields.
 */
export function getSessionRouteNodes(
  session: AdminSession,
  format: FormatText = t,
): ActivityRouteNode[] {
  const execution = session.routing_execution?.trim();
  const egress = session.routing_egress?.trim();
  const hasResolvedRoute = Boolean(session.routing_workload?.trim() || execution || egress);

  if (!hasResolvedRoute) {
    const reportedName = session.node_display_name?.trim() || session.reporting_node?.trim();
    const isLocal = !reportedName || reportedName.toLowerCase() === "local";
    // The key must not follow the interface language: filters hold it.
    return [
      {
        key: isLocal ? "legacy:local" : `legacy:${reportedName}`,
        kind: "legacy",
        label: format("web.admin.activity.route.node"),
        name: isLocal ? format("web.admin.activity.route.localServer") : reportedName,
      },
    ];
  }

  const nodes: ActivityRouteNode[] = [];
  const executionNodeName =
    session.routing_execution_node_name?.trim() || session.node_display_name;
  if (execution === "transcode") {
    nodes.push(
      namedRouteNode(format, "transcode", executionNodeName, session.routing_execution_node_id),
    );
  } else if (execution === "proxy") {
    nodes.push(
      namedRouteNode(format, "proxy", executionNodeName, session.routing_execution_node_id),
    );
  }

  if (egress === "proxy") {
    const egressNode = namedRouteNode(
      format,
      "proxy",
      session.routing_egress_node_name,
      session.routing_egress_node_id,
    );
    if (!nodes.some((node) => node.key === egressNode.key)) {
      nodes.push(egressNode);
    }
  }

  if (egress === "api" || (execution === "api" && nodes.length === 0)) {
    nodes.push(reportingServerRouteNode(format, session));
  }
  return nodes.length > 0 ? nodes : [reportingServerRouteNode(format, session)];
}

export function formatSourceContainerSummary(session: AdminSession): string {
  return formatContainer(session.source_container) || t("web.admin.activity.summary.unknownSource");
}

export function formatDeliveredContainerSummary(session: AdminSession): string {
  if (normalizeContainerDecision(session.play_method) === "direct") {
    return formatSourceContainerSummary(session);
  }
  const container = session.output_container?.trim().toLowerCase();
  if (container) {
    const label =
      container === "fmp4"
        ? "fMP4"
        : container === "mpegts"
          ? "MPEG-TS"
          : formatContainer(container) || t("web.admin.activity.summary.unknownOutputContainer");
    return session.output_protocol === "hls"
      ? t("web.admin.activity.summary.hlsOutput", { container: label })
      : label;
  }
  const unknown = t("web.admin.activity.summary.unknownOutputContainer");
  return session.output_protocol === "hls"
    ? t("web.admin.activity.summary.hlsOutput", { container: unknown })
    : unknown;
}

export function formatVideoSummary(session: AdminSession): string {
  return (
    [formatCodec(session.source_video_codec), session.source_video_resolution?.trim()]
      .filter(Boolean)
      .join(" · ") || t("web.admin.activity.summary.unknownSource")
  );
}

/**
 * The catalog labels a file by its size class (a 3840x1600 file is "2160p"),
 * while a transcode target names its output height ("1600p"). A transcode
 * that keeps the source height is labelled like the source, so a
 * full-resolution output doesn't read as a downscale.
 */
function deliveredResolutionLabel(session: AdminSession): string | undefined {
  const target = session.target_resolution?.trim();
  const source = session.source_video_resolution?.trim();
  const targetHeight = Number(/^(\d+)p$/i.exec(target ?? "")?.[1]);
  if (source && session.source_video_height && targetHeight === session.source_video_height) {
    return source;
  }
  return target;
}

export function formatDeliveredVideoSummary(session: AdminSession): string {
  const decision = session.video_decision || session.play_method;
  if (decision !== "transcode") {
    return formatVideoSummary(session);
  }

  return (
    [formatCodec(session.target_video_codec), deliveredResolutionLabel(session)]
      .filter(Boolean)
      .join(" · ") || t("web.admin.activity.detail.transcoding")
  );
}

export function formatAudioSummary(session: AdminSession): string {
  const lead = session.source_audio_title?.trim() || session.source_audio_language?.trim();
  const format = [
    formatCodec(session.source_audio_codec),
    formatChannelLayout(session.source_audio_channels),
  ]
    .filter(Boolean)
    .join(" ");
  return (
    [lead, format].filter(Boolean).join(" · ") || t("web.admin.activity.summary.unknownSource")
  );
}

/**
 * The audio a transcode actually delivers: the target codec, plus the target
 * channel layout only when the server reported one. The source channel count is
 * deliberately not a fallback — a TrueHD 7.1 source downmixed to AAC 5.1 read as
 * "AAC 7.1" while it did. Servers that do not send `target_audio_channels` get
 * the bare codec instead of an invented layout.
 */
function formatTargetAudio(session: AdminSession): string {
  return [
    formatCodec(session.target_audio_codec || "aac"),
    formatChannelLayout(session.target_audio_channels),
  ]
    .filter(Boolean)
    .join(" ");
}

export function formatDeliveredAudioSummary(session: AdminSession): string {
  const decision =
    session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method);
  if (decision !== "transcode") {
    return formatAudioSummary(session);
  }

  return formatTargetAudio(session) || t("web.admin.activity.transcodeMode.audioTranscode");
}

/** One stream's source format and, when the server converts it, what it delivers. */
export interface StreamConversion {
  /** The source format, compact: "HEVC 2160p", "TrueHD 7.1". */
  source: string;
  /** The delivered format; null when the stream reaches the viewer untouched. */
  delivered: string | null;
}

function compactFormat(parts: (string | null | undefined)[]): string {
  return parts.filter(Boolean).join(" ");
}

/** The video's source format and, for a video transcode, its output. */
export function getVideoConversion(session: AdminSession): StreamConversion {
  const source =
    compactFormat([
      formatCodec(session.source_video_codec),
      session.source_video_resolution?.trim(),
    ]) || t("web.admin.activity.summary.unknownSource");
  if (normalizeStreamDecision(session.video_decision || session.play_method) !== "transcode") {
    return { source, delivered: null };
  }
  return {
    source,
    delivered:
      compactFormat([formatCodec(session.target_video_codec), deliveredResolutionLabel(session)]) ||
      t("web.admin.activity.detail.transcoding"),
  };
}

/** The audio's source format and, when the audio is re-encoded, its output. */
export function getAudioConversion(session: AdminSession): StreamConversion {
  const source =
    compactFormat([
      formatCodec(session.source_audio_codec),
      formatChannelLayout(session.source_audio_channels),
    ]) || t("web.admin.activity.summary.unknownSource");
  const decision = normalizeStreamDecision(
    session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method),
  );
  if (decision !== "transcode") {
    return { source, delivered: null };
  }
  return {
    source,
    delivered: formatTargetAudio(session) || t("web.admin.activity.transcodeMode.audioTranscode"),
  };
}

/** How far through the title playback is, 0 to 1; null when the length is unknown. */
export function getPlaybackProgress(session: AdminSession): number | null {
  if (session.file_duration == null || session.file_duration <= 0) {
    return null;
  }
  return Math.min(1, Math.max(0, session.position_seconds / session.file_duration));
}

export interface ActivityTotals {
  /** Sessions whose video is re-encoded. */
  videoTranscodes: number;
  /** The summed delivered bitrate of the sessions that report one. */
  bitrateKbps: number;
  remote: number;
  paused: number;
}

/** The counts the page summary line reports. */
export function summarizeActivity(sessions: AdminSession[]): ActivityTotals {
  const totals: ActivityTotals = { videoTranscodes: 0, bitrateKbps: 0, remote: 0, paused: 0 };
  for (const session of sessions) {
    if (classifyActivityMethod(session) === "transcode") totals.videoTranscodes++;
    if (session.stream_bitrate_kbps && session.stream_bitrate_kbps > 0) {
      totals.bitrateKbps += session.stream_bitrate_kbps;
    }
    if (session.stream_location === "remote") totals.remote++;
    if (session.is_paused) totals.paused++;
  }
  return totals;
}

/**
 * Whether the route column says anything a row's location doesn't: some
 * session uses a non-default network, or the sessions take different routes.
 * On a single server every row's route is the same, so the column is hidden.
 */
export function sessionRoutesVary(sessions: AdminSession[]): boolean {
  const routes = new Set<string>();
  for (const session of sessions) {
    if (session.routing_network_provider) {
      return true;
    }
    routes.add(
      getSessionRouteNodes(session, englishText)
        .map((node) => node.key)
        .join(","),
    );
  }
  return routes.size > 1;
}

/**
 * "Auto-switched from HEVC · 2160p" when the server plays another version of
 * the title than the one the viewer picked; null otherwise.
 */
export function formatRequestedSourceSwitch(session: AdminSession): string | null {
  const source = formatRequestedVideoSource(session);
  return hasRequestedSourceSwitch(session) && source
    ? t("web.admin.activity.detail.autoSwitched", { source })
    : null;
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
  if (session.series_title && session.season_number != null && session.episode_number != null) {
    return session.media_title || `S${session.season_number}E${session.episode_number}`;
  }
  return session.media_title || t("web.common.activity.title.file", { id: session.media_file_id });
}

export function getPlaybackSessionSubtitle(session: AdminSession): string | null {
  if (session.series_title && session.season_number != null && session.episode_number != null) {
    const code = t("web.detail.episodeCode", {
      season: codeNumber(session.season_number),
      episode: codeNumber(session.episode_number),
    });
    return t("web.common.activity.subtitle.episode", {
      code,
      series: session.series_title,
    });
  }
  if (session.media_type === "movie") {
    return t("web.common.activity.subtitle.movie");
  }
  if (session.media_type === "series" || session.media_type === "episode") {
    return t("web.common.activity.subtitle.series");
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

/** Playback position as "47:12 / 2:29:40", or the position alone when the duration is unknown. */
export function formatPlaybackPosition(session: AdminSession): string {
  const position = formatClock(session.position_seconds);
  if (session.file_duration == null || session.file_duration <= 0) {
    return position;
  }
  return `${position} / ${formatClock(session.file_duration)}`;
}
