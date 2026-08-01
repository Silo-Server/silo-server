import { useEffectiveSettings } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";

/**
 * The resolution cap playback should apply, read from the contract.
 *
 * The settings screen writes `playback.preferred_quality` (and its bitrate
 * companion) as canonical rows, while playback historically took the cap from
 * `currentProfile.quality_preference` — a legacy compound column the canonical
 * write deliberately does not mirror, because one string cannot losslessly
 * carry two axes. Left alone, choosing a quality would appear to save and then
 * change nothing about what plays.
 *
 * The canonical vocabulary is already what these consumers parse — "auto",
 * "480p", "720p", "1080p", "2160p" — so the value passes straight through.
 * "original" means "do not cap", which is what "auto" already expresses to
 * every consumer, so it maps there rather than reaching a resolution table
 * that has no entry for it.
 *
 * `fallback` is the profile column, used until the settings read resolves (and
 * if it fails). Playback must never block on a preferences fetch, and the
 * column still holds the pre-cutover choice for anyone who has not re-picked.
 */
export function useQualityPreference(fallback?: string | null): string | null {
  const { data } = useEffectiveSettings({ keys: [SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY] });
  const resolved = data?.[SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY]?.value;

  if (typeof resolved !== "string") return fallback ?? null;
  if (resolved === "original") return "auto";
  return resolved;
}
