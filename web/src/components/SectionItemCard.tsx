import { memo, useEffect, useRef, useState } from "react";
import { BookOpen, Check, Info, Play, Plus } from "lucide-react";
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
import { buildItemHref, buildMediaPlayHref } from "@/lib/mediaNavigation";
import CardPlayOverlay from "@/components/CardPlayOverlay";
import HeroTrailerLayer from "@/components/HeroTrailerLayer";
import { heroClipUrl } from "@/components/heroClips";
import { RatingEntry } from "@/components/ratings/RatingEntry";
import { formatHeroMetadata } from "@/components/heroMetadata";
import { useFinePointer } from "@/hooks/useFinePointer";
import { useToggleWatchlist } from "@/hooks/queries/watchlist";
import { FEATURED_HOVER_DELAY_MS, FEATURED_TRAILER_DELAY_MS } from "@/lib/featuredCard";
import { mediaPlayLabel } from "@/lib/mediaPlayLabel";
import { rankNumeral } from "@/lib/rankNumerals";

interface SectionItemCardProps {
  item: SectionItem;
  libraryId?: number;
  overlayPrefs?: CardOverlayPrefs | null;
  quickActionMode?: CardQuickActionMode;
  /** On screen in the first row at first paint: load the poster eagerly. */
  priority?: boolean;
  /**
   * This card's place in a ranked row, or undefined for an ordinary one. Ranks
   * above MAX_RANKED_CARDS draw nothing: the numerals exist for 1-10, and a
   * plain digit would not match their outline treatment.
   */
  rank?: number;
  /**
   * The ranked gutter as padding on the card, from rankedCarouselGutterClasses.
   * Only meaningful with rank: it is the space the numeral occupies.
   *
   * It sits on the card, not inside the poster box, so the poster box stays
   * exactly the poster's width. That matters: the hover play button centres on
   * its parent, so padding inside that box would centre it on the numeral
   * instead of the poster.
   */
  rankedGutterClasses?: string;
  /**
   * The numeral's own offset, from rankedCarouselNumeralClasses: the same
   * gutter, negated, so a numeral anchored inside the poster box starts at the
   * card's left edge.
   */
  rankedNumeralClasses?: string;
  /**
   * This row's cards open into a landscape tile when the pointer rests on them.
   * The row decides this, because only one card may be open at a time.
   */
  expandable?: boolean;
  /** This card is the one open. */
  expanded?: boolean;
  /** The pointer has rested on this card for FEATURED_HOVER_DELAY_MS. */
  onExpand?: () => void;
  /** The pointer left, so the row may close this card. */
  onCollapse?: () => void;
  /**
   * The card's resting width classes, from carouselCardWidthClasses. Applied to
   * the poster while a card is open, so the poster keeps its own size as the card
   * widens around it rather than stretching with it.
   */
  restingWidthClasses?: string;
  /**
   * This profile plays a title's clip, read once by the row rather than by each
   * card — the same reason the row passes its overlay prefs down.
   */
  trailersEnabled?: boolean;
}

