import { useState } from "react";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { decodeThumbhash } from "@/lib/thumbhash";
import { formatRuntimeMinutes } from "@/lib/mediaFormat";
import type { ItemDetail } from "@/api/types";

export function itemHeadline(item: ItemDetail) {
  if (item.type === "episode" && item.series_title) {
    const code = `S${item.season_number ?? "?"} E${item.episode_number ?? "?"}`;
    return item.title
      ? `${item.series_title} — ${code} "${item.title}"`
      : `${item.series_title} — ${code}`;
  }
  return item.year ? `${item.title} (${item.year})` : item.title;
}

export function itemMetaLine(item: ItemDetail) {
  const parts: string[] = [];
  if (item.runtime) parts.push(formatRuntimeMinutes(item.runtime));
  if (item.content_rating) parts.push(item.content_rating);
  if (item.type === "movie" && item.genres?.length) parts.push(item.genres.slice(0, 2).join(", "));
  return parts.join(" · ");
}

/**
 * The stage hero: poster and backdrop for whatever the room is about to play
 * or is playing, with an eyebrow, headline, meta line and a slot for actions.
 */
export function StagedHero({
  contentId,
  libraryId,
  eyebrow,
  eyebrowTone = "muted",
  badge,
  caption,
  children,
}: {
  contentId: string;
  libraryId?: number;
  eyebrow: string;
  eyebrowTone?: "muted" | "live" | "leading";
  badge?: React.ReactNode;
  caption?: string;
  children?: React.ReactNode;
}) {
  const detail = useCatalogItemDetail(contentId, libraryId);
  const item = detail.data;
  const [backdropLoaded, setBackdropLoaded] = useState(false);
  const [posterLoaded, setPosterLoaded] = useState(false);
  const thumbhash = item?.poster_thumbhash ? decodeThumbhash(item.poster_thumbhash) : "";
  const eyebrowClass =
    eyebrowTone === "live"
      ? "text-emerald-400/90"
      : eyebrowTone === "leading"
        ? "text-amber-300/90"
        : "text-muted-foreground";

  return (
    <div className="relative overflow-hidden rounded-xl border border-white/10">
      {item?.backdrop_url ? (
        <img
          src={item.backdrop_url}
          alt=""
          className={`absolute inset-0 h-full w-full object-cover transition-opacity duration-700 ${
            backdropLoaded ? "opacity-25" : "opacity-0"
          }`}
          onLoad={() => setBackdropLoaded(true)}
        />
      ) : null}
      <div className="from-background via-background/90 absolute inset-0 bg-gradient-to-r to-transparent" />
      <div className="relative flex gap-5 p-5 sm:p-6">
        <div
          className="media-card-image hidden aspect-[2/3] w-28 shrink-0 overflow-hidden sm:block"
          style={
            thumbhash
              ? { backgroundImage: `url(${thumbhash})`, backgroundSize: "cover" }
              : undefined
          }
        >
          {item?.poster_url ? (
            <img
              src={item.poster_url}
              alt=""
              className={`h-full w-full object-cover transition-opacity duration-300 ${
                posterLoaded ? "opacity-100" : "opacity-0"
              }`}
              onLoad={() => setPosterLoaded(true)}
            />
          ) : null}
        </div>
        <div className="flex min-w-0 flex-1 flex-col justify-center gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <span
              className={`text-[11px] font-semibold tracking-[0.18em] uppercase ${eyebrowClass}`}
            >
              {eyebrow}
            </span>
            {badge}
          </div>
          <h2 className="text-xl font-semibold tracking-tight sm:text-2xl">
            {item ? itemHeadline(item) : detail.isError ? "Unavailable" : "Loading…"}
          </h2>
          {item ? (
            <div className="text-muted-foreground text-sm">
              {[itemMetaLine(item), caption].filter(Boolean).join(" · ")}
            </div>
          ) : caption ? (
            <div className="text-muted-foreground text-sm">{caption}</div>
          ) : null}
          {children ? (
            <div className="mt-1 flex flex-wrap items-center gap-3">{children}</div>
          ) : null}
        </div>
      </div>
    </div>
  );
}
