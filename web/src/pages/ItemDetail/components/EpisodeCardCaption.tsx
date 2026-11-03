import type { ReactNode } from "react";
import type { EpisodeListItem } from "@/api/types";
import MachineTranslatedLabel from "./MachineTranslatedLabel";
import { hasMachineTranslation } from "./machineTranslation";
import { episodeCardMetadata, episodeCardSpokenTitle, episodeCardTitle } from "./episodeCardText";

interface EpisodeCardCaptionProps {
  episode: EpisodeListItem;
  /** Spoiler protection leaves the description out; the title and metadata stay. */
  hideOverview: boolean;
  isCurrent?: boolean;
  /** Icons at the end of the title line, such as the watched check. */
  badges?: ReactNode;
  /** A short notice on the date line, such as a damaged-file warning. */
  notice?: ReactNode;
}

/**
 * The caption under an episode card's still, the same on the Apple and Android
 * phone apps: "S01E02 · Title", then air date and runtime, then up to three
 * lines of description.
 */
export default function EpisodeCardCaption({
  episode,
  hideOverview,
  isCurrent = false,
  badges,
  notice,
}: EpisodeCardCaptionProps) {
  const metadata = episodeCardMetadata(episode);
  const overview = hideOverview ? "" : (episode.overview?.trim() ?? "");
  const showsTranslationLabel =
    overview !== "" && hasMachineTranslation(episode.machine_translated_fields);

  return (
    <div className="mt-2 space-y-1">
      <div className="flex items-center gap-2">
        <p
          className="text-foreground min-w-0 flex-1 truncate text-sm font-semibold"
          style={isCurrent ? { color: "var(--primary)" } : undefined}
        >
          <span aria-hidden="true">{episodeCardTitle(episode)}</span>
          <span className="sr-only">{episodeCardSpokenTitle(episode)}</span>
        </p>
        {badges && <span className="flex shrink-0 items-center gap-2">{badges}</span>}
      </div>
      {(metadata || notice || showsTranslationLabel) && (
        <div className="text-muted-foreground flex items-center gap-2 text-xs">
          {metadata && <span className="min-w-0 truncate">{metadata}</span>}
          {notice && <span className="shrink-0">{notice}</span>}
          {showsTranslationLabel && <MachineTranslatedLabel compact />}
        </div>
      )}
      {overview && (
        // Three lines are reserved, as on the Apple card, so short descriptions
        // keep the row's cards the same height.
        <p className="text-muted-foreground line-clamp-3 min-h-[3lh] text-xs leading-relaxed">
          {overview}
        </p>
      )}
    </div>
  );
}
