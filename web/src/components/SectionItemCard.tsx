import { useRef } from "react";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import MediaCardArtwork, {
  MEDIA_CARD_CAPTION_CLASS,
  MEDIA_CARD_META_CLASS,
  MEDIA_CARD_TITLE_CLASS,
} from "@/components/MediaCardArtwork";
import MediaItemMenu from "@/components/MediaItemMenu";
import CardOverlays from "@/components/overlays/CardOverlays";
import { overlayDataFromSectionItem, type CardOverlayPrefs } from "@/lib/overlays";
import type { CardQuickActionMode } from "@/lib/cardQuickActions";
import { buildEpisodeCardLabels } from "@/lib/episodeCardLabels";
import {
  formatUpcomingDate,
  formatUpcomingSubtitle,
  formatUpcomingTime,
  upcomingBadgeClass,
  upcomingBadgeLabel,
} from "@/lib/upcomingEventPresentation";
import type { SectionItem } from "@/api/types";
import { useUICustomization } from "@/hooks/useUICustomization";
import { buildItemHref } from "@/lib/mediaNavigation";
import CardPlayOverlay from "@/components/CardPlayOverlay";

interface SectionItemCardProps {
  item: SectionItem;
  libraryId?: number;
  overlayPrefs?: CardOverlayPrefs | null;
  quickActionMode?: CardQuickActionMode;
}

export default function SectionItemCard({
  item,
  libraryId,
  overlayPrefs = null,
  quickActionMode = "none",
}: SectionItemCardProps) {
  const itemHref = buildItemHref({ contentId: item.content_id, libraryId });
  const upcomingEvent = item.upcoming_event;
  const subtitle = upcomingEvent ? formatUpcomingSubtitle(upcomingEvent) : "";
  const airDateLabel = upcomingEvent ? formatUpcomingDate(upcomingEvent.air_date) : "";
  const airTimeLabel = upcomingEvent ? formatUpcomingTime(upcomingEvent.air_time) : null;
  const episodeLabels = !upcomingEvent ? buildEpisodeCardLabels(item) : null;
  const headingHref =
    item.type === "episode" && item.series_id
      ? buildItemHref({ contentId: item.series_id, libraryId })
      : itemHref;
  const { cardPresentation } = useUICustomization();
  const showCaption = cardPresentation.caption !== "artwork";
  const showMetadata = cardPresentation.caption === "title_metadata";
  const cardRef = useRef<HTMLDivElement>(null);
  const displayTitle = episodeLabels ? episodeLabels.seriesTitle : item.title;

  return (
    <div ref={cardRef} className="media-card media-card-longpress group/card">
      <div className="group/media relative">
        <ViewTransitionLink
          to={itemHref}
          aria-label={displayTitle}
          className="block overflow-hidden rounded-xl"
        >
          <MediaCardArtwork
            src={item.poster_url}
            alt={item.title}
            mediaType={item.type}
            thumbhash={item.poster_thumbhash}
            square={item.type === "audiobook"}
            lazy
          >
            {item.status === "ambiguous" && (
              <span className="absolute top-2.5 left-2.5 rounded-full border border-amber-500/25 bg-black/40 px-2 py-0.5 text-[0.625rem] leading-none font-semibold tracking-wide text-amber-200 uppercase backdrop-blur-sm">
                Ambiguous
              </span>
            )}
            {item.status === "matched" && overlayPrefs && (
              <CardOverlays data={overlayDataFromSectionItem(item)} prefs={overlayPrefs} />
            )}
            {upcomingEvent && upcomingEvent.badges.length > 0 && (
              <div className="absolute top-2.5 left-2.5 flex max-w-[calc(100%-2.5rem)] flex-wrap gap-1">
                {upcomingEvent.badges.map((badge) => (
                  <span
                    key={badge}
                    className={`rounded-full border px-2 py-0.5 text-[0.625rem] leading-none font-semibold tracking-wide uppercase backdrop-blur-sm ${upcomingBadgeClass(
                      badge,
                    )}`}
                  >
                    {upcomingBadgeLabel(badge)}
                  </span>
                ))}
              </div>
            )}
          </MediaCardArtwork>
        </ViewTransitionLink>
        {item.play_content_id ? (
          <CardPlayOverlay
            contentId={item.play_content_id}
            title={episodeLabels ? episodeLabels.seriesTitle : item.title}
            type={item.type === "movie" ? "movie" : "episode"}
            libraryId={libraryId}
          />
        ) : null}
        <MediaItemMenu
          contentId={item.content_id}
          mediaType={item.type}
          libraryId={libraryId}
          userState={item.user_state}
          variant="poster"
          quickActionMode={quickActionMode}
          longPressRef={cardRef}
          itemTitle={displayTitle}
        />
      </div>
      {showCaption ? (
        <div className={MEDIA_CARD_CAPTION_CLASS}>
          <ViewTransitionLink to={headingHref} className={MEDIA_CARD_TITLE_CLASS}>
            {displayTitle}
          </ViewTransitionLink>
          {showMetadata && upcomingEvent ? (
            <ViewTransitionLink to={itemHref} className="block hover:underline">
              {subtitle && (
                <div className="text-muted-foreground mt-1 truncate text-[0.6875rem] font-medium tracking-[0.14em] uppercase">
                  {subtitle}
                </div>
              )}
              <div className="mt-1.5 flex min-w-0 items-center gap-1.5 text-[0.6875rem] font-medium">
                <span className="text-foreground shrink-0">{airDateLabel}</span>
                {airTimeLabel && (
                  <span className="text-muted-foreground min-w-0 truncate">{airTimeLabel}</span>
                )}
              </div>
            </ViewTransitionLink>
          ) : showMetadata && episodeLabels ? (
            <ViewTransitionLink to={itemHref} className="block hover:underline">
              {episodeLabels.episodeTitle ? (
                <div className="text-muted-foreground mt-1 truncate text-[0.75rem] font-medium">
                  {episodeLabels.episodeTitle}
                </div>
              ) : null}
              <div className="text-muted-foreground mt-1 text-[0.6875rem] font-medium tracking-[0.14em] uppercase">
                {episodeLabels.episodeCode}
              </div>
            </ViewTransitionLink>
          ) : showMetadata && item.item_source === "next_in_series" && item.series_title ? (
            <ViewTransitionLink to={itemHref} className={MEDIA_CARD_META_CLASS}>
              {[item.badges?.find((badge) => badge.startsWith("Book ")), item.series_title]
                .filter(Boolean)
                .join(" · ")}
            </ViewTransitionLink>
          ) : showMetadata ? (
            <ViewTransitionLink to={itemHref} className={MEDIA_CARD_META_CLASS}>
              {item.year ? `${item.year}` : ""} {item.type === "series" ? "Series" : ""}
            </ViewTransitionLink>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
