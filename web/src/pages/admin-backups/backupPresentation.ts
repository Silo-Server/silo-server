import type {
  AdminBackup,
  AdminBackupActive,
  AdminBackupKind,
  AdminBackupLastRun,
  AdminBackupStatus,
  AdminBackupWarning,
} from "@/api/v2/adminBackups";
import { V2ProblemError } from "@/api/v2/request";
import { formatDateTime, formatTime, preferredDateLocale } from "@/lib/datetime";

export const BACKUP_ENABLED_KEY = "backup.enabled";
export const BACKUP_TIME_KEY = "backup.time";
export const BACKUP_KEEP_KEY = "backup.keep";
export const BACKUP_INTRO_KEY = "backup.include_intro_fingerprints";
export const BACKUP_SUBTITLES_KEY = "backup.include_subtitles";
export const BACKUP_SCHEDULE_KEYS = [BACKUP_ENABLED_KEY, BACKUP_TIME_KEY, BACKUP_KEEP_KEY];
export const BACKUP_CONTENT_KEYS = [BACKUP_INTRO_KEY, BACKUP_SUBTITLES_KEY];
export const BACKUP_SETTING_KEYS = [...BACKUP_SCHEDULE_KEYS, ...BACKUP_CONTENT_KEYS];

/** Where backups are kept on this server: its own disk, or nowhere yet. */
export type BackupLocation = "local" | "none";

/** Table rows shown before "Show all". */
export const BACKUP_ROWS_SHOWN = 4;

export function backupRowId(id: string) {
  return `backup-row-${id}`;
}

export const BACKUP_KIND_LABELS: Record<AdminBackupKind, string> = {
  nightly: "Nightly",
  manual: "Manual",
  "before-restore": "Taken before a restore",
};

export const BACKUP_STATUS_LABELS: Record<AdminBackupStatus, string> = {
  complete: "Complete",
  complete_with_warnings: "Complete with warnings",
  in_progress: "In progress",
  incomplete: "Incomplete",
};

/**
 * A Silo version worth printing. A development build reports
 * "unavailable", which says nothing to an admin.
 */
export function knownSiloVersion(version: string | null | undefined): string | null {
  const trimmed = version?.trim();
  return trimmed && trimmed.toLowerCase() !== "unavailable" ? trimmed : null;
}

/** The first block of a server id, for when the id is shown as a detail. */
export function shortServerId(serverId: string): string {
  return serverId.split("-")[0] || serverId;
}

/** A source server's name, or null when it has none (only its id). */
export function sourceServerName(source: { server_name: string }): string | null {
  return source.server_name.trim() || null;
}

/** Whether parts of a backup can be restored: it is finished and holds a restore set. */
export function canRestoreParts(backup: AdminBackup): boolean {
  return isFinished(backup) && backup.objects.some((object) => object.name === "restore-set.tar");
}

export function restorePath(backupId: string): string {
  return `/admin/backups/${encodeURIComponent(backupId)}/restore`;
}

/** Where a running restore shows its progress, from the operation holding the lock. */
export function activeRestorePath(active: AdminBackupActive): string | null {
  return active.backup_id ? restorePath(active.backup_id) : null;
}

/** A backup with a manifest: one that can be downloaded and restored. */
export function isFinished(backup: AdminBackup): boolean {
  return backup.status === "complete" || backup.status === "complete_with_warnings";
}

const DAY_MS = 86_400_000;

function startOfDay(date: Date): number {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
}

/** Whole local calendar days from `from` to `to`; negative when `to` is earlier. */
function calendarDays(from: Date, to: Date): number {
  // Rounding absorbs the hour a daylight-saving change adds or removes.
  return Math.round((startOfDay(to) - startOfDay(from)) / DAY_MS);
}

function weekday(date: Date): string {
  return date.toLocaleDateString(undefined, { weekday: "long" });
}

/** "Oct 30, 2026, 4:00 AM": a moment as the backup pages print it. */
export function backupDateTime(value: string): string {
  return formatDateTime(value, { dateStyle: "medium", seconds: false });
}

/** "Oct 31", in the preferred day and month order. */
function shortDate(date: Date): string {
  return date.toLocaleDateString(preferredDateLocale(), { month: "short", day: "numeric" });
}

/** A day inside a sentence: "today", "yesterday", "on Sunday", "on Oct 24". */
function dayPhrase(date: Date, now: Date): string {
  const days = calendarDays(now, date);
  if (days === 0) return "today";
  if (days === 1) return "tomorrow";
  if (days === -1) return "yesterday";
  if (days > -7 && days < 7) return `on ${weekday(date)}`;
  return `on ${shortDate(date)}`;
}

