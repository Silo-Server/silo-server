import type { AdminBackup, AdminBackupOmitted } from "@/api/v2/adminBackups";

type MissingBlob = AdminBackup["missing_blobs"][number];

function plural(count: number, one: string, many = `${one}s`): string {
  return `${count} ${count === 1 ? one : many}`;
}

const UUID = /\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi;
const NUMERIC_ID = /\b(profile|account|user|item|library|collection)( id)? \d+\b/gi;
const BYTE_COUNT = /\b\d+ bytes\b/gi;
const LIMIT = /\(\d+\)/g;

/**
 * A cause without the ids and sizes that differ from one account to the
 * next, so the same failure on 182 accounts reads as one cause:
 * "profile 8bf4…be6 notifications: …" becomes "profile <id> notifications: …".
 */
function normalizeCause(message: string): string {
  return message
    .replace(UUID, "<id>")
    .replace(
      NUMERIC_ID,
      (_match, noun: string, id: string | undefined) => `${noun}${id ?? ""} <id>`,
    )
    .replace(BYTE_COUNT, "<n> bytes")
    .replace(LIMIT, "(<n>)")
    .trim();
}

/** The technical part of a cause: the database error when there is one. */
function technicalDetail(cause: string): string {
  const error = cause.indexOf("ERROR: ");
  return error >= 0 ? cause.slice(error + "ERROR: ".length) : cause;
}

/** What went wrong for a group of accounts, as a person would say it. */
function plainSummary(reason: AdminBackupOmitted["reason"], cause: string, count: number): string {
  const whose = count === 1 ? "its" : "their";
  if (reason === "too_large") return `${whose} data is too large for the restore set`;
  const reading = /(?:^|: )reading ([^:]+?)(?::|$)/.exec(cause);
  return reading?.[1]
    ? `${whose} ${reading[1].trim()} couldn't be read`
    : `${whose} data couldn't be read`;
}

export interface OmissionGroup {
  reason: AdminBackupOmitted["reason"];
  /** The normalized cause; one group per reason and cause. */
  cause: string;
  /** "their notification preferences couldn't be read". */
  summary: string;
  /** The normalized technical detail, such as the database error. */
  detail: string;
  labels: string[];
}

/** Omitted accounts grouped by reason and normalized cause, largest group first. */
export function groupOmissions(omitted: readonly AdminBackupOmitted[]): OmissionGroup[] {
  const groups = new Map<string, OmissionGroup>();
  for (const entry of omitted) {
    // Newer restore sets name the failing part (`cause`) and the error
    // without ids (`error`); older ones only have the message to parse.
    const cause = entry.cause?.trim() || normalizeCause(entry.message);
    const detail = entry.error?.trim() || technicalDetail(normalizeCause(entry.message));
    const key = `${entry.reason}\u0000${cause}\u0000${detail}`;
    const group = groups.get(key);
    if (group) group.labels.push(entry.label);
    else
      groups.set(key, { reason: entry.reason, cause, summary: "", detail, labels: [entry.label] });
  }
  return [...groups.values()]
    .map((group) => ({
      ...group,
      summary: plainSummary(group.reason, group.cause, group.labels.length),
    }))
    .sort((a, b) => b.labels.length - a.labels.length);
}

/** "182 accounts — their notification preferences couldn't be read". */
export function omissionGroupTitle(group: OmissionGroup): string {
  return `${plural(group.labels.length, "account")} — ${group.summary}`;
}

/** Names shown before "and N more". */
export const NAMES_SHOWN = 5;

/** "Alice, Bob, Cara, and 177 more". */
export function nameSampleText(names: readonly string[], shown = NAMES_SHOWN): string {
  const list = names.slice(0, shown).join(", ");
  const more = names.length - shown;
  return more > 0 ? `${list}, and ${more} more` : list;
}

export interface MissingFileGroup {
  source: string;
  keys: string[];
}

/** Missing files grouped by what referenced them, largest group first. */
export function groupMissingBlobs(blobs: readonly MissingBlob[]): MissingFileGroup[] {
  const groups = new Map<string, MissingFileGroup>();
  for (const blob of blobs) {
    const source = blob.source.trim() || "file";
    const group = groups.get(source);
    if (group) group.keys.push(blob.key);
    else groups.set(source, { source, keys: [blob.key] });
  }
  return [...groups.values()].sort((a, b) => b.keys.length - a.keys.length);
}

/** "98 downloaded subtitles", "2 uploaded artwork files". */
export function missingGroupTitle(group: MissingFileGroup): string {
  const count = group.keys.length;
  if (/artwork$/.test(group.source)) return plural(count, `${group.source} file`);
  return plural(count, group.source);
}

/** "182 accounts weren't included in this backup's restore set." */
export function omittedHeadline(count: number): string {
  return `${count === 1 ? "1 account wasn't" : `${count} accounts weren't`} included in this backup's restore set.`;
}

/** "101 files were missing when the backup copied them." */
export function missingHeadline(count: number): string {
  return count === 1
    ? "1 file was missing when the backup copied it."
    : `${count} files were missing when the backup copied them.`;
}

/** Every group as plain text, for pasting into an issue. */
export function warningDetailsReport({
  backupId,
  omitted,
  missing,
}: {
  backupId: string;
  omitted: readonly AdminBackupOmitted[];
  missing: readonly MissingBlob[];
}): string {
  const lines = [`Backup ${backupId}`];
  if (omitted.length > 0) {
    lines.push("", omittedHeadline(omitted.length));
    for (const group of groupOmissions(omitted)) {
      lines.push(
        "",
        `${omissionGroupTitle(group)} (${group.reason})`,
        `  ${group.detail}`,
        `  ${group.labels.join(", ")}`,
      );
    }
  }
  if (missing.length > 0) {
    lines.push("", missingHeadline(missing.length));
    for (const group of groupMissingBlobs(missing)) {
      lines.push("", missingGroupTitle(group), ...group.keys.map((key) => `  ${key}`));
    }
  }
  return lines.join("\n");
}
