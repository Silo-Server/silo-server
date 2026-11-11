import "@/styles/player-hud.css";
import { useCallback, useEffect, useRef, useState } from "react";
import { motion } from "framer-motion";
import { ChevronLeft, ChevronRight, Play, Shuffle, X } from "lucide-react";
import type { EpisodeRef, PlaybackStartTrigger } from "../types";
import type { ContinueWatchingItem } from "@/hooks/queries/progress";
import { useAutoPlayNextSetting } from "@/hooks/queries/autoPlayNext";
import { decodeThumbhash } from "@/lib/thumbhash";
import { isEpisodeStill, isEpisodeUnwatched, SPOILER_IMAGE_CLASS } from "@/lib/episodeSpoilers";
import { useEpisodeSpoilerPrefs } from "@/hooks/useEpisodeSpoilerPrefs";
import { codeNumber, useT } from "@/i18n";
import { cn } from "@/lib/utils";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";
import { parseCalendarDate, preferredDateLocale } from "@/lib/datetime";
import { formatRuntimeMinutes } from "@/lib/mediaFormat";
import { useDateTimeFormat } from "@/hooks/useDateTimeFormat";
import MachineTranslatedLabel from "@/pages/ItemDetail/components/MachineTranslatedLabel";
import { hasMachineTranslation } from "@/pages/ItemDetail/components/machineTranslation";

/** Decorative separator between values; not text, so not translated. */
const SEPARATOR = "\u00b7";

interface PlayingNextScreenProps {
  seriesId?: string;
  seriesTitle?: string;
  nextEpisode?: EpisodeRef;
  continueWatchingItems: ContinueWatchingItem[];
  videoEnded: boolean;
  /** Plays the next episode: `viewer` from Play Now or Enter, `automatic` from the countdown. */
  onPlayNow?: (trigger: PlaybackStartTrigger) => void;
  onPlayItem: (contentId: string) => void;
  onClose: () => void;
  /**
   * Set while a shuffle plays: the next item is a random pick from the
   * scope this labels, and the viewer can pick another or stop shuffling.
   */
  shuffle?: {
    scopeLabel: string;
    onStop: () => void;
    onReshuffle: () => void;
  };
}

const COUNTDOWN_SECONDS = 10;

