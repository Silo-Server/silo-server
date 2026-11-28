import { Layers } from "lucide-react";
import type { EpisodeListItem } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { useT } from "@/i18n";
import { episodeRangeCount } from "@/lib/episodeRange";
import { cn } from "@/lib/utils";

/**
 * Marks a row whose video holds several episodes (a multi-episode file such
 * as S01E15-E16): "2 episodes". Renders nothing for a single episode.
 */
export default function EpisodeRangeBadge({
  episode,
  className = "",
}: {
  episode: Pick<EpisodeListItem, "spanned_episodes">;
  className?: string;
}) {
  const t = useT();
  const count = episodeRangeCount(episode);
  if (count < 2) {
    return null;
  }
  return (
    <Badge
      variant="outline"
      className={cn("bg-muted/60 px-1.5 py-px text-[0.6875rem] font-semibold", className)}
    >
      <Layers aria-hidden="true" />
      {t("web.detail.episodes.rangeBadge", { count, count_text: count })}
    </Badge>
  );
}
