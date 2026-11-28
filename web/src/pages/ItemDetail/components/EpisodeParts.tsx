import { useState } from "react";
import type { EpisodeListItem } from "@/api/types";
import { codeNumber, useT } from "@/i18n";
import { cn } from "@/lib/utils";
import DetailOverview from "./DetailOverview";

/**
 * The description of a multi-episode video whose episodes are described
 * differently: a tab per episode above one description, so the page keeps its
 * height however many episodes the video holds. Wide screens name the episode
 * in its tab; narrow ones show the code alone and the title above the text.
 */
export default function EpisodeParts({
  episode,
  hideOverviews,
  onRevealOverview,
  clamp,
  translating,
  onTranslate,
  machineTranslated,
}: {
  episode: EpisodeListItem;
  hideOverviews: boolean;
  onRevealOverview?: () => void;
  clamp?: boolean;
  /** The first episode's translation state, as the plain description shows it. */
  translating?: boolean;
  onTranslate?: () => void;
  machineTranslated?: boolean;
}) {
  const t = useT();
  const first = {
    content_id: episode.content_id,
    episode_number: episode.episode_number,
    title: episode.title,
    overview: episode.overview,
  };
  const parts = [first, ...(episode.spanned_episodes ?? [])];
  const [selectedId, setSelectedId] = useState(first.content_id);
  const selected = parts.find((part) => part.content_id === selectedId) ?? first;
  const titleOf = (part: (typeof parts)[number]) =>
    part.title?.trim() || t("web.detail.episodes.numbered", { episode: part.episode_number });
  const spokenTitleOf = (part: (typeof parts)[number]) => {
    const title = titleOf(part);
    return episode.season_number === 0
      ? t("web.detail.episodes.spokenSpecialTitled", { episode: part.episode_number, title })
      : t("web.detail.episodes.spokenTitled", {
          season: episode.season_number,
          episode: part.episode_number,
          title,
        });
  };
  const panelId = `episode-part-${selected.content_id}`;
  const isFirst = selected === first;

  return (
    <div className="mt-3 mb-1 max-w-2xl">
      <div
        role="tablist"
        aria-label={t("web.detail.episodes.partsHeading")}
        className="mb-3 flex gap-2 overflow-x-auto"
      >
        {parts.map((part) => {
          const active = part.content_id === selected.content_id;
          return (
            <button
              key={part.content_id}
              type="button"
              role="tab"
              aria-selected={active}
              aria-controls={panelId}
              aria-label={spokenTitleOf(part)}
              onClick={() => setSelectedId(part.content_id)}
              className={cn(
                "max-w-64 shrink-0 truncate rounded-full border px-3 py-1 text-xs font-semibold transition-colors",
                active
                  ? "bg-foreground text-background border-transparent"
                  : "border-border text-muted-foreground hover:text-foreground",
              )}
            >
              {t("web.detail.episodes.partCode", { episode: codeNumber(part.episode_number, 2) })}
              <span className="hidden sm:inline">
                <span className="mx-1">{t("web.detail.separator.dot")}</span>
                {titleOf(part)}
              </span>
            </button>
          );
        })}
      </div>
      <div id={panelId} role="tabpanel">
        <p dir="auto" className="bidi-text text-foreground mb-1 text-sm font-semibold sm:hidden">
          {titleOf(selected)}
        </p>
        {selected.overview ? (
          <DetailOverview
            key={selected.content_id}
            overview={selected.overview}
            clamp={clamp}
            hidden={hideOverviews}
            onReveal={onRevealOverview}
            translating={isFirst && translating}
            onTranslate={isFirst ? onTranslate : undefined}
            machineTranslated={isFirst && machineTranslated}
          />
        ) : null}
      </div>
    </div>
  );
}
