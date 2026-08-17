import { useCallback, useEffect, useRef, useState } from "react";

import type { IntroSkipMode, PlayerTimeRange } from "../types";

export const INTRO_PROMPT_SECONDS = 5;
export const PLAYBACK_PAUSE_GRACE_MS = 1_500;

const INTRO_PROMPT_MS = INTRO_PROMPT_SECONDS * 1_000;

export interface IntroSkipPrompt {
  kind: "skip" | "undo";
  label: "Skip Intro" | "Watch Intro";
  /** Confirmation shown above the action for the `always` undo; absent for the `ask` offer. */
  caption?: "Intro skipped";
  durationMs: number;
  deadlineMs: number | null;
  remainingMs: number;
}

interface ActivePrompt {
  key: string;
  kind: IntroSkipPrompt["kind"];
  deadlineMs: number | null;
  remainingMs: number;
}

interface UseIntroSkipPromptOptions {
  mode: IntroSkipMode;
  intro: PlayerTimeRange | null;
  introKey: string | null;
  currentTime: number;
  playing: boolean;
  enabled: boolean;
  onSeek: (seconds: number) => void;
}

/**
 * Owns the intro prompt clock and the per-playback resolution state.
 *
 * The clock is based on Date.now rather than CSS animation time, so reduced
 * motion and animation-duration scaling cannot change when an action occurs.
 */
