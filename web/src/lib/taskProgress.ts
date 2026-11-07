import { t } from "@/i18n";
import { formatDecimal } from "@/lib/numberFormat";

export function clampTaskProgress(progress: number): number {
  if (!Number.isFinite(progress)) return 0;
  return Math.min(100, Math.max(0, progress));
}

export function formatTaskProgress(progress: number): string {
  const clamped = clampTaskProgress(progress);
  if (Number.isInteger(clamped))
    return t("web.admin.tasks.lib.taskProgress.percent", { percent: String(clamped) });
  // A task that is still running reports just under 100 on purpose; one-decimal
  // rounding must not turn that back into a finished-looking "100.0%".
  const shown = Number(clamped.toFixed(1)) >= 100 ? 99.9 : clamped;
  return t("web.admin.tasks.lib.taskProgress.percent", { percent: formatDecimal(shown, 1) });
}
