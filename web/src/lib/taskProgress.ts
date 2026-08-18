export function clampTaskProgress(progress: number): number {
  if (!Number.isFinite(progress)) return 0;
  return Math.min(100, Math.max(0, progress));
}

export function formatTaskProgress(progress: number): string {
  const clamped = clampTaskProgress(progress);
  if (Number.isInteger(clamped)) return `${clamped}%`;
  return `${clamped.toFixed(1)}%`;
}
