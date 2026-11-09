import { t } from "@/i18n";

export function clampTaskProgress(progress: number): number {
  if (!Number.isFinite(progress)) return 0;
  return Math.min(100, Math.max(0, progress));
}

export function formatTaskProgress(progress: number): string {
  const clamped = clampTaskProgress(progress);
  if (Number.isInteger(clamped))
    return t("web.admin.tasks.lib.taskProgress.percent", { percent: String(clamped) });
  const rounded = clamped.toFixed(1);
  // A task that is still running reports just under 100 on purpose; one-decimal
  // rounding must not turn that back into a finished-looking "100.0%".
  if (Number(rounded) >= 100)
    return t("web.admin.tasks.lib.taskProgress.percent", { percent: "99.9" });
  return t("web.admin.tasks.lib.taskProgress.percent", { percent: rounded });
}
