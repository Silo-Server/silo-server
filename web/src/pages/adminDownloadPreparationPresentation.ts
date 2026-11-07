import type {
  AdminDownloadPreparation,
  AdminDownloadPreparationAction,
  AdminDownloadPreparationActionResult,
  AdminDownloadPreparationList,
} from "@/api/v2/adminDownloadPreparations";
import { formatChannels, formatCodecLabel, formatFileSize } from "@/lib/mediaFormat";
import { formatLocale } from "@/lib/appLocale";
import { t, type MessageKey } from "@/i18n";
import { formatDecimal } from "@/lib/numberFormat";

const K = "web.admin.downloads.preparation";

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

const STATE_META: Record<PreparationState, { label: MessageKey; swatchClass: string }> = {
  running: { label: `${K}.state.running`, swatchClass: "bg-primary" },
  queued: { label: `${K}.state.queued`, swatchClass: "bg-muted-foreground/50" },
  retrying: { label: `${K}.state.retrying`, swatchClass: "bg-warning" },
  paused: { label: `${K}.state.paused`, swatchClass: "bg-muted-foreground/25" },
  failed: { label: `${K}.state.failed`, swatchClass: "bg-destructive" },
};

export function preparationStateMeta(state: PreparationState): PreparationStateMeta {
  const meta = STATE_META[state];
  return { label: t(meta.label), swatchClass: meta.swatchClass };
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
  return t(p.format === "remux" ? `${K}.running.remuxing` : `${K}.running.encoding`);
}

export function isEpisodePreparation(p: AdminDownloadPreparation): boolean {
  return Boolean(p.series_name) && p.season_number != null && p.episode_number != null;
}

function episodeCode(p: AdminDownloadPreparation): string {
  return t(`${K}.episodeCode`, {
    season: String(p.season_number).padStart(2, "0"),
    episode: String(p.episode_number).padStart(2, "0"),
  });
}

/** Row title: the episode's own name, or the movie title. */
export function preparationTitle(p: AdminDownloadPreparation): string {
  if (isEpisodePreparation(p)) return p.episode_name || episodeCode(p);
  return p.media_title || t(`${K}.fileFallback`, { id: String(p.media_file_id) });
}

/** Row subtitle: "S02E01 · Severance" for episodes, "Movie" otherwise. */
export function preparationSubtitle(p: AdminDownloadPreparation): string {
  if (isEpisodePreparation(p)) {
    return t(`${K}.subtitle.episode`, { code: episodeCode(p), series: p.series_name ?? "" });
  }
  return p.media_type === "movie" ? t(`${K}.subtitle.movie`) : p.media_type;
}

/** One-line title for tight spaces: "Severance S02E01" or the movie title. */
export function preparationCompactTitle(p: AdminDownloadPreparation): string {
  if (isEpisodePreparation(p)) {
    return t(`${K}.compactEpisodeTitle`, { series: p.series_name ?? "", code: episodeCode(p) });
  }
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
  return t(`${K}.bitrate`, { value: formatDecimal(mbps, Number.isInteger(mbps) ? 0 : 1) });
}

