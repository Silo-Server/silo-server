import { useRef } from "react";
import { Check, Layers } from "lucide-react";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import MediaCardArtwork, {
  MEDIA_CARD_CAPTION_CLASS,
  MEDIA_CARD_META_CLASS,
  MEDIA_CARD_TITLE_CLASS,
} from "@/components/MediaCardArtwork";
import type { BrowseItem } from "@/api/types";
import { timeAgo } from "@/lib/timeAgo";
import MediaItemMenu from "@/components/MediaItemMenu";
import CardOverlays from "@/components/overlays/CardOverlays";
import { overlayDataFromBrowseItem, type CardOverlayPrefs } from "@/lib/overlays";
import { buildEpisodeCardLabels } from "@/lib/episodeCardLabels";
import { formatDate as formatPreferredDate } from "@/lib/datetime";
import { formatBitrate } from "@/lib/mediaFormat";
import { formatOutOfTen, formatPercent } from "@/components/ratings/ratings";
import { useUICustomization } from "@/hooks/useUICustomization";
import { buildItemHref } from "@/lib/mediaNavigation";
import CardPlayOverlay from "@/components/CardPlayOverlay";
import type { CardQuickActionMode } from "@/lib/cardQuickActions";

const DATE_ONLY_PATTERN = /^\d{4}-\d{2}-\d{2}$/;

function formatDate(value?: string | null) {
  if (!value) {
    return null;
  }
  const date = new Date(DATE_ONLY_PATTERN.test(value) ? `${value}T00:00:00` : value);
  return formatPreferredDate(date, "medium") || null;
}

function formatRuntime(minutes?: number | null) {
  if (!minutes || minutes <= 0) {
    return null;
  }
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  if (hours === 0) {
    return `${remainingMinutes}m`;
  }
  return remainingMinutes === 0 ? `${hours}h` : `${hours}h ${remainingMinutes}m`;
}

// A rating reads the same as on the title page: its source's mark, then the
// score on the source's own scale ("IMDb 8.1", "RT 93%").
function ratingLabel(mark: string, score: string | null) {
  return score != null ? (
    <>
      <span className="not-uppercase">{mark}</span> {score}
    </>
  ) : null;
}

function outOfTen(value?: number | null) {
  return value != null ? formatOutOfTen(value) : null;
}

function percent(value?: number | null) {
  return value != null ? formatPercent(value) : null;
}

function formatProgress(ratio?: number | null) {
  if (ratio == null) {
    return null;
  }
  return `${Math.round(Math.max(0, Math.min(1, ratio)) * 100)}%`;
}

// mangaCountChipLabel returns the top-right poster chip label for a manga
// browse item, or null when the item is not manga or has no counts. The server
// sends distinct volumes (manga_volume_count) and loose un-volumed chapters
// (manga_chapter_count) separately. Labels are abbreviated ("12 Vol · 3 Ch")
// so the chip fits narrow cards without occluding the cover; the detail page
// carries the spelled-out counts. Strictly manga-gated so no other card type
// renders it.
function mangaCountChipLabel(item: BrowseItem): string | null {
  if (item.type !== "manga") {
    return null;
  }
  const volumes = item.manga_volume_count ?? 0;
  const chapters = item.manga_chapter_count ?? 0;
  const parts: string[] = [];
  if (volumes > 0) {
    parts.push(`${volumes} Vol`);
  }
  if (chapters > 0) {
    parts.push(`${chapters} Ch`);
  }
  return parts.length > 0 ? parts.join(" · ") : null;
}

// mangaStatusChip returns the top-left publication-status pill for a manga
// browse card (color-coded), or null when the item is not manga or has no
// status. Strictly manga-gated so no other card type renders it.
function mangaStatusChip(item: BrowseItem): { label: string; tone: string } | null {
  if (item.type !== "manga") {
    return null;
  }
  const status = item.show_status?.trim();
  if (!status) {
    return null;
  }
  const tone =
    {
      Ongoing: "text-emerald-200 border-emerald-400/30",
      Completed: "text-sky-200 border-sky-400/30",
      Hiatus: "text-amber-200 border-amber-400/30",
      Cancelled: "text-red-300 border-red-400/30",
      Upcoming: "text-violet-200 border-violet-400/30",
    }[status] ?? "text-foreground border-white/15";
  return { label: status, tone };
}

