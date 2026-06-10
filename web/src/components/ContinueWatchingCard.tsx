import ViewTransitionLink from "@/components/ViewTransitionLink";
import { BookOpen, Play } from "lucide-react";
import { useCallback } from "react";
import { useLocation } from "react-router";
import type { ItemDetail, SectionItem } from "@/api/types";
import type { ProgressEntry } from "@/api/types";
import MediaItemMenu from "@/components/MediaItemMenu";
import CardOverlays from "@/components/overlays/CardOverlays";
import { overlayDataFromSectionItem, type CardOverlayPrefs } from "@/lib/overlays";
import { formatListeningTimeLeft } from "@/lib/audiobooks/duration";
import { upcomingBadgeClass, upcomingBadgeLabel } from "@/lib/upcomingEventPresentation";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { parseWatchHref } from "@/pages/watchRouteHelpers";
import { buildItemHref, buildMediaPlayHref, isVideoWatchHref } from "@/lib/mediaNavigation";

type ContinueWatchingCardProps = (
  | {
      detail: ItemDetail;
      progress: ProgressEntry;
      sectionItem?: never;
    }
  | {
      sectionItem: SectionItem;
      detail?: never;
      progress?: never;
    }
) & {
  overlayPrefs?: CardOverlayPrefs | null;
  libraryId?: number;
  variant?: "wide" | "poster";
};