export function PlayingNextScreen({
  seriesId,
  seriesTitle,
  nextEpisode,
  continueWatchingItems,
  videoEnded,
  onPlayNow,
  onPlayItem,
  onClose,
  shuffle,
}: PlayingNextScreenProps) {
  const t = useT();
  useDateTimeFormat();
  // -- Auto-play setting --
  // Shared with Settings → Playback: both surfaces edit the same profile row,
  // so a choice made here is the one that screen shows and vice versa.
  const { enabled: autoplay, setEnabled: setAutoplay } = useAutoPlayNextSetting();
  const spoilerPrefs = useEpisodeSpoilerPrefs();

  const toggleAutoplay = useCallback(() => {
    void setAutoplay(!autoplay);
  }, [autoplay, setAutoplay]);

  // -- Countdown (only starts after video has ended) --
  const [secondsRemaining, setSecondsRemaining] = useState(COUNTDOWN_SECONDS);
  // A different next item, such as a shuffle's Pick Another, gets the full
  // countdown rather than whatever was left of the previous one.
  const [countdownFor, setCountdownFor] = useState(nextEpisode?.contentId);
  if (countdownFor !== nextEpisode?.contentId) {
    setCountdownFor(nextEpisode?.contentId);
    setSecondsRemaining(COUNTDOWN_SECONDS);
  }
  const countdownRef = useRef<ReturnType<typeof setInterval> | null>(null);
  // The countdown keys on the next item's ID: parents may rebuild an equal
  // nextEpisode object on every render, which must not restart the interval.
  const nextEpisodeId = nextEpisode?.contentId ?? null;
  const onPlayNowRef = useRef(onPlayNow);
  useEffect(() => {
    onPlayNowRef.current = onPlayNow;
  }, [onPlayNow]);

  useEffect(() => {
    if (!videoEnded || !autoplay || nextEpisodeId === null) {
      if (countdownRef.current) {
        clearInterval(countdownRef.current);
        countdownRef.current = null;
      }
      setSecondsRemaining(COUNTDOWN_SECONDS);
      return;
    }

    countdownRef.current = setInterval(() => {
      setSecondsRemaining((prev) => {
        if (prev <= 1) {
          if (countdownRef.current) clearInterval(countdownRef.current);
          onPlayNowRef.current?.("automatic");
          return 0;
        }
        return prev - 1;
      });
    }, 1000);

    return () => {
      if (countdownRef.current) clearInterval(countdownRef.current);
    };
  }, [videoEnded, autoplay, nextEpisodeId]);

  // -- Keyboard shortcuts --
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      } else if (e.key === "Enter" && onPlayNow) {
        // Enter on a focused control, such as Pick Another or Stop
        // shuffling, belongs to that control, not to Play Now.
        if (e.target instanceof Element && e.target.closest("button, a, input, select, textarea")) {
          return;
        }
        e.preventDefault();
        onPlayNow("viewer");
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [onClose, onPlayNow]);

  // -- On Deck items (filter out current series) --
  const onDeckItems = continueWatchingItems.filter(
    (item) => item.detail && (!seriesId || item.detail.series_id !== seriesId),
  );

  // -- Countdown ring SVG --
  const radius = 20;
  const circumference = 2 * Math.PI * radius;
  const progress = videoEnded && autoplay && nextEpisode ? secondsRemaining / COUNTDOWN_SECONDS : 0;
  const strokeDashoffset = circumference * (1 - progress);

  const nextAirDate = parseCalendarDate(nextEpisode?.airDate);
  const episodeStillUrl = nextEpisode?.stillUrl;
  const episodeThumbhash = nextEpisode?.stillThumbhash;
  const blurPlaceholder = episodeThumbhash ? decodeThumbhash(episodeThumbhash) : undefined;
  const nextUnwatched = !nextEpisode?.isMovie && isEpisodeUnwatched(nextEpisode?.watchState);
  const hideStill =
    spoilerPrefs.hideImages && nextUnwatched && isEpisodeStill(nextEpisode?.stillIsEpisodeStill);
  const hideOverview = spoilerPrefs.hideOverviews && nextUnwatched;
  const endOfSeriesHeading = seriesTitle
    ? t("web.player.upNext.finishedSeries", { series: seriesTitle })
    : t("upNext.endOfPlayback");

  return (
    <motion.div
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      transition={{ duration: 0.5 }}
      className="hud fixed inset-0 z-50 flex flex-col text-white"
    >
      {/* Atmospheric blurred background */}
      <div className="absolute inset-0 -z-10 overflow-hidden">
        {episodeStillUrl ? (
          <img
            src={episodeStillUrl}
            alt=""
            className="h-full w-full scale-110 object-cover blur-3xl brightness-[0.15] saturate-150"
          />
        ) : blurPlaceholder ? (
          <img
            src={blurPlaceholder}
            alt=""
            className="h-full w-full scale-110 object-cover blur-3xl brightness-[0.15] saturate-150"
          />
        ) : null}
        <div className="absolute inset-0 bg-black/60" />
      </div>

      {/* Close button */}
      <motion.button
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        transition={{ delay: 0.3 }}
        onClick={onClose}
        type="button"
        className="hud-glass-btn absolute top-[max(1rem,env(safe-area-inset-top))] right-[max(1rem,env(safe-area-inset-right))] z-10 sm:end-8 sm:top-6 rtl:right-auto rtl:left-[max(1rem,env(safe-area-inset-left))] sm:rtl:left-8"
        title={t("web.player.upNext.closeTitle")}
        aria-label={t("web.ui.close")}
      >
        <X />
      </motion.button>

      {/* Main content. `my-auto` on the inner block centers vertically when it
          fits and falls back to top-aligned + scrollable when content is too
          tall — avoids the `justify-center` trap that clips both ends. */}
      {/* Wide screens: the finishing episode (VideoPlayer's mini player) holds
          the start side, and this column is the one compact "up next" panel. */}
      <div className="flex min-h-0 flex-1 flex-col items-center overflow-y-auto px-4 py-4 sm:px-6 sm:py-6 md:px-8 lg:items-start lg:ps-[calc(var(--postroll-video-width)+6rem)] lg:pe-14">
        <motion.div
          initial={{ opacity: 0, y: 20 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.5, delay: 0.15 }}
          className="my-auto flex w-full max-w-[min(92vw,820px)] flex-col items-center lg:max-w-[34rem] lg:items-start"
        >
          {shuffle && (
            <div className="mb-3 inline-flex max-w-full items-center gap-1.5 rounded-full bg-white/10 px-3 py-1 text-xs font-semibold text-[var(--hud-paper)] ring-1 ring-white/10 backdrop-blur-sm">
              <Shuffle className="size-3.5 shrink-0" aria-hidden="true" />
              <span className="truncate">
                {t("upNext.shuffling", { scope: shuffle.scopeLabel })}
              </span>
            </div>
          )}
          {/* Label */}
          <div className="mb-3 text-xs font-semibold tracking-[0.18em] text-[var(--hud-lamp)] uppercase sm:mb-4 lg:mb-3">
            {nextEpisode
              ? shuffle
                ? t("web.player.upNext.headingShuffle")
                : t("web.player.upNext.heading")
              : t("upNext.headingFinished")}
          </div>

          {nextEpisode ? (
            <>
              {/* Episode still. Height cap is aggressive enough that hero + metadata
                  + actions typically fit above the On Deck row without scrolling. */}
              <div
                className="relative mb-3 w-full overflow-hidden rounded-xl shadow-[0_8px_60px_-12px_rgba(0,0,0,0.7)] ring-1 ring-white/[0.08] sm:mb-4 sm:rounded-2xl md:mb-5"
                style={{ aspectRatio: "16 / 9", maxHeight: "min(30vh, 300px)" }}
              >
                {episodeStillUrl ? (
                  <img
                    src={episodeStillUrl}
                    alt={nextEpisode.title}
                    className={cn("h-full w-full object-cover", hideStill && SPOILER_IMAGE_CLASS)}
                    style={
                      blurPlaceholder
                        ? { backgroundImage: `url(${blurPlaceholder})`, backgroundSize: "cover" }
                        : undefined
                    }
                  />
                ) : blurPlaceholder ? (
                  <img
                    src={blurPlaceholder}
                    alt=""
                    className={cn("h-full w-full object-cover", hideStill && SPOILER_IMAGE_CLASS)}
                  />
                ) : (
                  <div className="flex h-full w-full items-center justify-center bg-white/5 text-sm text-[var(--hud-ash)]">
                    {t("web.player.upNext.noPreview")}
                  </div>
                )}
              </div>

              {/* Episode metadata - centered */}
              <div className="flex flex-col items-center gap-1 text-center lg:items-start lg:text-start">
                {(seriesTitle || nextEpisode.episodeNumber <= 0) && (
                  <div className="text-xl font-semibold tracking-tight text-white sm:text-2xl">
                    {/* A movie has no series line; its own title heads the card. */}
                    {seriesTitle || nextEpisode.title}
                  </div>
                )}
                {/* A shuffled movie has no season or episode number. */}
                {nextEpisode.episodeNumber > 0 && (
                  <div className="flex flex-wrap items-center justify-center gap-x-2 gap-y-0.5 lg:justify-start">
                    <span className="text-sm font-medium text-[var(--hud-ash)] tabular-nums">
                      {t("web.player.episode.code", {
                        season: codeNumber(nextEpisode.seasonNumber),
                        episode: codeNumber(nextEpisode.episodeNumber),
                      })}
                    </span>
                    <span
                      aria-hidden="true"
                      className="hud-dot hidden text-[var(--hud-ash)] sm:inline-block"
                    />
                    <span className="text-base font-semibold">{nextEpisode.title}</span>
                  </div>
                )}
                <div className="flex items-center gap-2 text-sm text-[var(--hud-ash)]">
                  {nextAirDate && (
                    <span>
                      {nextAirDate.toLocaleDateString(preferredDateLocale(), {
                        year: "numeric",
                        month: "long",
                        day: "numeric",
                      })}
                    </span>
                  )}
                  {nextAirDate && nextEpisode.runtime > 0 && (
                    <span aria-hidden="true" className="hud-dot" />
                  )}
                  {nextEpisode.runtime > 0 && (
                    <span>{formatRuntimeMinutes(nextEpisode.runtime)}</span>
                  )}
                </div>
                {nextEpisode.overview && !hideOverview && (
                  <p
                    className="mt-1.5 line-clamp-2 max-w-xl text-sm leading-relaxed text-[var(--hud-ash)]"
                    title={nextEpisode.overview}
                  >
                    {nextEpisode.overview}
                  </p>
                )}
                {nextEpisode.overview &&
                  !hideOverview &&
                  hasMachineTranslation(nextEpisode.machineTranslatedFields) && (
                    <MachineTranslatedLabel className="mt-1" />
                  )}
              </div>

              {/* Actions: Play Now, then what happens if the viewer does nothing. */}
              <motion.div
                initial={{ opacity: 0, y: 10 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ duration: 0.4, delay: 0.3 }}
                className="mt-5 flex flex-wrap items-center justify-center gap-x-4 gap-y-3 lg:justify-start"
              >
                <button
                  onClick={() => onPlayNow?.("viewer")}
                  type="button"
                  className="hud-primary-btn min-h-14 px-9 text-base"
                >
                  <Play className="fill-current" />
                  {t("upNext.playNow")}
                </button>

                {shuffle && (
                  <button
                    onClick={shuffle.onReshuffle}
                    type="button"
                    className="hud-secondary-btn min-h-12 px-6"
                  >
                    <Shuffle className="h-4 w-4" />
                    {t("upNext.pickAnother")}
                  </button>
                )}

                {autoplay && (
                  <div className="flex items-center gap-2.5 text-sm text-[var(--hud-ash)]">
                    {videoEnded ? (
                      <>
                        <svg
                          width="40"
                          height="40"
                          viewBox="0 0 48 48"
                          className="-rotate-90"
                          aria-hidden="true"
                        >
                          <circle
                            cx="24"
                            cy="24"
                            r={radius}
                            fill="none"
                            stroke="rgba(255,255,255,0.12)"
                            strokeWidth="3"
                          />
                          <circle
                            cx="24"
                            cy="24"
                            r={radius}
                            fill="none"
                            stroke="currentColor"
                            strokeWidth="3"
                            strokeDasharray={circumference}
                            strokeDashoffset={strokeDashoffset}
                            strokeLinecap="round"
                            className="text-[var(--hud-lamp)] transition-all duration-1000 ease-linear"
                          />
                        </svg>
                        <span className="font-semibold text-[var(--hud-paper)] tabular-nums">
                          {t("web.player.upNext.startsIn", { count: secondsRemaining })}
                        </span>
                      </>
                    ) : (
                      <span>{t("web.player.upNext.startsAfterCredits")}</span>
                    )}
                  </div>
                )}
              </motion.div>

              <div className="mt-3 flex flex-wrap items-center justify-center gap-x-2 text-sm text-[var(--hud-ash)] lg:justify-start">
                <button
                  onClick={toggleAutoplay}
                  type="button"
                  role="switch"
                  aria-checked={autoplay}
                  className="-ms-3 flex min-h-11 items-center gap-3 rounded-full px-3 transition-colors hover:text-[var(--hud-paper)]"
                >
                  {t("web.player.upNext.autoplay")}
                  <span aria-hidden="true" className="hud-switch" />
                </button>
                {shuffle && (
                  <>
                    <span aria-hidden="true">{SEPARATOR}</span>
                    <button
                      onClick={shuffle.onStop}
                      type="button"
                      className="min-h-11 rounded-full px-3 transition-colors hover:text-[var(--hud-paper)]"
                    >
                      {t("upNext.stopShuffling")}
                    </button>
                  </>
                )}
              </div>
            </>
          ) : (
            <>
              <div className="text-center text-xl font-bold text-white sm:text-2xl">
                {endOfSeriesHeading}
              </div>
              <div className="mt-2 max-w-md text-center text-sm text-[var(--hud-ash)]">
                {t("web.player.upNext.noMoreEpisodes")}
              </div>
              <button onClick={onClose} type="button" className="hud-secondary-btn mt-6">
                {t("common.back")}
              </button>
            </>
          )}
          {/* On Deck follows the next item in the same column, so the screen
              reads as one group rather than a header and a footer. */}
          {onDeckItems.length > 0 && <OnDeckCarousel items={onDeckItems} onPlayItem={onPlayItem} />}
        </motion.div>
      </div>

      {/* Bottom spacer */}
      <div className="h-3 shrink-0 sm:h-4" />
    </motion.div>
  );
}

