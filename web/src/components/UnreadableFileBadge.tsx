import { TriangleAlert } from "lucide-react";
import type { EpisodeListItem } from "@/api/types";

/**
 * Reports whether every file of an episode is one the server could not read
 * (empty, corrupt, or truncated). An episode with at least one readable
 * version still plays, so it is not marked.
 */
export function episodeFilesUnreadable(episode: Pick<EpisodeListItem, "files">): boolean {
  const files = episode.files ?? [];
  return files.length > 0 && files.every((file) => file.unreadable === true);
}

/** Inline marker for an episode whose files can't be played. */
export default function UnreadableFileBadge({ className = "" }: { className?: string }) {
  return (
    <span
      className={`text-destructive inline-flex items-center gap-1 font-medium ${className}`}
      title="Silo couldn't read this file. It appears to be empty or damaged."
    >
      <TriangleAlert className="size-3 shrink-0" aria-hidden="true" />
      Damaged file
    </span>
  );
}
