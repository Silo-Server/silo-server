import { useCallback, useEffect, useRef } from "react";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import {
  sendSessionProgress,
  observeSessionProgress,
  captureSessionProgress,
} from "../session-mutations";
import { playbackSessionEndReason, type PlaybackSessionEndReason } from "../player-fetch";
import { toMediaTime } from "../utils/mediaTimeline";

/**
 * Reports watch progress to the server every 10 seconds, and at once when
 * the video pauses or resumes, so the server's paused timeout never judges a
 * resume it has not heard of yet.
 * Reads currentTime and paused state from the video element.
 * An optional streamOriginRef maps player-local time back onto the original
 * media timeline for restarted HLS sessions.
 *
 * On cleanup (player unmount), sends one final progress report with
 * keepalive so the backend records the last known position even when
 * the user seeks and closes quickly.
 *
 * `onSessionEnded` runs, with the reason, when a report learns the server
 * ended the session for good: an administrator stopped it while the player
 * was unreachable, or it stayed paused too long.
 */
export function useWatchProgress(
  sessionId: string | null,
  videoRef: React.RefObject<HTMLVideoElement | null>,
  streamOriginRef?: React.RefObject<number>,
  onSessionEnded?: (reason: PlaybackSessionEndReason) => void,
) {
  const onSessionEndedRef = useRef(onSessionEnded);
  useEffect(() => {
    onSessionEndedRef.current = onSessionEnded;
  }, [onSessionEnded]);
  const config = usePlayerConfig();
  const configRef = useRef(config);
  configRef.current = config;
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  // Tells the player when a refused report shows the server ended the session.
  const reportSessionEnd = useCallback((error: unknown) => {
    const reason = playbackSessionEndReason(error);
    if (reason) onSessionEndedRef.current?.(reason);
  }, []);
  const skipCleanupReportRef = useRef(false);

  const getProgressSnapshot = useCallback(() => {
    const video = videoRef.current;
    // Before metadata (startup, or a transport swap) currentTime is not a
    // position on this stream yet; reporting it would overwrite the resume point.
    if (!video || video.readyState === HTMLMediaElement.HAVE_NOTHING) return null;

    return {
      position: toMediaTime(video.currentTime ?? 0, streamOriginRef?.current ?? 0),
      isPaused: video.paused ?? true,
    };
  }, [streamOriginRef, videoRef]);

  useEffect(() => {
    if (!sessionId) return;
    const read = () => {
      const sample = getProgressSnapshot();
      return sample ? { position: sample.position, is_paused: sample.isPaused } : null;
    };
    const forget = observeSessionProgress(sessionId, read);
    const capture = () => captureSessionProgress(sessionId, read());
    const video = videoRef.current;
    const events = ["timeupdate", "pause", "seeked", "ended"];
    capture();
    events.forEach((event) => video?.addEventListener(event, capture));
    return () => {
      events.forEach((event) => video?.removeEventListener(event, capture));
      forget();
    };
  }, [getProgressSnapshot, sessionId, videoRef]);

  const reportProgress = useCallback(
    async (options?: { keepalive?: boolean; isPaused?: boolean }) => {
      if (!sessionId) return;

      const snapshot = getProgressSnapshot();
      if (!snapshot) return;

      await sendSessionProgress(
        configRef.current,
        sessionId,
        {
          position: snapshot.position,
          is_paused: options?.isPaused ?? snapshot.isPaused,
        },
        options?.keepalive,
      );
    },
    [getProgressSnapshot, sessionId],
  );

  // The session whose reports are live; a flush still in flight when the
  // session changes must not end the replacement.
  const liveSessionRef = useRef<string | null>(null);

  const flushProgress = useCallback(async () => {
    if (!sessionId) return;

    try {
      await reportProgress({ isPaused: true });
    } catch (error) {
      // A leave flushes first. When the server already ended the session,
      // say so, as a periodic report would: a minimize then still leaves.
      if (liveSessionRef.current === sessionId) reportSessionEnd(error);
      throw error;
    }
    skipCleanupReportRef.current = true;
  }, [reportProgress, reportSessionEnd, sessionId]);

  useEffect(() => {
    if (!sessionId) return;
    skipCleanupReportRef.current = false;
    liveSessionRef.current = sessionId;
    // A report still in flight when the session changes must not end the
    // replacement session.
    let active = true;

    const report = () => {
      reportProgress().catch((error: unknown) => {
        // Best effort — don't disrupt playback on progress report failure,
        // unless the server says the session is over.
        if (active) reportSessionEnd(error);
      });
    };
    intervalRef.current = setInterval(report, 10_000);
    const video = videoRef.current;
    const pauseEvents = ["play", "pause"];
    pauseEvents.forEach((event) => video?.addEventListener(event, report));

    return () => {
      active = false;
      liveSessionRef.current = null;
      pauseEvents.forEach((event) => video?.removeEventListener(event, report));
      if (intervalRef.current) clearInterval(intervalRef.current);
      intervalRef.current = null;

      if (skipCleanupReportRef.current) return;

      // Send one final progress report so the backend has the latest
      // position before the session is deleted (e.g. after a seek).
      reportProgress({ keepalive: true, isPaused: true }).catch(() => {});
    };
  }, [reportProgress, reportSessionEnd, sessionId, videoRef]);

  return flushProgress;
}