export default function ContinueWatchingCard(props: ContinueWatchingCardProps) {
  const location = useLocation();
  const playbackController = useWatchPlaybackController();
  const card =
    "sectionItem" in props && props.sectionItem
      ? {
          watchHref: buildMediaPlayHref({
            contentId: props.sectionItem.content_id,
            type: props.sectionItem.type,
            libraryId: props.libraryId,
          }),
          itemHref: buildItemHref({
            contentId: props.sectionItem.content_id,
            libraryId: props.libraryId,
          }),
          title: props.sectionItem.title,
          seriesId: props.sectionItem.series_id,
          seriesTitle: props.sectionItem.series_title,
          seasonNumber: props.sectionItem.season_number,
          episodeNumber: props.sectionItem.episode_number,
          backdropUrl: props.sectionItem.backdrop_url,
          posterUrl: props.sectionItem.poster_url,
          positionSeconds: props.sectionItem.position_seconds ?? 0,
          durationSeconds: props.sectionItem.duration_seconds ?? 0,
          type: props.sectionItem.type,
        }
      : {
          watchHref: buildMediaPlayHref({
            contentId: props.detail.content_id,
            type: props.detail.type,
            libraryId: props.libraryId,
          }),
          itemHref: buildItemHref({
            contentId: props.detail.content_id,
            libraryId: props.libraryId,
          }),
          title: props.detail.title,
          seriesId: props.detail.series_id,
          seriesTitle: props.detail.series_title,
          seasonNumber: props.detail.season_number,
          episodeNumber: props.detail.episode_number,
          backdropUrl: props.detail.backdrop_url,
          posterUrl: props.detail.poster_url,
          positionSeconds: props.progress.position_seconds,
          durationSeconds: props.progress.duration_seconds,
          type: props.detail.type,
        };

  const isNextUp = "sectionItem" in props && props.sectionItem?.item_source === "next_up";
  const dismissAction =
    "sectionItem" in props && props.sectionItem
      ? props.sectionItem.item_source === "next_up"
        ? props.sectionItem.series_id
          ? {
              itemId: props.sectionItem.content_id,
              surface: "next_up" as const,
              mediaType: props.sectionItem.type,
              seriesId: props.sectionItem.series_id,
            }
          : undefined
        : props.sectionItem.progress_updated_at
          ? {
              itemId: props.sectionItem.content_id,
              surface: "continue_watching" as const,
              mediaType: props.sectionItem.type,
              progressUpdatedAt: props.sectionItem.progress_updated_at,
            }
          : undefined
      : {
          itemId: props.detail.content_id,
          surface: "continue_watching" as const,
          mediaType: props.detail.type,
          progressUpdatedAt: props.progress.updated_at,
        };
  const progressPercent =
    card.durationSeconds > 0 ? (card.positionSeconds / card.durationSeconds) * 100 : 0;
  const hasPartialProgress = progressPercent > 0 && progressPercent < 100;
  const hasEpisodeMeta = card.seasonNumber != null && card.episodeNumber != null;
  const headingIsSeries = hasEpisodeMeta && !!card.seriesTitle;
  const heading = headingIsSeries ? card.seriesTitle : card.title;
  // The heading shows the series title for episodes, so it should navigate to
  // the series page; everything else heads to the item's own page.
  const headingHref =
    headingIsSeries && card.seriesId
      ? buildItemHref({ contentId: card.seriesId, libraryId: props.libraryId })
      : card.itemHref;
  const episodeLabel = hasEpisodeMeta
    ? `Season ${card.seasonNumber} Episode ${card.episodeNumber}`
    : null;
  const episodeMeta = hasEpisodeMeta
    ? card.seriesTitle && card.title
      ? `${episodeLabel} • ${card.title}`
      : episodeLabel
    : null;
  const premiereBadge =
    "sectionItem" in props && props.sectionItem
      ? props.sectionItem.badges?.find((badge) => badge === "season_premiere")
      : undefined;
  const timeLeftLabel = isNextUp
    ? premiereBadge
      ? null
      : "Next Episode"
    : card.type === "ebook"
      ? `${Math.round(Math.min(Math.max(progressPercent, 0), 100))}% read`
      : card.durationSeconds > 0
        ? card.type === "audiobook"
          ? formatListeningTimeLeft(card.positionSeconds, card.durationSeconds)
          : `${Math.round((card.durationSeconds - card.positionSeconds) / 60)} min left`
        : "\u00A0";
  const handleWatchClick = useCallback(
    (event: React.MouseEvent<HTMLAnchorElement>) => {
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.altKey ||
        event.ctrlKey ||
        event.shiftKey
      ) {
        return;
      }

      if (!isVideoWatchHref(card.watchHref)) {
        return;
      }

      const parsed = parseWatchHref(card.watchHref);
      if (!parsed) {
        return;
      }

      event.preventDefault();
      playbackController.startPlayback({
        contentId: parsed.contentId,
        fileId: parsed.fileId,
        libraryId: parsed.libraryId,
        restart: parsed.restart,
        returnHref: `${location.pathname}${location.search}`,
      });
    },
    [card.watchHref, location.pathname, location.search, playbackController],
  );

  const variant = props.variant ?? "wide";
  const isPoster = variant === "poster";
  const containerWidth = isPoster
    ? "w-[140px] shrink-0 sm:w-[160px] lg:w-[185px]"
    : "w-[260px] shrink-0 sm:w-[315px]";
  // Audiobook covers are square (Audible-style); use 1:1 for the poster
  // variant so they don't get top/bottom-cropped into a 2:3 frame.
  const imageAspect = isPoster
    ? card.type === "audiobook"
      ? "aspect-square"
      : "aspect-[2/3]"
    : "aspect-video";
  const isSectionEpisode = "sectionItem" in props && props.sectionItem?.type === "episode";
  // Episodes store the horizontal still in poster_url (see
  // episode_catalog_source.go); wide-variant movies/series/seasons need the
  // backdrop for the 16:9 card. Poster variant always wants the vertical
  // poster, except section-episode payloads which use poster_url for the
  // vertical season/series artwork and backdrop_url for the episode still.
  const imagePrimary = isPoster
    ? card.posterUrl
    : isSectionEpisode
      ? card.backdropUrl
      : card.type === "episode"
        ? card.posterUrl
        : card.backdropUrl;
  const imageFallback = isPoster
    ? card.backdropUrl
    : isSectionEpisode
      ? card.posterUrl
      : card.type === "episode"
        ? card.backdropUrl
        : card.posterUrl;
  const imageSrc = imagePrimary || imageFallback;

  return (
    <div className={`group/card ${containerWidth}`}>
      <div className="group/media relative">
        <ViewTransitionLink to={card.itemHref} className="block">
          <div className={`media-card-image relative ${imageAspect} overflow-hidden rounded-xl`}>
            {imageSrc ? (
              <img
                src={imageSrc}
                alt={heading}
                className="h-full w-full object-cover transition-transform duration-300 group-hover/media:scale-105"
                loading="lazy"
              />
            ) : (
              <div className="text-muted-foreground bg-surface flex h-full w-full items-center justify-center text-sm">
                No Image
              </div>
            )}

            {"sectionItem" in props && props.sectionItem && props.overlayPrefs && (
              <CardOverlays
                data={overlayDataFromSectionItem(props.sectionItem)}
                prefs={props.overlayPrefs}
                variant={variant}
              />
            )}

            {/* Hover dim behind the play button */}
            <div className="absolute inset-0 bg-black/0 transition-colors duration-150 pointer-fine:group-hover/media:bg-black/30" />

            {/* Progress bar */}
            {!isNextUp && progressPercent > 0 && (
              <div className="bg-background/40 absolute inset-x-0 bottom-0 h-[3px]">
                <div
                  className="h-full transition-all duration-300"
                  style={{
                    width: `${Math.min(progressPercent, 100)}%`,
                    background: "var(--primary)",
                  }}
                />
              </div>
            )}
          </div>
        </ViewTransitionLink>
        <ViewTransitionLink
          to={card.watchHref}
          onClick={handleWatchClick}
          aria-label={`${card.type === "ebook" ? "Read" : "Play"} ${heading}`}
          className="bg-primary text-primary-foreground absolute top-1/2 left-1/2 flex h-11 w-11 -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full opacity-100 shadow-lg transition-all duration-200 hover:scale-110 hover:shadow-xl hover:brightness-110 active:scale-95 pointer-fine:pointer-events-none pointer-fine:opacity-0 pointer-fine:group-hover/media:pointer-events-auto pointer-fine:group-hover/media:opacity-100 pointer-fine:focus-visible:pointer-events-auto pointer-fine:focus-visible:opacity-100"
        >
          {card.type === "ebook" ? (
            <BookOpen className="h-5 w-5" />
          ) : (
            <Play className="ml-0.5 h-5 w-5" fill="currentColor" />
          )}
        </ViewTransitionLink>
        <MediaItemMenu
          contentId={
            "sectionItem" in props && props.sectionItem
              ? props.sectionItem.content_id
              : props.detail.content_id
          }
          mediaType={
            "sectionItem" in props && props.sectionItem ? props.sectionItem.type : props.detail.type
          }
          libraryId={props.libraryId}
          userState={
            "sectionItem" in props && props.sectionItem ? props.sectionItem.user_state : undefined
          }
          variant={variant}
          dismissAction={dismissAction}
          hasPartialProgress={hasPartialProgress}
        />
      </div>

      {/* Info */}
      <div className="px-0.5 pt-2.5">
        <ViewTransitionLink
          to={headingHref}
          className="block truncate text-[13px] font-semibold hover:underline"
        >
          {heading}
        </ViewTransitionLink>
        {episodeMeta && (
          <ViewTransitionLink
            to={card.itemHref}
            className="text-muted-foreground block truncate text-xs hover:underline"
          >
            {episodeMeta}
          </ViewTransitionLink>
        )}
        {premiereBadge && (
          <div className="mt-1">
            <span
              className={`inline-flex rounded-full border px-2 py-0.5 text-[10px] leading-none font-semibold tracking-wide uppercase backdrop-blur-sm ${upcomingBadgeClass(
                premiereBadge,
              )}`}
            >
              {upcomingBadgeLabel(premiereBadge)}
            </span>
          </div>
        )}
        {timeLeftLabel &&
          (timeLeftLabel === "\u00A0" ? (
            <div className="text-muted-foreground text-xs">{timeLeftLabel}</div>
          ) : (
            <ViewTransitionLink
              to={card.itemHref}
              className="text-muted-foreground block w-fit text-xs hover:underline"
            >
              {timeLeftLabel}
            </ViewTransitionLink>
          ))}
      </div>
    </div>
  );
}
