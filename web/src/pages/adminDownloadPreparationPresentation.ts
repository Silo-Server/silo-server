import type {
  AdminDownloadPreparation,
  AdminDownloadPreparationAction,
  AdminDownloadPreparationActionResult,
  AdminDownloadPreparationList,
} from "@/api/v2/adminDownloadPreparations";
import { formatChannels, formatCodecLabel, formatFileSize } from "@/lib/mediaFormat";

export type PreparationState = AdminDownloadPreparation["state"];

export const PREPARATION_STATES: readonly PreparationState[] = [
  "running",
  "queued",
  "retrying",
  "paused",
  "failed",
];

/** Jobs still to finish: running, queued and retrying. Paused and failed jobs wait on an admin. */
export function activePreparationCount(
  counts: AdminDownloadPreparationList["counts"] | undefined,
): number {
  return counts ? counts.running + counts.queued + counts.retrying : 0;
}

export interface PreparationStateMeta {
  label: string;
  swatchClass: string;
}

const STATE_META: Record<PreparationState, PreparationStateMeta> = {
  running: { label: "Encoding", swatchClass: "bg-primary" },
  queued: { label: "Queued", swatchClass: "bg-muted-foreground/50" },
  retrying: { label: "Retrying", swatchClass: "bg-warning" },
  paused: { label: "Paused", swatchClass: "bg-muted-foreground/25" },
  failed: { label: "Failed (24 h)", swatchClass: "bg-destructive" },
};

export function preparationStateMeta(state: PreparationState): PreparationStateMeta {
  return STATE_META[state];
}

/** Whether a job can be paused: it is unfinished and not paused yet. */
export function canPausePreparation(p: AdminDownloadPreparation): boolean {
  return p.state === "running" || p.state === "queued" || p.state === "retrying";
}

export function canResumePreparation(p: AdminDownloadPreparation): boolean {
  return p.state === "paused";
}

/** The running verb: a remux copies streams, so it is not "encoding". */
export function preparationRunningLabel(p: AdminDownloadPreparation): string {
  return p.format === "remux" ? "Remuxing" : "Encoding";
}

export function isEpisodePreparation(p: AdminDownloadPreparation): boolean {
  return Boolean(p.series_name) && p.season_number != null && p.episode_number != null;
}

function episodeCode(p: AdminDownloadPreparation): string {
  return `S${String(p.season_number).padStart(2, "0")}E${String(p.episode_number).padStart(2, "0")}`;
}

/** Row title: the episode's own name, or the movie title. */
export function preparationTitle(p: AdminDownloadPreparation): string {
  if (isEpisodePreparation(p)) return p.episode_name || episodeCode(p);
  return p.media_title || `File #${p.media_file_id}`;
}

/** Row subtitle: "S02E01 · Severance" for episodes, "Movie" otherwise. */
export function preparationSubtitle(p: AdminDownloadPreparation): string {
  if (isEpisodePreparation(p)) return `${episodeCode(p)} · ${p.series_name}`;
  return p.media_type === "movie" ? "Movie" : p.media_type;
}

/** One-line title for tight spaces: "Severance S02E01" or the movie title. */
export function preparationCompactTitle(p: AdminDownloadPreparation): string {
  if (isEpisodePreparation(p)) return `${p.series_name} ${episodeCode(p)}`;
  return preparationTitle(p);
}

export function formatPreparationSource(p: AdminDownloadPreparation): string {
  const video = [p.source.resolution, formatCodecLabel(p.source.video_codec, "")]
    .filter(Boolean)
    .join(" ");
  return [
    p.source.container?.toUpperCase(),
    [video, p.source.hdr ? "HDR" : ""].filter(Boolean).join(" "),
    formatFileSize(p.source.file_size),
  ]
    .filter(Boolean)
    .join(" · ");
}

export function formatPreparationBitrate(kbps?: number): string {
  if (!kbps || kbps <= 0) return "";
  const mbps = kbps / 1000;
  return `${Number.isInteger(mbps) ? mbps : mbps.toFixed(1)} Mbps`;
}

