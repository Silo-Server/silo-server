import { useState } from "react";
import { Check } from "lucide-react";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import type { BrowseItem } from "@/api/types";
import { decodeThumbhash } from "@/lib/thumbhash";
import { timeAgo } from "@/lib/timeAgo";
import MediaItemMenu from "@/components/MediaItemMenu";
import CardOverlays from "@/components/overlays/CardOverlays";
import { overlayDataFromBrowseItem, type CardOverlayPrefs } from "@/lib/overlays";

function SortMeta({ item, sortField }: { item: BrowseItem; sortField?: string }) {
  if (
    item.type === "episode" &&
    item.series_title &&
    item.season_number != null &&
    item.episode_number != null
  ) {
    return (
      <>
        S{item.season_number} E{item.episode_number}
        {item.title ? ` • ${item.title}` : ""}
      </>
    );
  }

  const defaultLabel = [item.year || "", item.type === "series" ? "Series" : ""]
    .filter(Boolean)
    .join(" · ");

  switch (sortField) {
    case "added_at":
    case "recently_added": {
      const ago = item.added_at ? timeAgo(item.added_at) : null;
      return <>{ago ?? defaultLabel}</>;
    }
    case "rating_imdb":
      return item.rating_imdb != null ? (
        <>
          <span className="not-uppercase">★</span> {item.rating_imdb.toFixed(1)} / 10
        </>
      ) : (
        <>{defaultLabel}</>
      );
    case "release_date":
      return item.release_date ? (
        <>
          {new Date(item.release_date).toLocaleDateString(undefined, {
            year: "numeric",
            month: "short",
            day: "numeric",
          })}
        </>
      ) : (
        <>{defaultLabel}</>
      );
    case "last_air_date":
      return item.last_air_date ? (
        <>
          {new Date(item.last_air_date).toLocaleDateString(undefined, {
            year: "numeric",
            month: "short",
            day: "numeric",
          })}
        </>
      ) : (
        <>{defaultLabel}</>
      );
    default:
      return <>{defaultLabel}</>;
  }
}

export default function ItemCard({
  item,
  libraryId,
  sortField,
  overlayPrefs,
  selectionMode = false,
  selected = false,
  onToggleSelect,
}: {
  item: BrowseItem;
  libraryId?: number;
  sortField?: string;
  overlayPrefs?: CardOverlayPrefs | null;
  selectionMode?: boolean;
  selected?: boolean;
  onToggleSelect?: (item: BrowseItem) => void;
}) {
  const [loaded, setLoaded] = useState(false);
  const thumbhashUrl = item.poster_thumbhash ? decodeThumbhash(item.poster_thumbhash) : "";
  const itemHref = `/item/${item.content_id}${libraryId ? `?libraryId=${libraryId}` : ""}`;
  const displayTitle =
    item.type === "episode" && item.series_title ? item.series_title : item.title;

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
                alt={displayTitle}
                className={`h-full w-full object-cover transition-opacity duration-300 ${loaded ? "opacity-100" : "opacity-0"}`}
                onLoad={() => setLoaded(true)}
              />
            ) : (
              <div className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-1 p-3 text-center text-sm">
                <span className="line-clamp-3 font-medium">{displayTitle || "No Poster"}</span>
              </div>
            )}
            <div className="from-background/70 pointer-events-none absolute inset-x-0 bottom-0 h-24 bg-gradient-to-t to-transparent opacity-90" />
            {item.status === "pending" && (
              <span className="glass-subtle text-foreground absolute top-2.5 left-2.5 rounded-full border border-white/15 px-2.5 py-1 text-[10px] font-semibold tracking-[0.14em] uppercase">
                Scanning
              </span>
            )}
            {item.status === "unmatched" && (
              <span className="glass-subtle absolute top-2.5 left-2.5 rounded-full border border-red-500/25 px-2.5 py-1 text-[10px] font-semibold tracking-[0.14em] text-red-300 uppercase">
                Unmatched
              </span>
            )}
            {item.status === "ambiguous" && (
              <span className="glass-subtle absolute top-2.5 left-2.5 rounded-full border border-amber-500/25 px-2.5 py-1 text-[10px] font-semibold tracking-[0.14em] text-amber-200 uppercase">
                Ambiguous
              </span>
            )}
            {item.status === "matched" && overlayPrefs && (
              <CardOverlays data={overlayDataFromBrowseItem(item)} prefs={overlayPrefs} />
            )}
          </div>
        </ViewTransitionLink>
        {selectionMode && onToggleSelect && (
          <button
            type="button"
            aria-label={selected ? `Deselect ${item.title}` : `Select ${item.title}`}
            aria-pressed={selected}
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              onToggleSelect(item);
            }}
            onPointerDown={(event) => {
              event.preventDefault();
              event.stopPropagation();
            }}
            className="absolute top-2.5 left-2.5 z-20 inline-flex size-8 items-center justify-center rounded-full border border-white/20 bg-black/55 text-white shadow-sm backdrop-blur-sm transition-colors hover:bg-black/70"
          >
            <span
              className={`flex size-4 items-center justify-center rounded-full border ${
                selected ? "border-primary bg-primary text-primary-foreground" : "border-white/70"
              }`}
            >
              {selected && <Check className="size-3" />}
            </span>
          </button>
        )}
        <MediaItemMenu
          contentId={item.content_id}
          mediaType={item.type}
          userState={item.user_state}
          variant="poster"
        />
      </div>
      <ViewTransitionLink to={itemHref} className="block px-1 pt-3">
        <div className="truncate text-[14px] font-semibold tracking-tight">{displayTitle}</div>
        <div className="text-muted-foreground mt-1 text-[11px] font-medium tracking-[0.14em] uppercase">
          <SortMeta item={item} sortField={sortField} />
        </div>
      </ViewTransitionLink>
    </div>
  );
}
