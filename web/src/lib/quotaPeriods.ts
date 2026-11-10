import { t } from "@/i18n";

// Rolling-window labels for the subtitle-AI transcription quota periods.
// One source of truth for every surface that names a period; keep in sync
// with QuotaPeriodWindow in internal/subtitles/ai/quota.go.
export const QUOTA_PERIODS = ["day", "week", "month"] as const;

/** "24 hours" for `day`, and so on; undefined for a period this build doesn't know. */
export function quotaPeriodWindowLabel(period: string): string | undefined {
  switch (period) {
    case "day":
      return t("web.subtitles.quotaWindow.day");
    case "week":
      return t("web.subtitles.quotaWindow.week");
    case "month":
      return t("web.subtitles.quotaWindow.month");
    default:
      return undefined;
  }
}
