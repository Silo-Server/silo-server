import type { AdminBackup, AdminBackupOmitted } from "@/api/v2/adminBackups";
import { englishText, t, type MessageKey, type TextValues } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

type MissingBlob = AdminBackup["missing_blobs"][number];

/** Formats a message: `t` for the page, `englishText` for the report pasted into an issue. */
type Text = (id: MessageKey, values?: TextValues) => string;

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
function plainSummary(
  reason: AdminBackupOmitted["reason"],
  cause: string,
  count: number,
  text: Text = t,
): string {
  if (reason === "too_large") return text("web.admin.backups.warnings.tooLarge", { count });
  const reading = /(?:^|: )reading ([^:]+?)(?::|$)/.exec(cause);
  return reading?.[1]
    ? text("web.admin.backups.warnings.partUnreadable", { count, part: reading[1].trim() })
    : text("web.admin.backups.warnings.dataUnreadable", { count });
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
export function omissionGroupTitle(group: OmissionGroup, text: Text = t): string {
  const count = group.labels.length;
  return text("web.admin.backups.warnings.groupTitle", {
    count,
    count_text: count,
    summary: group.summary,
  });
}

/** Names shown before "and N more". */
export const NAMES_SHOWN = 5;

/** "Alice, Bob, Cara, and 177 more". */
export function nameSampleText(names: readonly string[], shown = NAMES_SHOWN): string {
  const list = names.slice(0, shown).join(", ");
  const more = names.length - shown;
  return more > 0 ? t("web.admin.backups.warnings.nameSample", { names: list, more: more }) : list;
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
export function missingGroupTitle(group: MissingFileGroup, text: Text = t): string {
  const count = group.keys.length;
  const values = { count, count_text: count, source: group.source };
  if (/artwork$/.test(group.source))
    return text("web.admin.backups.warnings.missingArtwork", values);
  return text("web.admin.backups.warnings.missingGroup", values);
}

/** "182 accounts weren't included in this backup's restore set." */
function omittedHeadline(count: number, text: Text = t): string {
  return text("web.admin.backups.warnings.omittedHeadline", { count, count_text: count });
}

/** "101 files were missing when the backup copied them." */
export function missingHeadline(listed: number, total: number = listed, text: Text = t): string {
  const values = {
    count: total,
    count_text: total === 1 ? "1" : total.toLocaleString(formatLocale()),
  };
  return total > listed && total !== 1
    ? text("web.admin.backups.warnings.missingHeadlineFirst", {
        ...values,
        listed: listed.toLocaleString(formatLocale()),
      })
    : text("web.admin.backups.warnings.missingHeadline", values);
}

/**
 * Every group as plain text, for pasting into an issue. It stays English in
 * every interface language: maintainers read it.
 */
export function warningDetailsReport({
  backupId,
  omitted,
  missing,
  missingTotal = missing.length,
}: {
  backupId: string;
  omitted: readonly AdminBackupOmitted[];
  missing: readonly MissingBlob[];
  /** Every missing file; `missing` lists at most the first 10,000. */
  missingTotal?: number;
}): string {
  const lines = [englishText("web.admin.backups.warnings.reportBackup", { id: backupId })];
  if (omitted.length > 0) {
    lines.push("", omittedHeadline(omitted.length, englishText));
    for (const group of groupOmissions(omitted)) {
      const summary = plainSummary(group.reason, group.cause, group.labels.length, englishText);
      lines.push(
        "",
        `${omissionGroupTitle({ ...group, summary }, englishText)} (${group.reason})`,
        `  ${group.detail}`,
        `  ${group.labels.join(", ")}`,
      );
    }
  }
  if (missing.length > 0) {
    lines.push("", missingHeadline(missing.length, missingTotal, englishText));
    for (const group of groupMissingBlobs(missing)) {
      lines.push("", missingGroupTitle(group, englishText), ...group.keys.map((key) => `  ${key}`));
    }
  }
  return lines.join("\n");
}
