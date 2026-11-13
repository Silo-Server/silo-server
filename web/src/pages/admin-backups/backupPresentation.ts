import type {
  AdminBackup,
  AdminBackupActive,
  AdminBackupKind,
  AdminBackupLastRun,
  AdminBackupList,
  AdminBackupStatus,
  AdminBackupWarning,
} from "@/api/v2/adminBackups";
import { V2ProblemError } from "@/api/v2/request";
import { formatDateTime, formatTime, preferredDateLocale } from "@/lib/datetime";
import { formatLocale } from "@/lib/appLocale";
import { t, type MessageKey, type Translate } from "@/i18n";

export const BACKUP_ENABLED_KEY = "backup.enabled";
export const BACKUP_TIME_KEY = "backup.time";
export const BACKUP_KEEP_KEY = "backup.keep";
export const BACKUP_INTRO_KEY = "backup.include_intro_fingerprints";
export const BACKUP_SUBTITLES_KEY = "backup.include_subtitles";
export const BACKUP_SCHEDULE_KEYS = [BACKUP_ENABLED_KEY, BACKUP_TIME_KEY, BACKUP_KEEP_KEY];
export const BACKUP_CONTENT_KEYS = [BACKUP_INTRO_KEY, BACKUP_SUBTITLES_KEY];
/** "default" (the server's own storage) or "s3" (a bucket used only for backups). */
export const BACKUP_DESTINATION_KEY = "backup.destination";
export const BACKUP_SETTING_KEYS = [
  ...BACKUP_SCHEDULE_KEYS,
  ...BACKUP_CONTENT_KEYS,
  BACKUP_DESTINATION_KEY,
];

/** Where the default destination lives on this server. */
export type BackupLocation = "local" | "bucket" | "none";

/**
 * Where the default destination (the server's own storage) lives. The list
 * shows only the current destination's backups, so their locations say so
 * only while default is current; otherwise the private bucket setting does.
 */
export function defaultBackupLocation({
  hasDefault,
  destination,
  items,
  privateBucket,
}: {
  /** The server has its own storage: capabilities list "default". */
  hasDefault: boolean;
  destination: AdminBackupList["destination"] | undefined;
  items: readonly AdminBackup[];
  /** The persisted s3.private_bucket setting. */
  privateBucket: string;
}): BackupLocation {
  if (!hasDefault) return "none";
  if (destination !== "s3" && items.length > 0)
    return items.some((item) => isBucketLocation(item.location)) ? "bucket" : "local";
  return privateBucket.trim() ? "bucket" : "local";
}

/**
 * Whether the page asks the admin to confirm this server's SECRET_KEY.
 * Encrypted backups (any bucket: the S3 destination, or the server's own
 * storage when that is a private bucket) pause until a copy of the current
 * key is confirmed.
 */
export function keyConfirmationNeeded(list: AdminBackupList | undefined): boolean {
  return Boolean(list?.encrypted && !list.key_confirmed);
}

/** An encrypted backup taken with another SECRET_KEY: this server can't decrypt it. */
export function encryptedWithOtherKey(backup: AdminBackup): boolean {
  return backup.encrypted && !backup.key_matches;
}

/** How the table marks a backup this server can't decrypt. */
export function otherKeyNote(backup: AdminBackup): string {
  return backup.key_check
    ? t("web.admin.backups.table.otherKeyNoteWithCheck", { key_check: backup.key_check })
    : t("web.admin.backups.table.otherKeyNote");
}

/** Why restoring or a decrypted download is unavailable for such a backup. */
export function otherKeyReason(): string {
  return t("web.admin.backups.table.otherKeyReason");
}

/** Table rows shown before "Show all". */
export const BACKUP_ROWS_SHOWN = 4;

export function backupRowId(id: string) {
  return `backup-row-${id}`;
}

export const BACKUP_KIND_LABELS: Record<AdminBackupKind, MessageKey> = {
  nightly: "web.admin.backups.kind.nightly",
  manual: "web.admin.backups.kind.manual",
  "before-restore": "web.admin.backups.kind.beforeRestore",
  "before-upgrade": "web.admin.backups.kind.beforeUpgrade",
};

