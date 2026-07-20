/**
 * Formats a duration in seconds as a short human label: "2h 10m", "45m", or
 * "<1m" for anything under a minute. Minutes are floored rather than rounded
 * so the label never overstates time remaining.
 */
export function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 60) {
    return "<1m";
  }
  const totalMinutes = Math.floor(seconds / 60);
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return hours > 0 ? `${hours}h ${minutes}m` : `${minutes}m`;
}