/** How a sentence names one saved copy: "Sunday's copy", "The Oct 24 copy". */
function copyName(date: Date, now: Date): string {
  const days = calendarDays(now, date);
  if (days === 0) return "Today's copy";
  if (days === -1) return "Yesterday's copy";
  if (days > -7) return `${weekday(date)}'s copy`;
  return `The ${shortDate(date)} copy`;
}

/**
 * When the next nightly backup starts, from the backup task's own schedule.
 * The server reads `backup.time` in its own time zone, which need not be the
 * browser's, so without that schedule (or once it has passed and not yet been
 * read again) the next run is unknown rather than guessed.
 */
export function nextBackupRun(now: Date, taskNextRunAt?: string): Date | null {
  if (!taskNextRunAt) return null;
  const scheduled = new Date(taskNextRunAt);
  return !Number.isNaN(scheduled.getTime()) && scheduled.getTime() > now.getTime()
    ? scheduled
    : null;
}

export interface RetentionPlan {
  /** Saved nightly and manual copies, oldest first. */
  kept: AdminBackup[];
  /** Ids of the copies any successful next backup deletes. */
  doomed: Set<string>;
  /**
   * The newest copy without warnings when it is past the limit: deleted only
   * if the next backup also completes without warnings, so it is not dimmed.
   */
  protectedId: string | null;
}

/**
 * What rotation does after the next successful backup, by the server's rules.
 * Nightly and manual copies with a manifest share `keep`, and the new copy
 * counts as one of them, so saved copies past the newest `keep - 1` go. The
 * newest copy without warnings is never rotated away: if the new copy has
 * warnings, the newest saved one without them stays. Which happens depends on
 * a run that has not happened yet, so that copy is reported apart, never as
 * deleted.
 */
export function retentionPlan(items: readonly AdminBackup[], keep: number): RetentionPlan {
  const newestFirst = items
    .filter((item) => (item.kind === "nightly" || item.kind === "manual") && isFinished(item))
    .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
  const room = Math.max(1, Math.floor(keep)) - 1;
  const newestClean = newestFirst.find((item) => item.status === "complete")?.id ?? null;
  const past = newestFirst.slice(room).map((item) => item.id);
  return {
    kept: [...newestFirst].reverse(),
    doomed: new Set(past.filter((id) => id !== newestClean)),
    protectedId: newestClean !== null && past.includes(newestClean) ? newestClean : null,
  };
}

function copies(count: number): string {
  return count === 1 ? "copy" : "copies";
}

/**
 * The line under Saved backups: how many copies Silo keeps and which saved
 * copies go next. `next` is when the next backup starts, or null when it is
 * unknown or nightly backups are off; `running` says one is being taken now.
 */
export function retentionNote({
  keep,
  plan,
  next,
  running,
  now,
}: {
  keep: number;
  plan: RetentionPlan;
  next: Date | null;
  running: boolean;
  now: Date;
}): string {
  const sentences = [`Silo keeps the ${keep === 1 ? "newest copy" : `${keep} newest copies`}.`];
  let when = "the next backup";
  if (running) when = "the backup running now";
  else if (next) {
    const days = calendarDays(now, next);
    if (days === 0) when = "today's backup";
    else if (days === 1) when = "tomorrow's backup";
  }
  const doomed = plan.kept.filter((item) => plan.doomed.has(item.id));
  const oldest = doomed[0];
  if (oldest) {
    const subject =
      doomed.length === 1
        ? `${copyName(new Date(oldest.created_at), now)} is`
        : `The ${doomed.length} oldest ${copies(doomed.length)} are`;
    sentences.push(`${subject} deleted once ${when} succeeds.`);
  }
  const kept = plan.kept.find((item) => item.id === plan.protectedId);
  if (kept) {
    sentences.push(
      `${copyName(new Date(kept.created_at), now)} is the newest without warnings, so it's deleted only if ${when} completes without warnings too.`,
    );
  }
  return sentences.join(" ");
}