export function useIntroSkipPrompt({
  mode,
  intro,
  introKey,
  currentTime,
  playing,
  enabled,
  onSeek,
}: UseIntroSkipPromptOptions) {
  const [activePrompt, setActivePromptState] = useState<ActivePrompt | null>(null);
  const activePromptRef = useRef<ActivePrompt | null>(null);
  const resolvedKeysRef = useRef(new Set<string>());
  const contextRef = useRef<string | null>(null);
  const wasInsideRef = useRef(false);
  const playingRef = useRef(playing);
  const onSeekRef = useRef(onSeek);
  const deadlineRef = useRef(0);
  const pausedRemainingRef = useRef<number | null>(null);
  const expiryTimerRef = useRef<number | null>(null);
  const pauseGraceTimerRef = useRef<number | null>(null);
  const expirePromptRef = useRef<() => void>(() => {});

  useEffect(() => {
    playingRef.current = playing;
  }, [playing]);

  useEffect(() => {
    onSeekRef.current = onSeek;
  }, [onSeek]);

  const replacePrompt = useCallback((next: ActivePrompt | null) => {
    activePromptRef.current = next;
    setActivePromptState(next);
  }, []);

  const clearCountdownTimers = useCallback(() => {
    if (expiryTimerRef.current !== null) {
      window.clearTimeout(expiryTimerRef.current);
      expiryTimerRef.current = null;
    }
  }, []);

  const clearPauseGraceTimer = useCallback(() => {
    if (pauseGraceTimerRef.current !== null) {
      window.clearTimeout(pauseGraceTimerRef.current);
      pauseGraceTimerRef.current = null;
    }
  }, []);

  const clearPrompt = useCallback(() => {
    clearCountdownTimers();
    clearPauseGraceTimer();
    deadlineRef.current = 0;
    pausedRemainingRef.current = null;
    replacePrompt(null);
  }, [clearCountdownTimers, clearPauseGraceTimer, replacePrompt]);

  const scheduleCountdown = useCallback(
    (remainingMs: number) => {
      clearCountdownTimers();
      const boundedRemaining = Math.max(0, remainingMs);
      deadlineRef.current = Date.now() + boundedRemaining;
      const current = activePromptRef.current;
      if (current) {
        replacePrompt({
          ...current,
          deadlineMs: deadlineRef.current,
          remainingMs: boundedRemaining,
        });
      }

      expiryTimerRef.current = window.setTimeout(() => expirePromptRef.current(), boundedRemaining);
    },
    [clearCountdownTimers, replacePrompt],
  );

  const expirePrompt = useCallback(() => {
    const current = activePromptRef.current;
    if (!current) return;
    if (current.kind === "undo") {
      resolvedKeysRef.current.add(current.key);
    }
    clearPrompt();
  }, [clearPrompt]);
  useEffect(() => {
    expirePromptRef.current = expirePrompt;
  }, [expirePrompt]);

  const startPrompt = useCallback(
    (kind: ActivePrompt["kind"], key: string) => {
      clearCountdownTimers();
      clearPauseGraceTimer();
      const next = { key, kind, deadlineMs: null, remainingMs: INTRO_PROMPT_MS };
      replacePrompt(next);
      if (playingRef.current) {
        pausedRemainingRef.current = null;
        scheduleCountdown(INTRO_PROMPT_MS);
      } else {
        // A prompt reached while already paused waits at a full clock. The
        // grace period applies to false edges during playback, not startup.
        pausedRemainingRef.current = INTRO_PROMPT_MS;
        deadlineRef.current = 0;
      }
    },
    [clearCountdownTimers, clearPauseGraceTimer, replacePrompt, scheduleCountdown],
  );

  useEffect(() => {
    const current = activePromptRef.current;
    if (!current) {
      clearPauseGraceTimer();
      return;
    }

    if (playing) {
      clearPauseGraceTimer();
      const pausedRemaining = pausedRemainingRef.current;
      if (pausedRemaining !== null) {
        pausedRemainingRef.current = null;
        scheduleCountdown(pausedRemaining);
      }
      return;
    }

    if (pausedRemainingRef.current !== null || pauseGraceTimerRef.current !== null) {
      return;
    }

    pauseGraceTimerRef.current = window.setTimeout(() => {
      pauseGraceTimerRef.current = null;
      if (playingRef.current || !activePromptRef.current) return;
      const remaining = Math.max(0, deadlineRef.current - Date.now());
      clearCountdownTimers();
      pausedRemainingRef.current = remaining;
      replacePrompt({ ...activePromptRef.current, deadlineMs: null, remainingMs: remaining });
    }, PLAYBACK_PAUSE_GRACE_MS);
  }, [
    activePrompt?.key,
    clearCountdownTimers,
    clearPauseGraceTimer,
    playing,
    replacePrompt,
    scheduleCountdown,
  ]);

  /* eslint-disable react-hooks/set-state-in-effect -- Playback position and mode changes are the
   * external events this hook translates into prompt state. The updates cannot be derived during
   * render because they also own timers, per-intro resolution, and one-shot seek effects. */
  useEffect(() => {
    const context = introKey && intro ? `${introKey}:${mode}` : null;
    if (contextRef.current !== context) {
      contextRef.current = context;
      wasInsideRef.current = false;
      clearPrompt();
    }

    const inside = intro !== null && currentTime >= intro.start && currentTime < intro.end;
    if (!enabled || mode === "never" || !intro || !introKey) {
      wasInsideRef.current = false;
      clearPrompt();
      return;
    }

    if (!inside) {
      wasInsideRef.current = false;
      const current = activePromptRef.current;
      // The undo prompt intentionally survives the automatic seek out of the
      // intro. The ask prompt does not survive a viewer seek out.
      if (current?.key === introKey && current.kind === "skip") {
        clearPrompt();
      }
      return;
    }

    if (wasInsideRef.current) return;
    wasInsideRef.current = true;
    if (resolvedKeysRef.current.has(introKey)) return;

    if (mode === "ask") {
      startPrompt("skip", introKey);
      return;
    }

    // Start the undo clock before seeking. The resulting position update is
    // outside the range, but the undo prompt must remain available.
    startPrompt("undo", introKey);
    onSeekRef.current(intro.end);
  }, [clearPrompt, currentTime, enabled, intro, introKey, mode, startPrompt]);
  /* eslint-enable react-hooks/set-state-in-effect */

  useEffect(
    () => () => {
      clearCountdownTimers();
      clearPauseGraceTimer();
    },
    [clearCountdownTimers, clearPauseGraceTimer],
  );

  const select = useCallback(() => {
    const current = activePromptRef.current;
    if (!current || !intro) return false;
    resolvedKeysRef.current.add(current.key);
    clearPrompt();
    onSeekRef.current(current.kind === "skip" ? intro.end : intro.start);
    return true;
  }, [clearPrompt, intro]);

  const dismiss = useCallback(() => {
    const current = activePromptRef.current;
    if (!current) return false;
    resolvedKeysRef.current.add(current.key);
    clearPrompt();
    return true;
  }, [clearPrompt]);

  const prompt: IntroSkipPrompt | null = activePrompt
    ? {
        kind: activePrompt.kind,
        label: activePrompt.kind === "skip" ? "Skip Intro" : "Watch Intro",
        caption: activePrompt.kind === "skip" ? undefined : "Intro skipped",
        durationMs: INTRO_PROMPT_MS,
        deadlineMs: activePrompt.deadlineMs,
        remainingMs: activePrompt.remainingMs,
      }
    : null;

  return { prompt, select, dismiss };
}
