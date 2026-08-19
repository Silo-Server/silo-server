export function clampTaskProgress(progress: number): number {
  if (!Number.isFinite(progress)) return 0;
  return Math.min(100, Math.max(0, progress));
}

export function formatTaskProgress(progress: number): string {
  const clamped = clampTaskProgress(progress);
  if (Number.isInteger(clamped)) return `${clamped}%`;
  const rounded = clamped.toFixed(1);
  // A task that is still running reports just under 100 on purpose; one-decimal
  // rounding must not turn that back into a finished-looking "100.0%".
  if (Number(rounded) >= 100) return "99.9%";
  return `${rounded}%`;
}
