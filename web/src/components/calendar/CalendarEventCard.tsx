import { useState } from "react";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { decodeThumbhash } from "@/lib/thumbhash";
import {
  formatUpcomingSubtitle,
  formatUpcomingTime,
  upcomingBadgeClass,
  upcomingBadgeLabel,
} from "@/lib/upcomingEventPresentation";
import type { CalendarEvent } from "@/hooks/queries/calendar";

export default function CalendarEventCard({ event }: { event: CalendarEvent }) {
  const [loaded, setLoaded] = useState(false);
  const thumbhashUrl = event.poster_thumbhash ? decodeThumbhash(event.poster_thumbhash) : "";

  const href =
    event.type === "movie"
      ? `/item/${event.content_id}`
      : `/item/${event.series_id ?? event.content_id}`;

  const subtitle = formatUpcomingSubtitle(event);
  const airTime = formatUpcomingTime(event.air_time, event.air_at);

  return (
    <div className="media-card group/card">
      <ViewTransitionLink to={href} className="block overflow-hidden rounded-xl">
        <div
          className="media-card-image relative aspect-[2/3]"
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
          {event.poster_url ? (
            <img
              src={event.poster_url}
              alt={event.title}
              className={`h-full w-full object-cover transition-opacity duration-300 ${loaded ? "opacity-100" : "opacity-0"}`}
              loading="lazy"
              onLoad={() => setLoaded(true)}
            />
          ) : (
            <div className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-1 p-3 text-center text-sm">
              <span className="line-clamp-3 font-medium">{event.title || "No Poster"}</span>
            </div>
          )}
          <div className="from-background/70 pointer-events-none absolute inset-x-0 bottom-0 h-24 bg-gradient-to-t to-transparent opacity-90" />
          {event.badges.length > 0 && (
            <div className="absolute top-2.5 left-2.5 flex flex-wrap gap-1">
              {event.badges.map((badge) => (
                <span
                  key={badge}
                  className={`rounded-full border px-2 py-0.5 text-[10px] leading-none font-semibold tracking-wide uppercase backdrop-blur-sm ${upcomingBadgeClass(badge)}`}
                >
                  {upcomingBadgeLabel(badge)}
                </span>
              ))}
            </div>
          )}
        </div>
      </ViewTransitionLink>
      <ViewTransitionLink to={href} className="block px-1 pt-3">
        <div className="truncate text-[14px] font-semibold tracking-tight">{event.title}</div>
        {subtitle && (
          <div className="text-muted-foreground mt-1 truncate text-[11px] font-medium tracking-[0.14em] uppercase">
            {subtitle}
          </div>
        )}
        {airTime && (
          <div className="text-muted-foreground mt-0.5 text-[11px] font-medium">{airTime}</div>
        )}
      </ViewTransitionLink>
    </div>
  );
}
