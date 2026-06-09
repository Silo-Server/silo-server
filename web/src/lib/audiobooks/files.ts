import type { ItemDetail } from "@/api/types";
import type { AudiobookFile } from "@/lib/audiobooks/types";

/** Maps an item detail's file versions to playable audiobook files. */
export function audiobookFilesFromVersions(versions: ItemDetail["versions"]): AudiobookFile[] {
  return (versions ?? []).map((version) => ({
    id: version.file_id,
    path: version.file_path ?? version.file_name ?? "",
    duration_seconds: version.duration ?? 0,
    chapters: version.chapters ?? [],
  }));
}