export const BACKUP_STATUS_LABELS: Record<AdminBackupStatus, MessageKey> = {
  complete: "web.admin.backups.status.complete",
  complete_with_warnings: "web.admin.backups.status.completeWithWarnings",
  in_progress: "web.admin.backups.status.inProgress",
  incomplete: "web.admin.backups.status.incomplete",
  unverified: "web.admin.backups.status.unverified",
  unreadable: "web.admin.backups.status.unreadable",
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

/**
 * Whether parts of a backup can be restored: it is finished and holds a
 * restore set, stored plain or encrypted. An encrypted one also needs this
 * server's key; see {@link encryptedWithOtherKey}.
 */
export function canRestoreParts(backup: AdminBackup): boolean {
  return (
    isFinished(backup) &&
    backup.objects.some(
      (object) => object.name === "restore-set.tar" || object.name === "restore-set.tar.age",
    )
  );
}

export function restorePath(backupId: string): string {
  return `/admin/backups/${encodeURIComponent(backupId)}/restore`;
}

export function uploadRestorePath(uploadId: string): string {
  return `/admin/backups/uploads/${encodeURIComponent(uploadId)}/restore`;
}

/** Where a running restore shows its progress, from the operation holding the lock. */
export function activeRestorePath(active: AdminBackupActive): string | null {
  if (active.upload_id) return uploadRestorePath(active.upload_id);
  return active.backup_id ? restorePath(active.backup_id) : null;
}

/**
 * A backup with a manifest: one that can be downloaded and restored. An
 * unverified one with its objects listed was written under another
 * SECRET_KEY, and restoring it asks for confirmation; one without was
 * changed after it was written and is never offered.
 */
export function isFinished(backup: AdminBackup): boolean {
  return (
    backup.status === "complete" ||
    backup.status === "complete_with_warnings" ||
    (backup.status === "unverified" && backup.objects.length > 0)
  );
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
  return date.toLocaleDateString(formatLocale(), { weekday: "long" });
}

/** "Oct 30, 2026, 4:00 AM": a moment as the backup pages print it. */
export function backupDateTime(value: string): string {
  return formatDateTime(value, { dateStyle: "medium", seconds: false });
}

/** "Oct 31", in the preferred day and month order. */
function shortDate(date: Date): string {
  return date.toLocaleDateString(preferredDateLocale(), { month: "short", day: "numeric" });
}

/**
 * A day inside a sentence ("today", "yesterday", "on Sunday", "on Oct 24"),
 * as the `day` select and `date` values the messages take.
 */
function dayValues(date: Date, now: Date): { day: string; date: string } {
  const days = calendarDays(now, date);
  if (days === 0) return { day: "today", date: "" };
  if (days === 1) return { day: "tomorrow", date: "" };
  if (days === -1) return { day: "yesterday", date: "" };
  if (days > -7 && days < 7) return { day: "other", date: weekday(date) };
  return { day: "other", date: shortDate(date) };
}

/**
 * How a sentence names one saved copy ("Sunday's copy", "The Oct 24 copy"),
 * as the `copy` select, the `date`, and the `<day>` tag the messages take.
 */
function copyValues(date: Date, now: Date) {
  const days = calendarDays(now, date);
  const day = weekday(date);
  if (days === 0) return { copy: "today", date: "", day };
  if (days === -1) return { copy: "yesterday", date: "", day };
  if (days > -7) return { copy: "weekday", date: "", day };
  return { copy: "other", date: shortDate(date), day };
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
  // The server decides what a clean copy is (complete, every file,
  // verified), as rotation does.
  const newestClean = newestFirst.find((item) => item.clean)?.id ?? null;
  const past = newestFirst.slice(room).map((item) => item.id);
  return {
    kept: [...newestFirst].reverse(),
    doomed: new Set(past.filter((id) => id !== newestClean)),
    protectedId: newestClean !== null && past.includes(newestClean) ? newestClean : null,
  };
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
  t,
}: {
  keep: number;
  plan: RetentionPlan;
  next: Date | null;
  running: boolean;
  now: Date;
  /** The component's formatter: the copy names use a rich-text tag for the weekday. */
  t: Translate;
}): string {
  const sentences = [t("web.admin.backups.retention.keeps", { keep, keep_text: keep })];
  let when = "other";
  if (running) when = "running";
  else if (next) {
    const days = calendarDays(now, next);
    if (days === 0) when = "today";
    else if (days === 1) when = "tomorrow";
  }
  const doomed = plan.kept.filter((item) => plan.doomed.has(item.id));
  const oldest = doomed[0];
  if (oldest) {
    sentences.push(
      doomed.length === 1
        ? t("web.admin.backups.retention.oneDeleted", {
            ...copyValues(new Date(oldest.created_at), now),
            when,
          })
        : t("web.admin.backups.retention.manyDeleted", {
            count: doomed.length,
            count_text: doomed.length,
            when,
          }),
    );
  }
  const kept = plan.kept.find((item) => item.id === plan.protectedId);
  if (kept) {
    sentences.push(
      t("web.admin.backups.retention.protected", {
        ...copyValues(new Date(kept.created_at), now),
        when,
      }),
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
      ? t("web.admin.backups.schedule.lastFinished", {
          ...dayValues(lastDate, now),
          time: formatTime(lastDate),
        })
      : t("web.admin.backups.schedule.noneYet");
  let upcoming: string | null;
  if (busy) upcoming = null;
  else if (!enabled) upcoming = t("web.admin.backups.schedule.off");
  else if (next)
    upcoming = t(
      lastDate ? "web.admin.backups.schedule.next" : "web.admin.backups.schedule.first",
      {
        ...dayValues(next, now),
        time: formatTime(next),
      },
    );
  else upcoming = t("web.admin.backups.schedule.unknown");
  return { last, next: upcoming };
}

/**
 * A warning line for the latest run when it failed or was interrupted, or
 * null when it finished.
 */
export function lastRunProblem(lastRun: AdminBackupLastRun | undefined, now: Date): string | null {
  if (!lastRun || (lastRun.status !== "failed" && lastRun.status !== "interrupted")) return null;
  const started = new Date(lastRun.started_at);
  const values = {
    kind: lastRun.kind === "manual" ? "manual" : "other",
    ...dayValues(started, now),
    time: formatTime(started),
  };
  const error = lastRun.error?.trim();
  const failed = lastRun.status === "failed";
  if (error) {
    return t(
      failed
        ? "web.admin.backups.lastRun.failedWithError"
        : "web.admin.backups.lastRun.interruptedWithError",
      { ...values, error: error.replace(/\.?$/, ".") },
    );
  }
  return t(
    failed ? "web.admin.backups.lastRun.failed" : "web.admin.backups.lastRun.interrupted",
    values,
  );
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

/** Warning codes that are information, not a problem: shown muted, not amber. */
const INFO_WARNINGS: ReadonlySet<AdminBackupWarning["code"]> = new Set([
  "no_backups_at_destination",
]);

export function warningTone(warning: AdminBackupWarning): "warn" | "muted" {
  return INFO_WARNINGS.has(warning.code) ? "muted" : "warn";
}

/**
 * A warning's text. The key pause gets the page's own wording: the API can't
 * say whether the key was never confirmed or changed since, so it names the
 * fix rather than guessing the cause.
 */
export function warningText(warning: AdminBackupWarning): string {
  return warning.code === "key_not_confirmed"
    ? t("web.admin.backups.warning.keyNotConfirmed")
    : warning.message;
}

/** Why a backup or a delete can't start because the lock is held, or null when it is free. */
export function lockBlocker(busy: boolean, active: AdminBackupActive | undefined): string | null {
  if (!busy && !active) return null;
  if (active?.operation === "backup") return t("web.admin.backups.lock.backup");
  if (active?.operation === "delete")
    return active.upload_id
      ? t("web.admin.backups.lock.deleteUpload")
      : t("web.admin.backups.lock.delete");
  if (active?.operation === "upload") return t("web.admin.backups.lock.upload");
  if (active?.operation === "restore") return t("web.admin.backups.lock.restore");
  return t("web.admin.backups.lock.anotherTask");
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
  if (!configured) return t("web.admin.backups.backUpNow.noStorage");
  const sqlite = warnings.find((warning) => warning.code === "sqlite_user_store");
  if (sqlite) return sqlite.message;
  // The status area explains the pause and offers the confirmation.
  if (warnings.some((warning) => warning.code === "key_not_confirmed")) {
    return t("web.admin.backups.backUpNow.keyNotConfirmed");
  }
  if (lock) return lock;
  if (!destinationAvailable) return t("web.admin.backups.backUpNow.unreachable");
  return null;
}

/**
 * What to tell the admin when a backup action fails. The server's own detail
 * says what happened and what to do about it.
 */
export function backupErrorMessage(error: unknown): string {
  if (!(error instanceof V2ProblemError)) {
    return t("web.admin.backups.error.noAnswer");
  }
  if (error.status === 404) return t("web.admin.backups.error.notFound");
  return error.message;
}

/** Whether a backup's location is in a bucket rather than on this server's disk. */
export function isBucketLocation(location: string): boolean {
  return /^s3:\/\//i.test(location);
}

/** Quotes a path for a POSIX shell when it holds anything beyond plain path characters. */
function shellPath(path: string): string {
  return /^[\w./:@+-]+$/.test(path) ? path : `'${path.replaceAll("'", `'\\''`)}'`;
}

/**
 * The full-restore command for a backup at `location`: its folder on this
 * server, or its s3:// address, which the command reads with the
 * SILO_BACKUP_S3_* variables. Without a backup it names the folder the
 * downloaded files go into.
 */
export function restoreCommand(location: string | null | undefined): string {
  if (!location) {
    return `silo backup restore --from <${t("web.admin.backups.restoreCommand.folderPlaceholder")}>`;
  }
  const from = isBucketLocation(location) ? location.replace(/\/+$/, "") : location;
  return `silo backup restore --from ${shellPath(from)}`;
}