/** What the prepared file will be, e.g. "1080p H.264 · 10 Mbps · HDR → SDR". */
export function formatPreparationOutput(p: AdminDownloadPreparation): string {
  if (p.format === "remux") {
    return t(`${K}.output.remux`, { container: p.output.container.toUpperCase() });
  }
  const video =
    p.output.video_codec === "copy"
      ? t(`${K}.output.videoCopied`)
      : [p.output.resolution, formatCodecLabel(p.output.video_codec, "")].filter(Boolean).join(" ");
  return [
    video,
    formatPreparationBitrate(p.output.bitrate_kbps),
    p.output.tone_map_mode ? t(`${K}.output.hdrToSdr`) : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

/** Short job label: "Remux" or "Transcode 1080p H.264". */
export function formatPreparationKind(p: AdminDownloadPreparation): string {
  if (p.format === "remux") return t(`${K}.kind.remux`);
  const target =
    p.output.video_codec === "copy"
      ? ""
      : [p.output.resolution, formatCodecLabel(p.output.video_codec, "")].filter(Boolean).join(" ");
  return target ? t(`${K}.kind.transcodeTarget`, { target }) : t(`${K}.kind.transcode`);
}

export function formatPreparationVideoOutput(p: AdminDownloadPreparation): string {
  if (p.output.video_codec === "copy") return t(`${K}.videoOutput.copied`);
  const bitrate = formatPreparationBitrate(p.output.bitrate_kbps);
  return [
    [p.output.resolution, formatCodecLabel(p.output.video_codec, "")].filter(Boolean).join(" "),
    bitrate ? t(`${K}.videoOutput.upTo`, { bitrate }) : "",
  ]
    .filter(Boolean)
    .join(" · ");
}

export function formatPreparationToneMap(p: AdminDownloadPreparation): string | null {
  switch (p.output.tone_map_mode) {
    case "hardware":
      return t(`${K}.toneMap.hardware`);
    case "software":
      return t(`${K}.toneMap.software`);
    case undefined:
    case "":
      return null;
    default:
      return t(`${K}.toneMap.unnamed`);
  }
}

export function formatPreparationAudioOutput(p: AdminDownloadPreparation): string {
  const copied = p.output.audio_codec === "copy";
  if (p.output.all_audio_tracks) {
    const count = p.source.audio_tracks.length;
    if (copied) return t(`${K}.audioOutput.allTracksCopied`, { count });
    const codec =
      p.output.audio_codec === "aac"
        ? t(`${K}.audioOutput.stereoAac`)
        : formatCodecLabel(p.output.audio_codec);
    return t(`${K}.audioOutput.allTracksConverted`, { count, codec });
  }
  return copied ? t(`${K}.audioOutput.copied`) : formatCodecLabel(p.output.audio_codec);
}

export function formatPreparationSourceAudio(p: AdminDownloadPreparation): string {
  const tracks = p.source.audio_tracks;
  if (tracks.length === 0) return "—";
  const labels = tracks.map((track) =>
    [formatCodecLabel(track.codec, ""), formatChannels(track.channels)].filter(Boolean).join(" "),
  );
  const shown = labels.slice(0, 3).join(", ");
  const more = labels.length > 3 ? ` +${labels.length - 3}` : "";
  return t(`${K}.sourceAudio.tracks`, { count: tracks.length, codecs: `${shown}${more}` });
}

export interface PreparationWorker {
  key: string;
  /** "Node" or "Server", in the interface language. */
  label: string;
  name: string;
}

export function preparationWorker(p: AdminDownloadPreparation): PreparationWorker | null {
  if (!p.worker) return null;
  if (p.worker.kind === "node") {
    return {
      key: `node:${p.worker.node_id ?? p.worker.name}`,
      label: t(`${K}.worker.node`),
      name:
        p.worker.name ||
        (p.worker.node_id
          ? t(`${K}.worker.nodeWithId`, { id: p.worker.node_id })
          : t(`${K}.worker.node`)),
    };
  }
  return {
    key: `server:${p.worker.name}`,
    label: t(`${K}.worker.server`),
    name: p.worker.name || t(`${K}.worker.localServer`),
  };
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
  if (seconds < 60) return t(`${K}.remaining.underOneMinute`);
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return t(`${K}.remaining.minutes`, { minutes });
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0
    ? t(`${K}.remaining.hours`, { hours, hours_text: String(hours) })
    : t(`${K}.remaining.hoursMinutes`, {
        hours,
        hours_text: String(hours),
        minutes: String(rest),
      });
}

export function formatSpeed(speed?: number): string {
  if (!speed || speed <= 0) return "";
  return `${speed >= 10 ? Math.round(speed) : formatDecimal(speed, 1)}×`;
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
  if (seconds <= 0) return t(`${K}.retryIn.now`);
  if (seconds < 60) return t(`${K}.retryIn.seconds`, { seconds });
  const minutes = Math.round(seconds / 60);
  return t(`${K}.retryIn.minutes`, { minutes, minutes_text: String(minutes) });
}

/** "12 min ago" style age for a past instant. */
export function formatAgo(iso: string | undefined, now = Date.now()): string {
  const seconds = secondsSince(iso, now);
  if (seconds == null) return "";
  if (seconds < 60) return t(`${K}.ago.justNow`);
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return t(`${K}.ago.minutes`, { minutes });
  const hours = Math.round(minutes / 60);
  return t(`${K}.ago.hours`, { hours, hours_text: String(hours) });
}

export function requesterName(r: AdminDownloadPreparation["requesters"][number]): string {
  return r.username || t(`${K}.requester.userFallback`, { id: String(r.user_id) });
}

export function requesterDevice(r: AdminDownloadPreparation["requesters"][number]): string {
  if (!r.device_id) return t(`${K}.requester.webDownload`);
  return r.device_name || r.device_platform || t(`${K}.requester.unnamedDevice`);
}

/** Text a filter box matches: title, series, requesters, and worker (shown label and raw kind). */
export function preparationSearchText(p: AdminDownloadPreparation): string {
  const worker = preparationWorker(p);
  return [
    p.media_title,
    p.series_name,
    p.episode_name,
    // The raw kind keeps English searches ("node") working in every language.
    worker ? `${p.worker?.kind} ${worker.label} ${worker.name}` : "",
    ...p.requesters.flatMap((r) => [r.username, r.profile_name, r.device_name]),
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

/** Plural values for a job count: the number selects the form, the text is how it prints. */
export function jobCount(n: number): { count: number; count_text: string } {
  return { count: n, count_text: n.toLocaleString(formatLocale()) };
}

const ACTION_APPLIED: Record<AdminDownloadPreparationAction, MessageKey> = {
  pause: `${K}.summary.paused`,
  resume: `${K}.summary.resumed`,
  cancel: `${K}.summary.canceled`,
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
  if (applied > 0) parts.push(t(ACTION_APPLIED[action], jobCount(applied)));
  if (unchanged > 0) {
    parts.push(
      t(
        action === "pause" ? `${K}.summary.alreadyPaused` : `${K}.summary.notPaused`,
        jobCount(unchanged),
      ),
    );
  }
  if (notApplicable > 0) parts.push(t(`${K}.summary.skippedFailed`, jobCount(notApplicable)));
  if (notFound > 0) parts.push(t(`${K}.summary.alreadyFinished`, jobCount(notFound)));
  return parts.join(" ") || t(`${K}.summary.nothingChanged`);
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
    (sum, job) => sum + job.requesters.filter((r) => r.status === "preparing").length,
    0,
  );
  const encoding = targets.some((job) => job.state === "running");
  const description = [
    encoding ? t(`${K}.cancelPrompt.encodingLost`) : "",
    waiting > 0
      ? t(`${K}.cancelPrompt.waitingFail`, {
          count: waiting,
          count_text: waiting.toLocaleString(formatLocale()),
        })
      : t(`${K}.cancelPrompt.noneWaiting`, { count: targets.length }),
  ]
    .filter(Boolean)
    .join(" ");

  if (targets.length > 1) {
    return {
      title: t(`${K}.cancelPrompt.titleMany`, jobCount(targets.length)),
      description,
      confirmLabel: t(`${K}.cancelPrompt.confirmMany`, jobCount(targets.length)),
    };
  }
  const name = targets[0] ? preparationCompactTitle(targets[0]) : "";
  if (targets[0]?.state === "failed") {
    return {
      title: t(`${K}.cancelPrompt.removeFailedTitle`, { name }),
      description,
      confirmLabel: t(`${K}.cancelPrompt.removeConfirm`),
    };
  }
  return {
    title: t(`${K}.cancelPrompt.cancelOneTitle`, { name }),
    description,
    confirmLabel: t(`${K}.cancelPrompt.cancelOneConfirm`),
  };
}
