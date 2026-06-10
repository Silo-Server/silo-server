// Rolling-window labels for the subtitle-AI transcription quota periods.
// One source of truth for every surface that names a period; keep in sync
// with QuotaPeriodWindow in internal/subtitles/ai/quota.go.
export const QUOTA_PERIODS = ["day", "week", "month"] as const;

export const QUOTA_PERIOD_WINDOW_LABELS: Record<string, string> = {
  day: "24 hours",
  week: "7 days",
  month: "30 days",
};
