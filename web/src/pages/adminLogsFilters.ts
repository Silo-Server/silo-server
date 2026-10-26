/** Sentinel for "no filter" in the Admin Logs level Select. */
export const LOG_FILTER_ALL = "all" as const;

/** Treat a reserved "all" URL value as no filter; leave other values unchanged. */
export function normalizeLogFilterParam(value: string): string {
  return value === LOG_FILTER_ALL ? "" : value;
}

/** Levels stored by opslog (slog.Level.String(), lowercased). */
export const LOG_LEVEL_FILTER_OPTIONS = ["debug", "info", "warn", "error"] as const;

/**
 * Canonical log components from docs/architecture/observability.md, plus
 * ffmpeg (playback sink) and grandfathered settings / webhook_sync.
 * Suggestions only: components emitted by the server are not a closed set.
 */
export const LOG_COMPONENT_FILTER_OPTIONS = [
  "access",
  "activitylog",
  "adminjob",
  "ai",
  "api",
  "app",
  "audiobooks",
  "auth",
  "autoscan",
  "catalog",
  "chapterthumbs",
  "diagnostics",
  "downloads",
  "ebooks",
  "ffmpeg",
  "historyimport",
  "jellycompat",
  "libraryingest",
  "manga",
  "metadata",
  "nodeconfig",
  "nodepool",
  "noderecipe",
  "nodesessions",
  "notifications",
  "opslog",
  "playback",
  "plugins",
  "policy",
  "proxy",
  "ratelimit",
  "recommendations",
  "requests",
  "scanner",
  "scanqueue",
  "sections",
  "settings",
  "taskmanager",
  "telemetry",
  "transcodenode",
  "watchlist",
  "watchsync",
  "webhook_sync",
  "webhooksync",
  "worker",
] as const;

export type LogLevelFilterOption = (typeof LOG_LEVEL_FILTER_OPTIONS)[number];
export type LogComponentFilterOption = (typeof LOG_COMPONENT_FILTER_OPTIONS)[number];

/** Options for a Select, including a current URL value not in the fixed list. */
export function withUnknownFilterOption(options: readonly string[], current: string): string[] {
  if (!current || options.includes(current)) {
    return [...options];
  }
  return [...options, current].sort((a, b) => a.localeCompare(b));
}
