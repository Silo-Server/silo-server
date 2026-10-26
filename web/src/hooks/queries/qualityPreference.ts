import { useEffectiveSettings } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { normalizeQualityPreferenceV3 } from "@/player/protocol-v3";

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
 * The canonical vocabulary is "auto", "480p", "720p", "1080p" and "2160p".
 * "original" is a deprecated member that a migration rewrites to "auto"; it,
 * and the older "source" and "max" spellings, read as "auto" here, because
 * Auto already plays the original file whenever the device and connection
 * can carry it.
 *
 * `fallback` is the profile column, used until the settings read resolves (and
 * if it fails). Playback must never block on a preferences fetch, and the
 * column still holds the pre-cutover choice for anyone who has not re-picked.
 */
export function useQualityPreference(fallback?: string | null): string | null {
  const { data } = useEffectiveSettings({ keys: [SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY] });
  const resolved = data?.[SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY]?.value;

  if (typeof resolved === "string") return normalizeQualityPreferenceV3(resolved);
  return fallback ? normalizeQualityPreferenceV3(fallback) : null;
}
