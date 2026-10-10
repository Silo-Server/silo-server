import type { AutoscanSettings, AutoscanSource } from "@/api/types";

/**
 * Whether the header offers Run now. Run now polls every enabled polling
 * source immediately, but it does nothing while Autoscan is off and never
 * polls webhook sources, so it is hidden when Autoscan is known to be off or
 * the loaded source list has no enabled polling source. While either is still
 * loading or failed to load, the button stays available.
 */
export function showRunNow(
  sources: AutoscanSource[] | undefined,
  settings: AutoscanSettings | undefined,
): boolean {
  if (settings?.enabled === false) return false;
  if (!sources) return true;
  return sources.some((source) => source.enabled && source.delivery_mode === "poll");
}
