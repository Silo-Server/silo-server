import { CalendarClock } from "lucide-react";
import type { EpisodeListItem } from "@/api/types";
import type { EpisodeSpoilerPrefs } from "@/hooks/useEpisodeSpoilerPrefs";
import { isEpisodeStill, SPOILER_IMAGE_CLASS } from "@/lib/episodeSpoilers";
import { cn } from "@/lib/utils";
import EpisodeCardCaption from "./EpisodeCardCaption";

interface MissingEpisodeCardProps {
  episode: EpisodeListItem;
  /** A request or a download server already covers the episode's season. */
  requested?: boolean;
  /** No one has watched an episode the library lacks, so the unwatched rules apply. */
  spoilerPrefs: EpisodeSpoilerPrefs;
}

/**
 * An episode the library lacks, in a season grid that asked to show them:
 * dimmed, not a link, labeled by whether it has aired or is already coming.
 * Requests cover whole seasons, so the request lives on the season.
 */
export default function MissingEpisodeCard({
  episode,
  requested = false,
  spoilerPrefs,
}: MissingEpisodeCardProps) {
  const hideImage = spoilerPrefs.hideImages && isEpisodeStill(episode.still_is_episode_still);
  const unaired = episode.availability === "unaired";
  const label = requested ? "Requested" : unaired ? "Not aired yet" : "Not in library";

  return (
    <div className="group/missing media-card" data-availability={episode.availability}>
      <div className="media-card-image relative aspect-video overflow-hidden">
        {episode.still_url ? (
          <img
            src={episode.still_url}
            alt=""
            decoding="async"
            loading="lazy"
            className={cn(
              "h-full w-full object-cover opacity-40 grayscale",
              hideImage && SPOILER_IMAGE_CLASS,
            )}
          />
        ) : (
          <div className="bg-surface flex h-full w-full items-center justify-center">
            <CalendarClock size={28} className="text-muted-foreground/40" />
          </div>
        )}
        <span className="absolute top-2 left-2 rounded-md bg-black/70 px-1.5 py-0.5 text-[0.6875rem] font-medium text-white">
          {label}
        </span>
      </div>
      <div className="opacity-60">
        <EpisodeCardCaption episode={episode} hideOverview={spoilerPrefs.hideOverviews} />
      </div>
    </div>
  );
}