function SortMeta({ item, sortField }: { item: BrowseItem; sortField?: string }) {
  const episodeLabels = buildEpisodeCardLabels(item);
  const defaultLabel = [item.year || "", item.type === "series" ? "Series" : ""]
    .filter(Boolean)
    .join(" · ");

  switch (sortField) {
    case "added_at":
    case "recently_added": {
      const ago = item.added_at ? timeAgo(item.added_at) : null;
      return <>{ago ?? defaultLabel}</>;
    }
    case "title":
      if (episodeLabels) {
        return <>{episodeLabels.episodeCode}</>;
      }
      return <>{defaultLabel}</>;
    case "year":
      return <>{item.year || defaultLabel}</>;
    case "content_rating":
      return <>{item.content_rating || defaultLabel}</>;
    case "runtime":
      return (
        <>{formatRuntime(item.sort_metrics?.runtime_minutes ?? item.runtime) ?? defaultLabel}</>
      );
    case "rating_imdb":
      return ratingLabel("IMDb", outOfTen(item.rating_imdb)) ?? <>{defaultLabel}</>;
    case "rating_tmdb":
      return ratingLabel("TMDB", outOfTen(item.rating_tmdb)) ?? <>{defaultLabel}</>;
    case "rating_rt_critic":
      return ratingLabel("RT", percent(item.rating_rt_critic)) ?? <>{defaultLabel}</>;
    case "rating_rt_audience":
      return ratingLabel("RT Audience", percent(item.rating_rt_audience)) ?? <>{defaultLabel}</>;
    case "release_date":
      return (
        <>{formatDate(item.sort_metrics?.release_date ?? item.release_date) ?? defaultLabel}</>
      );
    case "last_air_date":
      return <>{formatDate(item.last_air_date) ?? defaultLabel}</>;
    case "resolution":
      return (
        <>{item.sort_metrics?.resolution || item.overlay_summary?.resolution || defaultLabel}</>
      );
    case "bitrate":
      return <>{formatBitrate(item.sort_metrics?.bitrate_kbps ?? undefined) || defaultLabel}</>;
    case "progress":
      return <>{formatProgress(item.sort_metrics?.progress_ratio) ?? defaultLabel}</>;
    case "date_viewed":
      return <>{formatDate(item.sort_metrics?.viewed_at) ?? defaultLabel}</>;
    case "plays":
      return <>{item.sort_metrics?.play_count ?? defaultLabel}</>;
    case "author":
      return <>{item.sort_metrics?.author || defaultLabel}</>;
    case "narrator":
      return <>{item.sort_metrics?.narrator || defaultLabel}</>;
    case "series":
      return <>{item.sort_metrics?.series_name || defaultLabel}</>;
    default:
      if (episodeLabels) {
        return <>{episodeLabels.episodeCode}</>;
      }
      return <>{defaultLabel}</>;
  }
}

