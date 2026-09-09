/** Clamp on the media timeline; an unknown duration must not prevent forward seeking. */
export function skipTarget(current: number, duration: number, delta: number): number {
  return Math.max(0, Math.min(duration > 0 ? duration : Infinity, current + delta));
}
