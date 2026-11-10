import { TriangleAlert } from "lucide-react";
import type { EpisodeListItem } from "@/api/types";
import { useT } from "@/i18n";

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
  const t = useT();
  return (
    <span
      className={`text-destructive inline-flex items-center gap-1 font-medium ${className}`}
      title={t("web.player.unreadableFile.title")}
    >
      <TriangleAlert className="size-3 shrink-0" aria-hidden="true" />
      {t("web.player.unreadableFile.label")}
    </span>
  );
}
