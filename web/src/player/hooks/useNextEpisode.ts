import { useCallback, useEffect, useRef, useState } from "react";
import { isStillWatchingGated } from "../stillWatching";
import type { EpisodeRef, PlaybackStartTrigger, PlayerTimeRange, SeriesContext } from "../types";

interface NextEpisodeOptions {
  /** The viewer's Auto-play next episode setting. Off, the card waits for them. */
  autoPlay: boolean;
  /** Whether the video is playing. A paused viewer's countdown holds. */
  playing: boolean;
}

interface NextEpisodeState {
  /** Whether the Up next card is up. */
  showCountdown: boolean;
  /** Seconds left before the automatic start, or null when nothing counts down. */
  secondsRemaining: number | null;
  nextEpisode: EpisodeRef | null;
  skipToNext: () => void;
  cancelAutoPlay: () => void;
}

/** Seconds the next-episode countdown runs. */
export const NEXT_EPISODE_COUNTDOWN_SECONDS = 10;

/**
 * Detects when the current playback position enters the configured trigger region,
 * starts a 10-second countdown, and provides the next episode reference. The
 * countdown running out is an automatic start; skipping it is the viewer's.
 *
 * With Auto-play next episode off the card still opens but nothing counts down,
 * as on the post-roll screen. The countdown holds while the video is paused.
 */
export function useNextEpisode(
  triggerRegion: PlayerTimeRange | null,
  seriesContext: SeriesContext | undefined,
  currentTime: number,
  onNavigate: (contentId: string, trigger: PlaybackStartTrigger) => void,
  { autoPlay, playing }: NextEpisodeOptions,
): NextEpisodeState {
  const [showCountdown, setShowCountdown] = useState(false);
  const [secondsRemaining, setSecondsRemaining] = useState(NEXT_EPISODE_COUNTDOWN_SECONDS);
  const cancelledRef = useRef(false);
  const countdownRef = useRef<ReturnType<typeof setInterval> | null>(null);
  // Set once this card has started the next episode, so a pause and play while
  // that episode loads cannot start it a second time. Only a newly opened card
  // clears it: the episode change itself must not, or the stale 0 from the
  // previous episode would start the one after it.
  const startedRef = useRef(false);

  // Find the next episode.
  const nextEpisode = seriesContext ? findNextEpisode(seriesContext) : null;

  // Reset cancelled state when the episode changes so the overlay works for every episode.
  const currentEpisodeKey = seriesContext
    ? `${seriesContext.currentSeason}-${seriesContext.currentEpisode}`
    : null;
  useEffect(() => {
    cancelledRef.current = false;
    setShowCountdown(false);
    setSecondsRemaining(NEXT_EPISODE_COUNTDOWN_SECONDS);
    if (countdownRef.current) {
      clearInterval(countdownRef.current);
      countdownRef.current = null;
    }
  }, [currentEpisodeKey]);

  // Detect entry into the configured trigger region.
  useEffect(() => {
    if (
      !triggerRegion ||
      currentTime < triggerRegion.start ||
      currentTime >= triggerRegion.end ||
      !nextEpisode ||
      cancelledRef.current ||
      isStillWatchingGated()
    ) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- the playback position closes the card, which ends its countdown
      if (showCountdown) setShowCountdown(false);
      return;
    }

    if (!showCountdown) {
      startedRef.current = false;
      setShowCountdown(true);
      setSecondsRemaining(NEXT_EPISODE_COUNTDOWN_SECONDS);
    }
  }, [currentTime, triggerRegion, nextEpisode, showCountdown]);

  // Count down only while the card is up, auto-play is on, and the video plays.
  // A pause clears the interval and keeps the seconds left; playing again picks
  // the count up from there.
  const counting = showCountdown && autoPlay && playing;
  useEffect(() => {
    if (!counting) return;
    countdownRef.current = setInterval(() => {
      setSecondsRemaining((prev) => Math.max(0, prev - 1));
    }, 1000);
    return () => {
      if (countdownRef.current) clearInterval(countdownRef.current);
      countdownRef.current = null;
    };
  }, [counting]);

  // The count reaching 0 is the automatic start. It runs here rather than in
  // the state updater, which React may call twice.
  const onNavigateRef = useRef(onNavigate);
  useEffect(() => {
    onNavigateRef.current = onNavigate;
  }, [onNavigate]);
  const nextEpisodeId = nextEpisode?.contentId ?? null;
  useEffect(() => {
    if (!counting || secondsRemaining > 0 || nextEpisodeId === null || startedRef.current) return;
    startedRef.current = true;
    if (countdownRef.current) clearInterval(countdownRef.current);
    countdownRef.current = null;
    onNavigateRef.current(nextEpisodeId, "automatic");
  }, [counting, secondsRemaining, nextEpisodeId]);

  // Clean up interval on unmount.
  useEffect(() => {
    return () => {
      if (countdownRef.current) clearInterval(countdownRef.current);
    };
  }, []);

  const skipToNext = useCallback(() => {
    startedRef.current = true;
    if (countdownRef.current) clearInterval(countdownRef.current);
    if (nextEpisode) onNavigate(nextEpisode.contentId, "viewer");
  }, [nextEpisode, onNavigate]);

  const cancelAutoPlay = useCallback(() => {
    cancelledRef.current = true;
    if (countdownRef.current) clearInterval(countdownRef.current);
    setShowCountdown(false);
  }, []);

  return {
    showCountdown,
    secondsRemaining: autoPlay ? secondsRemaining : null,
    nextEpisode,
    skipToNext,
    cancelAutoPlay,
  };
}

function findNextEpisode(ctx: SeriesContext): EpisodeRef | null {
  const idx = ctx.episodes.findIndex(
    (ep) => ep.seasonNumber === ctx.currentSeason && ep.episodeNumber === ctx.currentEpisode,
  );
  if (idx < 0 || idx >= ctx.episodes.length - 1) return null;
  return ctx.episodes[idx + 1] ?? null;
}
