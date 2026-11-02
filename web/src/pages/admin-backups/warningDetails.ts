import type { AdminBackup } from "@/api/v2/adminBackups";

type MissingBlob = AdminBackup["missing_blobs"][number];

function plural(count: number, one: string, many = `${one}s`): string {
  return `${count} ${count === 1 ? one : many}`;
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

/** "101 files were missing when the backup copied them." */
export function missingHeadline(count: number): string {
  return count === 1
    ? "1 file was missing when the backup copied it."
    : `${count} files were missing when the backup copied them.`;
}

/** Every group as plain text, for pasting into an issue. */
export function warningDetailsReport({
  backupId,
  missing,
}: {
  backupId: string;
  missing: readonly MissingBlob[];
}): string {
  const lines = [`Backup ${backupId}`];
  if (missing.length > 0) {
    lines.push("", missingHeadline(missing.length));
    for (const group of groupMissingBlobs(missing)) {
      lines.push("", missingGroupTitle(group), ...group.keys.map((key) => `  ${key}`));
    }
  }
  return lines.join("\n");
}
