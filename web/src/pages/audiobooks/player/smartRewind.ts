/**
 * Smart rewind: after a pause, back up a little so the listener regains
 * context before new audio starts. The longer the pause, the bigger the jump.
 */
export function smartRewindSeconds(pauseMs: number): number {
  if (!Number.isFinite(pauseMs) || pauseMs < 10_000) return 0;
  if (pauseMs < 60_000) return 3;
  if (pauseMs < 600_000) return 10;
  if (pauseMs < 3_600_000) return 20;
  return 30;
}

/**
 * Rewind applied when resuming a book "cold" (from the detail page) where the
 * age of the saved progress is unknown. Flat, small, and skipped for fresh
 * starts so "Listen from Start" and chapter jumps are never moved.
 */
export const COLD_RESUME_REWIND_SECONDS = 10;

export function coldResumePosition(resumeSeconds: number, smartRewindEnabled: boolean): number {
  if (!smartRewindEnabled || resumeSeconds <= 0) return Math.max(0, resumeSeconds);
  return Math.max(0, resumeSeconds - COLD_RESUME_REWIND_SECONDS);
}
