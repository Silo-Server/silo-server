import { useState } from "react";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import MediaItemMenu from "@/components/MediaItemMenu";
import CardOverlays from "@/components/overlays/CardOverlays";
import { decodeThumbhash } from "@/lib/thumbhash";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { overlayDataFromSectionItem } from "@/lib/overlays";
import { buildEpisodeCardLabels } from "@/lib/episodeCardLabels";
import {
  formatUpcomingDate,
  formatUpcomingSubtitle,
  formatUpcomingTime,
  upcomingBadgeClass,
  upcomingBadgeLabel,
} from "@/lib/upcomingEventPresentation";
import type { SectionItem } from "@/api/types";

interface SectionItemCardProps {
  item: SectionItem;
  libraryId?: number;
}

export default function SectionItemCard({ item, libraryId }: SectionItemCardProps) {
  const [loaded, setLoaded] = useState(false);
  const thumbhashUrl = item.poster_thumbhash ? decodeThumbhash(item.poster_thumbhash) : "";
  const itemHref = `/item/${item.content_id}${libraryId ? `?libraryId=${libraryId}` : ""}`;
  const { prefs: overlayPrefs } = useOverlayPrefs();
  const upcomingEvent = item.upcoming_event;
  const subtitle = upcomingEvent ? formatUpcomingSubtitle(upcomingEvent) : "";
  const airDateLabel = upcomingEvent ? formatUpcomingDate(upcomingEvent.air_date) : "";
  const airTimeLabel = upcomingEvent ? formatUpcomingTime(upcomingEvent.air_time) : null;
  const episodeLabels = !upcomingEvent ? buildEpisodeCardLabels(item) : null;

  return (
    <div className="media-card group/card">
      <div className="relative">
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
                onLoad={() => setLoaded(true)}
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
        <MediaItemMenu
          contentId={item.content_id}
          mediaType={item.type}
          libraryId={libraryId}
          userState={item.user_state}
          variant="poster"
        />
      </div>
      <ViewTransitionLink to={itemHref} className="block px-1 pt-3">
        <div className="truncate text-[14px] font-semibold tracking-tight">
          {episodeLabels ? episodeLabels.seriesTitle : item.title}
        </div>
        {upcomingEvent ? (
          <>
            {subtitle && (
              <div className="text-muted-foreground mt-1 truncate text-[11px] font-medium tracking-[0.14em] uppercase">
                {subtitle}
              </div>
            )}
            <div className="mt-1.5 flex items-center gap-1.5 text-[11px] font-medium">
              <span className="text-foreground">{airDateLabel}</span>
              {airTimeLabel && <span className="text-muted-foreground">{airTimeLabel}</span>}
            </div>
          </>
        ) : episodeLabels ? (
          <>
            {episodeLabels.episodeTitle ? (
              <div className="text-muted-foreground mt-1 truncate text-[12px] font-medium">
                {episodeLabels.episodeTitle}
              </div>
            ) : null}
            <div className="text-muted-foreground mt-1 text-[11px] font-medium tracking-[0.14em] uppercase">
              {episodeLabels.episodeCode}
            </div>
          </>
        ) : (
          <div className="text-muted-foreground mt-1 text-[11px] font-medium tracking-[0.14em] uppercase">
            {item.year ? `${item.year}` : ""} {item.type === "series" ? "Series" : ""}
          </div>
        )}
      </ViewTransitionLink>
    </div>
  );
}
