import { useCallback, useEffect, useRef, useState } from "react";

import type { IntroSkipMode, PlayerTimeRange } from "../types";

export const INTRO_PROMPT_SECONDS = 5;
export const PLAYBACK_PAUSE_GRACE_MS = 1_500;

const INTRO_PROMPT_MS = INTRO_PROMPT_SECONDS * 1_000;

/**
 * Runs [then] with the seek's verdict, synchronously when the caller answered
 * synchronously.
 *
 * The synchronous path matters: a click on the pill must resolve within the
 * same task, or a re-render can land between the seek and the resolution and
 * offer the prompt a second time. A rejected promise counts as a refusal — the
 * prompt stays, which is the safe way to be wrong.
 */
function settleSeek(outcome: boolean | Promise<boolean>, then: (accepted: boolean) => void) {
  if (typeof outcome === "boolean") {
    then(outcome);
    return;
  }
  void outcome.then(then, () => then(false));
}

/**
 * Whether the prompt an async seek was started for is still the one on screen.
 * Compared by identity of intro and kind rather than by object, because the
 * countdown replaces the state object whenever it is rescheduled.
 */
function promptStillActive(
  current: ActivePrompt | null,
  key: string,
  kind: ActivePrompt["kind"],
): boolean {
  return current !== null && current.key === key && current.kind === kind;
}

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
  /**
   * Moves playback and reports whether the seek was accepted.
   *
   * A seek can be refused without anything moving — a watch-together room that
   * declines the transport request, a timeline that cannot be reanchored. The
   * prompt is the viewer's only handle on the intro, so it may only be resolved
   * and hidden once the seek that resolves it actually took.
   */
  onSeek: (seconds: number) => boolean | Promise<boolean>;
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
  /** What the clock had left when playback stopped, before the grace window ran. */
  const remainingAtEdgeRef = useRef<number | null>(null);
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
    remainingAtEdgeRef.current = null;
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
      remainingAtEdgeRef.current = null;
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
        // Resuming from a confirmed pause: the clock restarts from the value it
        // was frozen at, so the pause cost the viewer nothing.
        pausedRemainingRef.current = null;
        remainingAtEdgeRef.current = null;
        scheduleCountdown(pausedRemaining);
      } else if (remainingAtEdgeRef.current !== null) {
        // Playback came back inside the grace window, so this was a rebuffer
        // and not a pause. The deadline never moved; re-arm the expiry against
        // it, which fires immediately when the buffer stall outlasted the
        // countdown.
        remainingAtEdgeRef.current = null;
        scheduleCountdown(deadlineRef.current - Date.now());
      }
      return;
    }

    if (pausedRemainingRef.current !== null || pauseGraceTimerRef.current !== null) {
      return;
    }

    // The false edge. What the clock has left is captured here rather than when
    // the grace timer fires, or every real pause would silently eat the grace
    // window. The expiry is disarmed for the same reason it is not recomputed:
    // playback is stopped, so a prompt with less than 1.5 s left must not run
    // out while the picture is frozen. deadlineRef is deliberately untouched —
    // a rebuffer resumes against the original deadline.
    remainingAtEdgeRef.current = Math.max(0, deadlineRef.current - Date.now());
    clearCountdownTimers();

    pauseGraceTimerRef.current = window.setTimeout(() => {
      pauseGraceTimerRef.current = null;
      if (playingRef.current || !activePromptRef.current) return;
      const remaining = remainingAtEdgeRef.current ?? 0;
      remainingAtEdgeRef.current = null;
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
    settleSeek(onSeekRef.current(intro.end), (accepted) => {
      if (accepted) return;
      // Nothing moved, so there is no skip to undo and "Intro skipped" would be
      // a lie. The intro stays unresolved: whatever refused the seek may not
      // refuse the next one.
      if (!promptStillActive(activePromptRef.current, introKey, "undo")) return;
      clearPrompt();
    });
  }, [clearPrompt, currentTime, enabled, intro, introKey, mode, startPrompt]);
  /* eslint-enable react-hooks/set-state-in-effect */

  useEffect(
    () => () => {
      clearCountdownTimers();
      clearPauseGraceTimer();
    },
    [clearCountdownTimers, clearPauseGraceTimer],
  );

  /**
   * Acts on the visible prompt, reporting only whether there was one to act on.
   *
   * The return value is what the caller uses to consume the key press, so it
   * stays synchronous. Resolution waits for the seek: if the seek is refused
   * the prompt stays up and unresolved, because a viewer left sitting inside
   * the intro needs to be able to press it again.
   */
  const select = useCallback(() => {
    const current = activePromptRef.current;
    if (!current || !intro) return false;
    settleSeek(onSeekRef.current(current.kind === "skip" ? intro.end : intro.start), (accepted) => {
      if (!accepted) return;
      if (!promptStillActive(activePromptRef.current, current.key, current.kind)) return;
      resolvedKeysRef.current.add(current.key);
      clearPrompt();
    });
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