// ── On Deck Carousel ────────────────────────────────────────

function OnDeckCarousel({
  items,
  onPlayItem,
}: {
  items: ContinueWatchingItem[];
  onPlayItem: (contentId: string) => void;
}) {
  const t = useT();
  const { emblaRef, canScrollPrev, canScrollNext, scrollPrev, scrollNext } = useCarouselEmbla({
    options: { slidesToScroll: 3, align: "start" },
  });

  return (
    <motion.div
      initial={{ opacity: 0, y: 16 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.4, delay: 0.45 }}
      className="mt-10 w-full shrink-0 pb-3 sm:mt-12"
    >
      <div className="mb-2 flex items-center justify-between sm:mb-3">
        <h3 className="text-xs font-semibold tracking-[0.12em] text-[var(--hud-ash)] uppercase">
          {t("upNext.onDeck")}
        </h3>
        {/* Arrows only when there is somewhere to scroll. */}
        <div className={`flex gap-1 ${canScrollPrev || canScrollNext ? "" : "hidden"}`}>
          <button
            onClick={scrollPrev}
            type="button"
            disabled={!canScrollPrev}
            aria-label={t("web.player.upNext.onDeckScrollBack")}
            className="hud-icon-btn disabled:opacity-35"
          >
            <ChevronLeft />
          </button>
          <button
            onClick={scrollNext}
            type="button"
            disabled={!canScrollNext}
            aria-label={t("web.player.upNext.onDeckScrollForward")}
            className="hud-icon-btn disabled:opacity-35"
          >
            <ChevronRight />
          </button>
        </div>
      </div>

      <div ref={emblaRef} className="overflow-hidden">
        <div className="-ms-2 flex sm:-ms-3">
          {items.map((item) => {
            const detail = item.detail;
            if (!detail) return null;

            const contentId = detail.content_id;
            const title = detail.series_title ?? detail.title;
            const hasEpisodeNumber = detail.season_number != null && detail.episode_number != null;
            const episodeCode = {
              season: String(detail.season_number),
              episode: String(detail.episode_number),
            };
            const episodeMeta = !hasEpisodeNumber
              ? null
              : detail.series_title && detail.title
                ? t("web.player.episode.codeWithTitle", { ...episodeCode, title: detail.title })
                : t("web.player.episode.code", episodeCode);
            const thumbnailUrl = detail.backdrop_url ?? detail.poster_url;
            const progressPercent =
              item.progress.duration_seconds > 0
                ? (item.progress.position_seconds / item.progress.duration_seconds) * 100
                : 0;
            const timeLeft =
              item.progress.duration_seconds > 0
                ? Math.round((item.progress.duration_seconds - item.progress.position_seconds) / 60)
                : 0;

            return (
              <div
                key={contentId}
                className="min-w-0 shrink-0 basis-3/4 ps-2 sm:basis-1/2 sm:ps-3 md:basis-1/3"
              >
                <button
                  onClick={() => onPlayItem(contentId)}
                  type="button"
                  className="group w-full text-start"
                >
                  <div className="relative aspect-video overflow-hidden rounded-lg ring-1 ring-white/[0.06]">
                    {thumbnailUrl ? (
                      <img
                        src={thumbnailUrl}
                        alt={title}
                        className="h-full w-full object-cover transition-transform duration-200 group-hover:scale-105"
                        loading="lazy"
                      />
                    ) : (
                      <div className="flex h-full w-full items-center justify-center bg-white/5 text-xs text-[var(--hud-ash)]">
                        {t("web.player.upNext.noImage")}
                      </div>
                    )}
                    {/* Play overlay */}
                    <div className="absolute inset-0 flex items-center justify-center bg-black/0 transition-colors group-hover:bg-black/30">
                      <div className="flex h-9 w-9 items-center justify-center rounded-full bg-[var(--hud-paper)] text-[var(--hud-ink)] opacity-0 shadow-lg transition-opacity group-hover:opacity-100">
                        {/* eslint-disable-next-line silo/no-physical-direction -- centres the play triangle, which doesn't mirror */}
                        <Play className="ml-0.5 h-3.5 w-3.5" fill="currentColor" />
                      </div>
                    </div>
                    {/* Progress bar */}
                    {progressPercent > 0 && (
                      <div className="absolute inset-x-0 bottom-0 h-[3px] bg-white/10">
                        <div
                          className="h-full bg-[var(--hud-lamp)]"
                          style={{ width: `${Math.min(progressPercent, 100)}%` }}
                        />
                      </div>
                    )}
                  </div>
                  <div className="mt-1.5 space-y-0.5">
                    <div className="truncate text-sm font-semibold" title={title}>
                      {title}
                    </div>
                    {episodeMeta && (
                      <div className="truncate text-xs text-[var(--hud-ash)]" title={episodeMeta}>
                        {episodeMeta}
                      </div>
                    )}
                    {timeLeft > 0 && (
                      <div className="text-xs text-[var(--hud-ash)]">
                        {t("web.player.upNext.minutesLeft", {
                          count: timeLeft,
                          minutes: timeLeft,
                        })}
                      </div>
                    )}
                  </div>
                </button>
              </div>
            );
          })}
        </div>
      </div>
    </motion.div>
  );
}