/** What the prepared file will be, e.g. "1080p H.264 · 10 Mbps · HDR → SDR". */
export function formatPreparationOutput(p: AdminDownloadPreparation): string {
  if (p.format === "remux") {
    return `${p.output.container.toUpperCase()} · streams copied`;
  }
  const video =
    p.output.video_codec === "copy"
      ? "video copied"
      : [p.output.resolution, formatCodecLabel(p.output.video_codec, "")].filter(Boolean).join(" ");
  return [
    video,
    formatPreparationBitrate(p.output.bitrate_kbps),
    p.output.tone_map_mode ? "HDR → SDR" : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

/** Short job label: "Remux" or "Transcode 1080p H.264". */
export function formatPreparationKind(p: AdminDownloadPreparation): string {
  if (p.format === "remux") return "Remux";
  const target =
    p.output.video_codec === "copy"
      ? ""
      : [p.output.resolution, formatCodecLabel(p.output.video_codec, "")].filter(Boolean).join(" ");
  return target ? `Transcode ${target}` : "Transcode";
}

export function formatPreparationVideoOutput(p: AdminDownloadPreparation): string {
  if (p.output.video_codec === "copy") return "Copied";
  const bitrate = formatPreparationBitrate(p.output.bitrate_kbps);
  return [
    [p.output.resolution, formatCodecLabel(p.output.video_codec, "")].filter(Boolean).join(" "),
    bitrate ? `up to ${bitrate}` : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

export function formatPreparationToneMap(p: AdminDownloadPreparation): string | null {
  switch (p.output.tone_map_mode) {
    case "hardware":
      return "HDR → SDR (hardware)";
    case "software":
      return "HDR → SDR (software)";
    case undefined:
    case "":
      return null;
    default:
      return "HDR → SDR";
  }
}

export function formatPreparationAudioOutput(p: AdminDownloadPreparation): string {
  const codec = p.output.audio_codec === "copy" ? "copied" : formatCodecLabel(p.output.audio_codec);
  if (p.output.all_audio_tracks) {
    const count = p.source.audio_tracks.length;
    const tracks = count === 1 ? "1 track" : count > 1 ? `All ${count} tracks` : "All tracks";
    return p.output.audio_codec === "copy"
      ? `${tracks} copied`
      : `${tracks} → ${p.output.audio_codec === "aac" ? "stereo AAC" : codec}`;
  }
  return p.output.audio_codec === "copy" ? "Copied" : codec;
}

export function formatPreparationSourceAudio(p: AdminDownloadPreparation): string {
  const tracks = p.source.audio_tracks;
  if (tracks.length === 0) return "—";
  const labels = tracks.map((track) =>
    [formatCodecLabel(track.codec, ""), formatChannels(track.channels)].filter(Boolean).join(" "),
  );
  const shown = labels.slice(0, 3).join(", ");
  const more = labels.length > 3 ? ` +${labels.length - 3}` : "";
  return `${tracks.length} ${tracks.length === 1 ? "track" : "tracks"} · ${shown}${more}`;
}

export interface PreparationWorker {
  key: string;
  label: "Node" | "Server";
  name: string;
}

export function preparationWorker(p: AdminDownloadPreparation): PreparationWorker | null {
  if (!p.worker) return null;
  if (p.worker.kind === "node") {
    return {
      key: `node:${p.worker.node_id ?? p.worker.name}`,
      label: "Node",
      name: p.worker.name || `Node ${p.worker.node_id ?? ""}`.trim(),
    };
  }
  return { key: `server:${p.worker.name}`, label: "Server", name: p.worker.name || "Local server" };
}

/** Percent complete in [0, 100], or null when progress or duration is unknown. */
export function preparationPercent(p: AdminDownloadPreparation): number | null {
  const progress = p.progress;
  if (!progress || progress.duration_seconds <= 0) return null;
  const percent = (progress.encoded_seconds / progress.duration_seconds) * 100;
  return Math.max(0, Math.min(100, percent));
}

/** Seconds of wall time left at the current speed, or null when unknown. */
export function preparationRemainingSeconds(p: AdminDownloadPreparation): number | null {
  const progress = p.progress;
  if (!progress || progress.duration_seconds <= 0 || progress.speed <= 0) return null;
  return Math.max(0, progress.duration_seconds - progress.encoded_seconds) / progress.speed;
}

export function formatRemaining(seconds: number | null): string {
  if (seconds == null) return "";
  if (seconds < 60) return "under 1 min left";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `about ${minutes} min left`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0 ? `about ${hours} h left` : `about ${hours} h ${rest} min left`;
}

export function formatSpeed(speed?: number): string {
  if (!speed || speed <= 0) return "";
  return `${speed >= 10 ? Math.round(speed) : speed.toFixed(1)}×`;
}

export function formatClock(seconds: number): string {
  const safe = Math.max(0, Math.floor(Number.isFinite(seconds) ? seconds : 0));
  const h = Math.floor(safe / 3600);
  const m = Math.floor((safe % 3600) / 60);
  const s = safe % 60;
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
  return `${m}:${String(s).padStart(2, "0")}`;
}

export function secondsSince(iso: string | undefined, now = Date.now()): number | null {
  if (!iso) return null;
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return null;
  return Math.max(0, (now - at) / 1000);
}

/** "in 45 s", "in 3 min", or "now" for a retry time. */
export function formatRetryIn(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "";
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return "";
  const seconds = Math.round((at - now) / 1000);
  if (seconds <= 0) return "now";
  if (seconds < 60) return `in ${seconds} s`;
  return `in ${Math.round(seconds / 60)} min`;
}

/** "12 min ago" style age for a past instant. */
export function formatAgo(iso: string | undefined, now = Date.now()): string {
  const seconds = secondsSince(iso, now);
  if (seconds == null) return "";
  if (seconds < 60) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  return `${hours} h ago`;
}

export function requesterName(r: AdminDownloadPreparation["requesters"][number]): string {
  return r.username || `User #${r.user_id}`;
}

export function requesterDevice(r: AdminDownloadPreparation["requesters"][number]): string {
  if (!r.device_id) return "Web download";
  return r.device_name || r.device_platform || "Unnamed device";
}

/** Text a filter box matches: title, series, requesters, and worker. */
export function preparationSearchText(p: AdminDownloadPreparation): string {
  const worker = preparationWorker(p);
  return [
    p.media_title,
    p.series_name,
    p.episode_name,
    worker ? `${worker.label} ${worker.name}` : "",
    ...p.requesters.flatMap((r) => [r.username, r.profile_name, r.device_name]),
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

function jobs(n: number): string {
  return `${n.toLocaleString()} ${n === 1 ? "job" : "jobs"}`;
}

const ACTION_PAST: Record<AdminDownloadPreparationAction, string> = {
  pause: "Paused",
  resume: "Resumed",
  cancel: "Canceled",
};

/** One sentence per outcome of an action, e.g. "Paused 3 jobs. Skipped 1 job that failed." */
export function summarizePreparationAction(
  action: AdminDownloadPreparationAction,
  results: readonly AdminDownloadPreparationActionResult[],
): string {
  const count = (outcome: AdminDownloadPreparationActionResult["outcome"]) =>
    results.filter((r) => r.outcome === outcome).length;
  const applied = count("applied");
  const unchanged = count("unchanged");
  const notApplicable = count("not_applicable");
  const notFound = count("not_found");
  const parts: string[] = [];
  if (applied > 0) parts.push(`${ACTION_PAST[action]} ${jobs(applied)}.`);
  if (unchanged > 0) {
    parts.push(
      action === "pause"
        ? `${jobs(unchanged)} ${unchanged === 1 ? "was" : "were"} already paused.`
        : `${jobs(unchanged)} ${unchanged === 1 ? "wasn't" : "weren't"} paused.`,
    );
  }
  if (notApplicable > 0) parts.push(`Skipped ${jobs(notApplicable)} that failed.`);
  if (notFound > 0) {
    parts.push(`${jobs(notFound)} had already finished or been canceled.`);
  }
  return parts.join(" ") || "Nothing changed.";
}

export interface CancelPreparationsPrompt {
  title: string;
  description: string;
  confirmLabel: string;
}

/** Confirmation copy for canceling jobs, naming what the requesters lose. */
export function cancelPreparationsPrompt(
  targets: readonly AdminDownloadPreparation[],
): CancelPreparationsPrompt {
  const waiting = targets.reduce(
    (sum, t) => sum + t.requesters.filter((r) => r.status === "preparing").length,
    0,
  );
  const encoding = targets.some((t) => t.state === "running");
  const description = [
    encoding ? "Encoding stops and its progress is lost." : "",
    waiting > 0
      ? `${waiting.toLocaleString()} waiting ${waiting === 1 ? "download fails" : "downloads fail"} with "Canceled by an administrator". Users can download again later, which starts a new job.`
      : `No downloads are waiting on ${targets.length === 1 ? "this job" : "these jobs"}.`,
  ]
    .filter(Boolean)
    .join(" ");

  if (targets.length > 1) {
    return {
      title: `Cancel ${jobs(targets.length)}?`,
      description,
      confirmLabel: `Cancel ${jobs(targets.length)}`,
    };
  }
  const name = targets[0] ? preparationCompactTitle(targets[0]) : "";
  if (targets[0]?.state === "failed") {
    return { title: `Remove the failed job for ${name}?`, description, confirmLabel: "Remove" };
  }
  return { title: `Cancel preparing ${name}?`, description, confirmLabel: "Cancel job" };
}
