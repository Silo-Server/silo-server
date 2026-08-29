import { useRef } from "react";
import { useImageLoaded } from "@/hooks/useImageLoaded";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import MediaItemMenu from "@/components/MediaItemMenu";
import CardOverlays from "@/components/overlays/CardOverlays";
import { decodeThumbhash } from "@/lib/thumbhash";
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
  const { loaded, onLoad } = useImageLoaded(item.poster_url);
  const thumbhashUrl = item.poster_thumbhash ? decodeThumbhash(item.poster_thumbhash) : "";
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
        <ViewTransitionLink to={itemHref} className="block overflow-hidden rounded-xl">
          <div
            className={`media-card-image relative ${
              item.type === "audiobook" ? "aspect-square" : "aspect-[2/3]"
            }`}
            style={
              thumbhashUrl
                ? {
                    backgroundImage: `url(${thumbhashUrl})`,
                    backgroundSize: "cover",
                    backgroundPosition: "center",
                  }
                : undefined
            }
          >
            {item.poster_url ? (
              <img
                src={item.poster_url}
                alt={item.title}
                className={`h-full w-full object-cover transition-opacity duration-300 ${loaded ? "opacity-100" : "opacity-0"}`}
                loading="lazy"
                onLoad={onLoad}
              />
            ) : (
              <div className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-1 p-3 text-center text-sm">
                <span className="line-clamp-3 font-medium">{item.title || "No Poster"}</span>
              </div>
            )}
            <div className="pointer-events-none absolute inset-x-0 bottom-0 h-24 bg-gradient-to-t from-black/55 to-transparent opacity-90" />
            {item.status === "ambiguous" && (
              <span className="absolute top-2.5 left-2.5 rounded-full border border-amber-500/25 bg-black/40 px-2 py-0.5 text-[10px] leading-none font-semibold tracking-wide text-amber-200 uppercase backdrop-blur-sm">
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
                    className={`rounded-full border px-2 py-0.5 text-[10px] leading-none font-semibold tracking-wide uppercase backdrop-blur-sm ${upcomingBadgeClass(
                      badge,
                    )}`}
                  >
                    {upcomingBadgeLabel(badge)}
                  </span>
                ))}
              </div>
            )}
          </div>
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
        <div className="px-1 pt-3">
          <ViewTransitionLink
            to={headingHref}
            className="block truncate text-[14px] font-semibold tracking-tight hover:underline"
          >
            {displayTitle}
          </ViewTransitionLink>
          {showMetadata && upcomingEvent ? (
            <ViewTransitionLink to={itemHref} className="block hover:underline">
              {subtitle && (
                <div className="text-muted-foreground mt-1 truncate text-[11px] font-medium tracking-[0.14em] uppercase">
                  {subtitle}
                </div>
              )}
              <div className="mt-1.5 flex min-w-0 items-center gap-1.5 text-[11px] font-medium">
                <span className="text-foreground shrink-0">{airDateLabel}</span>
                {airTimeLabel && (
                  <span className="text-muted-foreground min-w-0 truncate">{airTimeLabel}</span>
                )}
              </div>
            </ViewTransitionLink>
          ) : showMetadata && episodeLabels ? (
            <ViewTransitionLink to={itemHref} className="block hover:underline">
              {episodeLabels.episodeTitle ? (
                <div className="text-muted-foreground mt-1 truncate text-[12px] font-medium">
                  {episodeLabels.episodeTitle}
                </div>
              ) : null}
              <div className="text-muted-foreground mt-1 text-[11px] font-medium tracking-[0.14em] uppercase">
                {episodeLabels.episodeCode}
              </div>
            </ViewTransitionLink>
          ) : showMetadata && item.item_source === "next_in_series" && item.series_title ? (
            <ViewTransitionLink
              to={itemHref}
              className="text-muted-foreground mt-1 block truncate text-[11px] font-medium tracking-[0.14em] uppercase hover:underline"
            >
              {[item.badges?.find((badge) => badge.startsWith("Book ")), item.series_title]
                .filter(Boolean)
                .join(" · ")}
            </ViewTransitionLink>
          ) : showMetadata ? (
            <ViewTransitionLink
              to={itemHref}
              className="text-muted-foreground mt-1 block truncate text-[11px] font-medium tracking-[0.14em] uppercase hover:underline"
            >
              {item.year ? `${item.year}` : ""} {item.type === "series" ? "Series" : ""}
            </ViewTransitionLink>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
