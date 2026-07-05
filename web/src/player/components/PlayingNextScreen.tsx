import { useCallback, useEffect, useRef, useState } from "react";
import { motion } from "framer-motion";
import { ChevronLeft, ChevronRight, Play, X } from "lucide-react";
import type { EpisodeRef } from "../types";
import type { ContinueWatchingItem } from "@/hooks/queries/progress";
import { useEffectiveSettings, useSetDeviceSetting } from "@/hooks/queries/settings";
import { decodeThumbhash } from "@/lib/thumbhash";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { preferredDateLocale } from "@/lib/datetime";
import { useDateTimeFormat } from "@/hooks/useDateTimeFormat";

interface PlayingNextScreenProps {
  seriesId?: string;
  seriesTitle?: string;
  nextEpisode?: EpisodeRef;
  continueWatchingItems: ContinueWatchingItem[];
  videoEnded: boolean;
  onPlayNow?: () => void;
  onPlayItem: (contentId: string) => void;
  onClose: () => void;
}

const COUNTDOWN_SECONDS = 10;
const AUTOPLAY_SETTING_KEY = "playback.auto_play_next";

export function PlayingNextScreen({
  seriesId,
  seriesTitle,
  nextEpisode,
  continueWatchingItems,
  videoEnded,
  onPlayNow,
  onPlayItem,
  onClose,
}: PlayingNextScreenProps) {
  useDateTimeFormat();
  // -- Auto-play setting --
  const { profile } = useCurrentProfile();
  const { data: effectiveSettings } = useEffectiveSettings(profile?.id, [AUTOPLAY_SETTING_KEY]);
  const setDeviceSetting = useSetDeviceSetting();
  const autoplay = effectiveSettings?.[AUTOPLAY_SETTING_KEY]?.effective_value !== "false";

  const toggleAutoplay = useCallback(() => {
    const newValue = autoplay ? "false" : "true";
    setDeviceSetting.mutate({ key: AUTOPLAY_SETTING_KEY, value: newValue });
  }, [autoplay, setDeviceSetting]);

  // -- Countdown (only starts after video has ended) --
  const [secondsRemaining, setSecondsRemaining] = useState(COUNTDOWN_SECONDS);
  const countdownRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const onPlayNowRef = useRef(onPlayNow);
  useEffect(() => {
    onPlayNowRef.current = onPlayNow;
  }, [onPlayNow]);

  useEffect(() => {
    if (!videoEnded || !autoplay || !nextEpisode) {
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
          onPlayNowRef.current?.();
          return 0;
        }
        return prev - 1;
      });
    }, 1000);

    return () => {
      if (countdownRef.current) clearInterval(countdownRef.current);
    };
  }, [videoEnded, autoplay, nextEpisode]);

  // -- Keyboard shortcuts --
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      } else if (e.key === "Enter" && onPlayNow) {
        e.preventDefault();
        onPlayNow();
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

  const episodeStillUrl = nextEpisode?.stillUrl;
  const episodeThumbhash = nextEpisode?.stillThumbhash;
  const blurPlaceholder = episodeThumbhash ? decodeThumbhash(episodeThumbhash) : undefined;
  const endOfSeriesHeading = seriesTitle ? `You've finished ${seriesTitle}` : "End of playback";

  return (
    <motion.div
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      transition={{ duration: 0.5 }}
      className="fixed inset-0 z-50 flex flex-col text-white"
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
        className="absolute top-3 right-3 z-10 flex h-9 w-9 items-center justify-center rounded-full bg-white/10 backdrop-blur-sm transition-colors hover:bg-white/20 sm:top-5 sm:right-5 sm:h-10 sm:w-10"
        title="Close (Esc)"
      >
        <X className="h-4 w-4 sm:h-5 sm:w-5" />
      </motion.button>

      {/* Main content. `my-auto` on the inner block centers vertically when it
          fits and falls back to top-aligned + scrollable when content is too
          tall — avoids the `justify-center` trap that clips both ends. */}
      <div className="flex min-h-0 flex-1 flex-col items-center overflow-y-auto px-4 py-4 sm:px-6 sm:py-6 md:px-8">
        <motion.div
          initial={{ opacity: 0, y: 20 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.5, delay: 0.15 }}
          className="my-auto flex w-full flex-col items-center"
          style={{ maxWidth: "min(92vw, 820px)" }}
        >
          {/* Label */}
          <div className="mb-2.5 text-[10px] font-semibold tracking-[0.25em] text-white/40 uppercase sm:mb-3 sm:text-[11px]">
            {nextEpisode ? "Playing Next" : "Finished"}
          </div>

          {nextEpisode ? (
            <>
              {/* Episode still. Height cap is aggressive enough that hero + metadata
                  + actions typically fit above the On Deck row without scrolling. */}
              <div
                className="relative mb-3 w-full overflow-hidden rounded-xl shadow-[0_8px_60px_-12px_rgba(0,0,0,0.7)] ring-1 ring-white/[0.08] sm:mb-4 sm:rounded-2xl md:mb-5"
                style={{ aspectRatio: "16 / 9", maxHeight: "min(38vh, 400px)" }}
              >
                {episodeStillUrl ? (
                  <img
                    src={episodeStillUrl}
                    alt={nextEpisode.title}
                    className="h-full w-full object-cover"
                    style={
                      blurPlaceholder
                        ? { backgroundImage: `url(${blurPlaceholder})`, backgroundSize: "cover" }
                        : undefined
                    }
                  />
                ) : blurPlaceholder ? (
                  <img src={blurPlaceholder} alt="" className="h-full w-full object-cover" />
                ) : (
                  <div className="flex h-full w-full items-center justify-center bg-white/5 text-sm text-white/30">
                    No Preview
                  </div>
                )}
              </div>

              {/* Episode metadata - centered */}
              <div className="flex flex-col items-center gap-1 text-center">
                {seriesTitle && (
                  <div className="text-base font-bold text-white sm:text-lg">{seriesTitle}</div>
                )}
                <div className="flex flex-wrap items-center justify-center gap-x-2 gap-y-0.5">
                  <span className="text-xs font-medium text-white/60 sm:text-sm">
                    S{nextEpisode.seasonNumber}:E{nextEpisode.episodeNumber}
                  </span>
                  <span className="hidden text-sm text-white/25 sm:inline">&mdash;</span>
                  <span className="text-sm font-semibold sm:text-base">{nextEpisode.title}</span>
                </div>
                <div className="flex items-center gap-2 text-[11px] text-white/40 sm:text-xs">
                  {nextEpisode.airDate && (
                    <span>
                      {new Date(nextEpisode.airDate).toLocaleDateString(preferredDateLocale(), {
                        year: "numeric",
                        month: "long",
                        day: "numeric",
                      })}
                    </span>
                  )}
                  {nextEpisode.airDate && nextEpisode.runtime > 0 && (
                    <span className="text-white/20">&bull;</span>
                  )}
                  {nextEpisode.runtime > 0 && (
                    <span>{Math.round(nextEpisode.runtime / 60)} min</span>
                  )}
                </div>
                {nextEpisode.overview && (
                  <p className="mt-1 line-clamp-2 max-w-xl text-xs leading-relaxed text-white/50 sm:text-sm">
                    {nextEpisode.overview}
                  </p>
                )}
              </div>

              {/* Actions */}
              <motion.div
                initial={{ opacity: 0, y: 10 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ duration: 0.4, delay: 0.3 }}
                className="mt-3 flex items-center gap-3 sm:mt-4 sm:gap-4"
              >
                <button
                  onClick={onPlayNow}
                  type="button"
                  className="bg-primary text-primary-foreground flex items-center gap-2 rounded-full px-5 py-2.5 text-sm font-semibold shadow-lg transition-all hover:scale-105 hover:shadow-xl sm:px-7 sm:py-3"
                >
                  <Play className="h-4 w-4 fill-current" />
                  Play Now
                </button>

                {videoEnded && autoplay && (
                  <motion.div
                    initial={{ opacity: 0, scale: 0.8 }}
                    animate={{ opacity: 1, scale: 1 }}
                    className="flex items-center gap-2"
                  >
                    <svg width="48" height="48" viewBox="0 0 48 48" className="-rotate-90">
                      <circle
                        cx="24"
                        cy="24"
                        r={radius}
                        fill="none"
                        stroke="rgba(255,255,255,0.08)"
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
                        className="text-primary transition-all duration-1000 ease-linear"
                      />
                    </svg>
                    <span className="text-sm text-white/50 tabular-nums">{secondsRemaining}s</span>
                  </motion.div>
                )}
              </motion.div>

              {/* Auto-play toggle */}
              <button
                onClick={toggleAutoplay}
                type="button"
                className="mt-2 text-xs text-white/30 transition-colors hover:text-white/60 sm:mt-2.5"
              >
                Auto-play is {autoplay ? "on" : "off"}
              </button>
            </>
          ) : (
            <>
              <div className="text-center text-xl font-bold text-white sm:text-2xl">
                {endOfSeriesHeading}
              </div>
              <div className="mt-2 max-w-md text-center text-sm text-white/50">
                There are no more episodes available. Pick something else from On Deck below.
              </div>
              <button
                onClick={onClose}
                type="button"
                className="mt-5 flex items-center gap-2 rounded-full bg-white/10 px-5 py-2.5 text-sm font-semibold text-white shadow-lg transition-colors hover:bg-white/20 sm:mt-6 sm:px-7 sm:py-3"
              >
                Back
              </button>
            </>
          )}
        </motion.div>
      </div>

      {/* On Deck section */}
      {onDeckItems.length > 0 && <OnDeckCarousel items={onDeckItems} onPlayItem={onPlayItem} />}

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
  const { emblaRef, canScrollPrev, canScrollNext, scrollPrev, scrollNext } = useCarouselEmbla({
    options: { slidesToScroll: 3, align: "start" },
  });

  return (
    <motion.div
      initial={{ opacity: 0, y: 16 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.4, delay: 0.45 }}
      className="shrink-0 px-4 pb-3 sm:px-6 sm:pb-4 md:px-8"
    >
      <div className="mb-2 flex items-center justify-between sm:mb-3">
        <h3 className="text-[10px] font-semibold tracking-wider text-white/40 uppercase sm:text-xs">
          On Deck
        </h3>
        <div className="flex gap-1">
          <button
            onClick={scrollPrev}
            type="button"
            disabled={!canScrollPrev}
            className="flex h-7 w-7 items-center justify-center rounded-full bg-white/10 text-white/60 transition-colors hover:bg-white/20 disabled:opacity-30"
          >
            <ChevronLeft className="h-4 w-4" />
          </button>
          <button
            onClick={scrollNext}
            type="button"
            disabled={!canScrollNext}
            className="flex h-7 w-7 items-center justify-center rounded-full bg-white/10 text-white/60 transition-colors hover:bg-white/20 disabled:opacity-30"
          >
            <ChevronRight className="h-4 w-4" />
          </button>
        </div>
      </div>

      <div ref={emblaRef} className="overflow-hidden">
        <div className="-ml-2 flex sm:-ml-3">
          {items.map((item) => {
            const detail = item.detail;
            if (!detail) return null;

            const contentId = detail.content_id;
            const title = detail.series_title ?? detail.title;
            const episodeMeta =
              detail.season_number != null && detail.episode_number != null
                ? `S${detail.season_number}:E${detail.episode_number}`
                : null;
            const episodeTitle = episodeMeta && detail.series_title ? detail.title : null;
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
                className="min-w-0 shrink-0 basis-1/2 pl-2 sm:basis-1/3 sm:pl-3 md:basis-1/4 lg:basis-1/5 xl:basis-1/6 2xl:basis-[14.2857%]"
              >
                <button
                  onClick={() => onPlayItem(contentId)}
                  type="button"
                  className="group w-full text-left"
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
                      <div className="flex h-full w-full items-center justify-center bg-white/5 text-xs text-white/30">
                        No Image
                      </div>
                    )}
                    {/* Play overlay */}
                    <div className="absolute inset-0 flex items-center justify-center bg-black/0 transition-colors group-hover:bg-black/30">
                      <div className="bg-primary text-primary-foreground flex h-8 w-8 items-center justify-center rounded-full opacity-0 shadow-lg transition-opacity group-hover:opacity-100">
                        <Play className="ml-0.5 h-3.5 w-3.5" fill="currentColor" />
                      </div>
                    </div>
                    {/* Progress bar */}
                    {progressPercent > 0 && (
                      <div className="absolute inset-x-0 bottom-0 h-[3px] bg-white/10">
                        <div
                          className="bg-primary h-full"
                          style={{ width: `${Math.min(progressPercent, 100)}%` }}
                        />
                      </div>
                    )}
                  </div>
                  <div className="mt-1.5 space-y-0.5">
                    <div className="truncate text-xs font-semibold text-white/80">{title}</div>
                    {episodeMeta && (
                      <div className="truncate text-[11px] text-white/40">
                        {episodeMeta}
                        {episodeTitle ? ` \u00b7 ${episodeTitle}` : ""}
                      </div>
                    )}
                    {timeLeft > 0 && (
                      <div className="text-[11px] text-white/30">{timeLeft} min left</div>
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
