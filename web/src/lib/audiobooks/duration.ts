/**
 * Listening-time formatting for audiobooks. Audiobooks are hours long, so time
 * is framed in "hr/min" units ("27 hr 14 min") rather than the "52:30" or
 * "112 min" framings used for video runtimes.
 */

/** "27 hr 14 min", "3 hr", "45 min"; durations under a minute round up. */
export function formatHoursMinutes(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds <= 0) {
    return "0 min";
  }
  const totalMinutes = Math.round(totalSeconds / 60);
  if (totalMinutes < 1) {
    return "1 min";
  }
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  if (hours <= 0) {
    return `${minutes} min`;
  }
  if (minutes === 0) {
    return `${hours} hr`;
  }
  return `${hours} hr ${minutes} min`;
}

/** "27 hr 14 min left" for an in-progress listen; null without a duration. */
export function formatListeningTimeLeft(
  positionSeconds: number | undefined,
  durationSeconds: number | undefined,
): string | null {
  if (!durationSeconds || durationSeconds <= 0) {
    return null;
  }
  const remaining = Math.max(durationSeconds - (positionSeconds ?? 0), 0);
  return `${formatHoursMinutes(remaining)} left`;
}
