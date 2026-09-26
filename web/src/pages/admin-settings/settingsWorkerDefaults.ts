/**
 * The recommended values for the worker-pool settings, mirrored from
 * `adminSettingDefaults` in `internal/config/admin_settings.go`. "Restore
 * defaults" stages these through the normal form flow so the save bar
 * confirms them like any other edit.
 *
 * Every value the UI restores is written once here so the button cannot drift
 * from what the server runs with, the same way `settingsPathDefaults.ts`
 * mirrors the path fallbacks. Change a default in Go and this module has to
 * change with it.
 */
export const WORKER_SETTING_DEFAULTS: Readonly<Record<string, string>> = {
  "scanner.workers": "8",
  /** 0 means one encode per CPU core, resolved when the task runs. */
  "metadata.image_workers": "0",
  "matcher.workers": "8",
  "matcher.batch_size": "500",
};

/** True when at least one of the keys holds something other than its default. */
export function hasWorkerOverrides(getValue: (key: string) => string): boolean {
  return Object.entries(WORKER_SETTING_DEFAULTS).some(
    ([key, fallback]) => getValue(key).trim() !== "" && getValue(key).trim() !== fallback,
  );
}