/** The status line under the header: the last success and the next run. */
export function backupSchedulePhrases({
  lastSuccessAt,
  enabled,
  busy,
  next,
  now,
}: {
  lastSuccessAt?: string;
  enabled: boolean;
  busy: boolean;
  next: Date | null;
  now: Date;
}): { last: string; next: string | null } {
  const lastDate = lastSuccessAt ? new Date(lastSuccessAt) : null;
  const last =
    lastDate && !Number.isNaN(lastDate.getTime())
      ? `Last backup finished ${dayPhrase(lastDate, now)} at ${formatTime(lastDate)}.`
      : "No backup has finished yet.";
  let upcoming: string | null;
  if (busy) upcoming = null;
  else if (!enabled) upcoming = "Nightly backups are off.";
  else if (next)
    upcoming = `${lastDate ? "Next one" : "The first one"} starts ${dayPhrase(next, now)} at ${formatTime(next)}.`;
  else upcoming = "The next start time is unknown.";
  return { last, next: upcoming };
}

/**
 * A warning line for the latest run when it failed or was interrupted, or
 * null when it finished.
 */
export function lastRunProblem(lastRun: AdminBackupLastRun | undefined, now: Date): string | null {
  if (!lastRun || (lastRun.status !== "failed" && lastRun.status !== "interrupted")) return null;
  const started = new Date(lastRun.started_at);
  const kind = lastRun.kind === "manual" ? "manual backup" : "backup";
  const outcome =
    lastRun.status === "failed" ? "failed" : "was interrupted when its server stopped";
  const what = `The ${kind} started ${dayPhrase(started, now)} at ${formatTime(started)} ${outcome}`;
  const error = lastRun.error?.trim();
  return error ? `${what}: ${error.replace(/\.?$/, ".")}` : `${what}.`;
}

/**
 * Warnings for the status line. "Backups are off" is already the status
 * line's own second half, so it is not repeated as a warning. "No recent
 * backup" says nothing while one is being taken, or before any backup has
 * run at all, where the empty state already says so.
 */
export function visibleWarnings(
  warnings: readonly AdminBackupWarning[],
  {
    backingUp = false,
    neverBackedUp = false,
  }: { backingUp?: boolean; neverBackedUp?: boolean } = {},
): AdminBackupWarning[] {
  return warnings.filter(
    (warning) =>
      warning.code !== "disabled" &&
      !((backingUp || neverBackedUp) && warning.code === "no_recent_backup"),
  );
}

/** Why a backup or a delete can't start because the lock is held, or null when it is free. */
export function lockBlocker(busy: boolean, active: AdminBackupActive | undefined): string | null {
  if (!busy && !active) return null;
  if (active?.operation === "backup") return "A backup is running. Wait for it to finish.";
  if (active?.operation === "delete") return "A backup is being deleted. Wait for it to finish.";
  if (active?.operation === "restore") return "A restore is running. Wait for it to finish.";
  return "Another backup task is running. Wait for it to finish.";
}

/** Why "Back up now" is unavailable, or null when it can run. */
export function backUpNowBlocker({
  configured,
  lock,
  warnings,
  destinationAvailable,
}: {
  configured: boolean;
  /** From {@link lockBlocker}. */
  lock: string | null;
  warnings: readonly AdminBackupWarning[];
  destinationAvailable: boolean;
}): string | null {
  if (!configured) return "This server has no storage for backups.";
  const sqlite = warnings.find((warning) => warning.code === "sqlite_user_store");
  if (sqlite) return sqlite.message;
  if (lock) return lock;
  if (!destinationAvailable) return "The backup destination can't be reached.";
  return null;
}

/**
 * What to tell the admin when a backup action fails. The server's own detail
 * says what happened; this adds what to do about it. A plain conflict means
 * another backup or restore holds the lock, except on the SQLite user store,
 * whose conflict detail already says what to do.
 */
export function backupErrorMessage(error: unknown, { sqliteUserStore = false } = {}): string {
  if (!(error instanceof V2ProblemError)) {
    return "The server didn't answer. Check that Silo is running, then try again.";
  }
  if (error.status === 404) return "That backup no longer exists. The list is refreshing.";
  if (error.status === 409 && error.problemType === "conflict" && !sqliteUserStore) {
    return `${error.message} Try again when it finishes.`;
  }
  return error.message;
}

/** Quotes a path for a POSIX shell when it holds anything beyond plain path characters. */
function shellPath(path: string): string {
  return /^[\w./:@+-]+$/.test(path) ? path : `'${path.replaceAll("'", `'\\''`)}'`;
}

/**
 * The full-restore command for a backup at `location`, its folder on this
 * server. Without a backup it names the folder the downloaded files go into.
 */
export function restoreCommand(location: string | null | undefined): string {
  if (!location) return "silo backup restore --from <folder with the downloaded files>";
  return `silo backup restore --from ${shellPath(location)}`;
}