export default function ItemCard({
  item,
  libraryId,
  sortField,
  overlayPrefs,
  quickActionMode = "none",
  narrowPosterActions = false,
  selectionMode = false,
  selected = false,
  onToggleSelect,
}: {
  item: BrowseItem;
  libraryId?: number;
  sortField?: string;
  overlayPrefs?: CardOverlayPrefs | null;
  quickActionMode?: CardQuickActionMode;
  narrowPosterActions?: boolean;
  selectionMode?: boolean;
  selected?: boolean;
  onToggleSelect?: (item: BrowseItem) => void;
}) {
  const itemHref = buildItemHref({ contentId: item.content_id, libraryId });
  const episodeLabels = buildEpisodeCardLabels(item);
  const displayTitle = episodeLabels ? episodeLabels.seriesTitle : item.title;
  const headingHref =
    item.type === "episode" && item.series_id
      ? buildItemHref({ contentId: item.series_id, libraryId })
      : itemHref;
  const mangaCountLabel = mangaCountChipLabel(item);
  const mangaStatus = mangaStatusChip(item);
  const { cardPresentation } = useUICustomization();
  const showCaption = cardPresentation.caption !== "artwork";
  const showMetadata = cardPresentation.caption === "title_metadata";
  const cardRef = useRef<HTMLDivElement>(null);

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
            alt={displayTitle}
            mediaType={item.type}
            thumbhash={item.poster_thumbhash}
            square={item.type === "audiobook"}
            scrim="background"
          >
            {item.status === "pending" && (
              <span className="glass-subtle text-foreground absolute top-2.5 left-2.5 rounded-full border border-white/15 px-2.5 py-1 text-[0.625rem] font-semibold tracking-[0.14em] uppercase">
                Scanning
              </span>
            )}
            {item.status === "unmatched" && (
              <span className="glass-subtle absolute top-2.5 left-2.5 rounded-full border border-red-500/25 px-2.5 py-1 text-[0.625rem] font-semibold tracking-[0.14em] text-red-300 uppercase">
                Unmatched
              </span>
            )}
            {item.status === "ambiguous" && (
              <span className="glass-subtle absolute top-2.5 left-2.5 rounded-full border border-amber-500/25 px-2.5 py-1 text-[0.625rem] font-semibold tracking-[0.14em] text-amber-200 uppercase">
                Ambiguous
              </span>
            )}
            {/* Manga cards carry purpose-built status/count chips in both top
                corners; generic overlays would render underneath them. */}
            {item.status === "matched" && item.type !== "manga" && overlayPrefs && (
              <CardOverlays data={overlayDataFromBrowseItem(item)} prefs={overlayPrefs} />
            )}
            {(mangaStatus || mangaCountLabel) && (
              /* One shared row so the two chips split the card width and
                 truncate instead of overlapping on narrow cards. */
              <div className="pointer-events-none absolute inset-x-2.5 top-2.5 flex items-start justify-between gap-1.5">
                {mangaStatus ? (
                  <span
                    className={`glass-chip min-w-0 truncate rounded-full border px-2.5 py-1 text-[0.625rem] font-semibold tracking-[0.14em] uppercase ${mangaStatus.tone}`}
                  >
                    {mangaStatus.label}
                  </span>
                ) : (
                  <span />
                )}
                {mangaCountLabel && (
                  <span className="glass-chip text-foreground inline-flex min-w-0 items-center gap-1 rounded-full border border-white/15 px-2.5 py-1 text-[0.625rem] font-semibold tracking-[0.14em] uppercase">
                    <Layers className="size-3 shrink-0" />
                    <span className="truncate">{mangaCountLabel}</span>
                  </span>
                )}
              </div>
            )}
          </MediaCardArtwork>
        </ViewTransitionLink>
        {!selectionMode && item.play_content_id ? (
          <CardPlayOverlay
            contentId={item.play_content_id}
            title={displayTitle}
            type={item.type === "movie" ? "movie" : "episode"}
            libraryId={libraryId}
          />
        ) : null}
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
          libraryId={libraryId}
          userState={item.user_state}
          variant="poster"
          narrowPosterActions={narrowPosterActions}
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
          {showMetadata && episodeLabels?.episodeTitle ? (
            <ViewTransitionLink
              to={itemHref}
              className="text-muted-foreground mt-1 block truncate text-[0.75rem] font-medium hover:underline"
            >
              {episodeLabels.episodeTitle}
            </ViewTransitionLink>
          ) : null}
          {showMetadata ? (
            <ViewTransitionLink to={itemHref} className={MEDIA_CARD_META_CLASS}>
              <SortMeta item={item} sortField={sortField} />
            </ViewTransitionLink>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