export default memo(function SectionItemCard({
  item,
  libraryId,
  overlayPrefs = null,
  quickActionMode = "none",
  priority = false,
  rank,
  rankedGutterClasses = "",
  rankedNumeralClasses = "",
  expandable = false,
  expanded = false,
  onExpand,
  onCollapse,
  restingWidthClasses = "",
  trailersEnabled = false,
}: SectionItemCardProps) {
  const numeral = rank === undefined ? undefined : rankNumeral(rank);
  const finePointer = useFinePointer();
  const clipUrl = expandable ? heroClipUrl(item) : undefined;
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  /*
   * The open tile's clip, in one piece of state: `played` waits out
   * FEATURED_TRAILER_DELAY_MS after the tile opens, so the tile settles first,
   * and `ready` waits for the element to answer `canplay`, so the fade can never
   * land on a black rectangle.
   */
  const [trailer, setTrailer] = useState({ played: false, ready: false });
  /*
   * The open tile's own affordances. The row's cards are library items, so the
   * watchlist toggle is the library one the item menu uses, not the TMDB-title
   * one the request lists use.
   */
  const toggleWatchlist = useToggleWatchlist(item.content_id);
  const inWatchlist = Boolean(item.user_state?.in_watchlist);
  const playLabel = mediaPlayLabel(item);
  const playHref = item.play_content_id
    ? buildMediaPlayHref({
        contentId: item.play_content_id,
        type: item.type === "movie" ? "movie" : "episode",
        libraryId,
      })
    : "";
  // Only the open tile reads these, so nothing is worked out for a resting card.
  const metadata = expanded ? formatHeroMetadata(item) : [];
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
  const cardRef = useRef<HTMLDivElement>(null);
  const displayTitle = episodeLabels ? episodeLabels.seriesTitle : item.title;

  useEffect(
    () => () => {
      if (hoverTimer.current !== null) {
        clearTimeout(hoverTimer.current);
      }
    },
    [],
  );

  useEffect(() => {
    if (!expanded || !clipUrl || !trailersEnabled) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- the reset follows the collapse; a bare guard would leave the last clip mounted over the next card
      setTrailer({ played: false, ready: false });
      return;
    }

    const timer = setTimeout(
      () => setTrailer({ played: true, ready: false }),
      FEATURED_TRAILER_DELAY_MS,
    );
    return () => clearTimeout(timer);
  }, [expanded, clipUrl, trailersEnabled]);

  /*
   * A fine pointer only. A tap opens the title exactly as it does today, so a
   * touch device never has to discover that a card can open.
   */
  const handlePointerEnter = () => {
    if (!expandable || !finePointer) {
      return;
    }
    if (hoverTimer.current !== null) {
      clearTimeout(hoverTimer.current);
    }
    hoverTimer.current = setTimeout(() => onExpand?.(), FEATURED_HOVER_DELAY_MS);
  };

  const handlePointerLeave = () => {
    if (hoverTimer.current !== null) {
      clearTimeout(hoverTimer.current);
      hoverTimer.current = null;
    }
    if (expanded) {
      onCollapse?.();
    }
  };

  return (
    <div
      ref={cardRef}
      onMouseEnter={handlePointerEnter}
      onMouseLeave={handlePointerLeave}
      className={`media-card media-card-longpress group/card relative ${rankedGutterClasses}${
        expanded ? "overflow-hidden rounded-xl" : ""
      }`}
    >
      <div className="group/media relative">
        {numeral ? (
          /*
           * A ranked card's numeral: it sits in the card's left gutter with its
           * right side cropped by the poster, which paints over it because the
           * poster comes later in the DOM.
           *
           * inset-y-0 gives it the poster box's exact height, because the gutter
           * padding on the card keeps this box exactly the poster's width. The
           * negated gutter pulls the numeral left, to the card's own edge. Anchor
           * it inside the padding instead and it would stretch down over the
           * caption as well.
           *
           * aria-hidden: decorative. The card's own title names the item, and the
           * row's title already says the row is a ranked list.
           */
          <span
            aria-hidden="true"
            data-rank={rank}
            className={`pointer-events-none absolute inset-y-0 z-0 flex ${rankedNumeralClasses} ${
              rank === 10 ? "items-center" : "items-end"
            }`}
          >
            <svg
              viewBox={numeral.viewBox}
              className={`block w-auto drop-shadow-[0_8px_20px_rgba(0,0,0,0.55)] ${
                rank === 10 ? "h-[95%]" : "h-full"
              }`}
              preserveAspectRatio="xMinYMax meet"
            >
              <path d={numeral.d} fill="none" stroke="#595959" strokeWidth="4" />
            </svg>
          </span>
        ) : null}
        <ViewTransitionLink
          to={itemHref}
          aria-label={displayTitle}
          /*
           * Pinned whenever this row's cards can open, not only while this one is
           * open. The card's width animates, and the artwork's height comes from
           * its own width, so a poster that followed the card would balloon to
           * the grown width's height for the length of the collapse and shove
           * every row below it down. Held at its resting width, the card's height
           * never changes and only the width moves.
           */
          className={`block overflow-hidden rounded-xl ${expandable ? restingWidthClasses : ""}`}
        >
          <MediaCardArtwork
            src={item.poster_url}
            alt={item.title}
            mediaType={item.type}
            thumbhash={item.poster_thumbhash}
            square={item.type === "audiobook"}
            lazy
            priority={priority}
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
      <div className={`${MEDIA_CARD_CAPTION_CLASS}${expanded ? "invisible" : ""}`}>
        <ViewTransitionLink to={headingHref} className={MEDIA_CARD_TITLE_CLASS}>
          {displayTitle}
        </ViewTransitionLink>
        {upcomingEvent ? (
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
        ) : episodeLabels ? (
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
        ) : item.item_source === "next_in_series" && item.series_title ? (
          <ViewTransitionLink to={itemHref} className={MEDIA_CARD_META_CLASS}>
            {[item.badges?.find((badge) => badge.startsWith("Book ")), item.series_title]
              .filter(Boolean)
              .join(" · ")}
          </ViewTransitionLink>
        ) : (
          <ViewTransitionLink to={itemHref} className={MEDIA_CARD_META_CLASS}>
            {item.year ? `${item.year}` : ""} {item.type === "series" ? "Series" : ""}
          </ViewTransitionLink>
        )}
      </div>
      {expanded ? (
        /*
         * The open tile: the backdrop, a dark gradient from the left so the logo
         * stays legible, and — once it has settled — the title's clip.
         *
         * It covers the whole card, so the caption is replaced by the logo while
         * a card is open, and the poster keeps its resting width underneath (see
         * the link above). The card itself is what widens, in SectionRow, and
         * that is what moves its neighbours along.
         *
         * pointer-events-none is deliberate: the resting poster link stays the
         * click target underneath, so opening a card never changes what a click
         * does.
         */
        <div className="media-card-featured-tile pointer-events-none absolute inset-0 z-20 overflow-hidden rounded-xl">
          {item.backdrop_url ? (
            <img
              src={item.backdrop_url}
              alt=""
              aria-hidden="true"
              className="absolute inset-0 h-full w-full object-cover"
            />
          ) : null}
          <div className="media-card-featured-gradient absolute inset-0" aria-hidden="true" />
          {trailer.played && clipUrl ? (
            <HeroTrailerLayer
              videoRef={videoRef}
              clipUrl={clipUrl}
              stage={trailer.ready ? "video" : "still"}
              muted
              playing
              onReady={() => setTrailer((current) => ({ ...current, ready: true }))}
              onProgress={() => {}}
              onEnded={() => {}}
              onUnavailable={() => setTrailer({ played: false, ready: false })}
            />
          ) : null}
          <div className="absolute bottom-0 left-0 z-30 flex w-[68%] flex-col items-start gap-3 p-4">
            {item.logo_url ? (
              <img
                src={item.logo_url}
                alt=""
                aria-hidden="true"
                className="max-h-16 w-auto max-w-full object-contain object-left-bottom drop-shadow-[0_4px_14px_rgba(0,0,0,0.5)]"
              />
            ) : (
              <p className="text-lg leading-tight font-semibold text-white drop-shadow-[0_2px_8px_rgba(0,0,0,0.6)]">
                {displayTitle}
              </p>
            )}
            {/*
             * The hero's own buttons and metadata row, at tile size: the play
             * pill, the watchlist toggle the item menu already writes, and the
             * details link. One set of wording, read off the same item.
             */}
            <div className="flex flex-wrap items-center gap-2">
              {playHref ? (
                <ViewTransitionLink
                  to={playHref}
                  aria-label={`${playLabel} ${displayTitle}`}
                  className="pill pill-primary"
                >
                  {item.type === "ebook" ? (
                    <BookOpen className="h-4 w-4" />
                  ) : (
                    <Play className="h-4 w-4 fill-current" />
                  )}
                  {playLabel}
                </ViewTransitionLink>
              ) : null}
              <button
                type="button"
                onClick={() => toggleWatchlist.mutate(inWatchlist)}
                disabled={toggleWatchlist.isPending}
                aria-pressed={inWatchlist}
                aria-label={
                  inWatchlist
                    ? `Remove ${displayTitle} from watchlist`
                    : `Add ${displayTitle} to watchlist`
                }
                className="pill pill-glass"
              >
                {inWatchlist ? <Check className="h-4 w-4" /> : <Plus className="h-4 w-4" />}
              </button>
              <ViewTransitionLink
                to={itemHref}
                aria-label={`More info about ${displayTitle}`}
                className="pill pill-glass"
              >
                <Info className="h-4 w-4" />
              </ViewTransitionLink>
            </div>
            {metadata.length > 0 ? (
              <div className="hero-meta-track text-white/85">
                {metadata.map((entry) =>
                  entry.rating ? (
                    <RatingEntry key={entry.key} rating={entry.rating} size="sm" />
                  ) : (
                    <span key={entry.key}>{entry.label}</span>
                  ),
                )}
              </div>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
});
