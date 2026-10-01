import {
  settingsCapabilitiesSupportKey,
  useEffectiveSettings,
  useSettingsCapabilities,
} from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";

const SPOILER_KEYS = [
  SETTING_KEYS.CATALOG_HIDE_UNWATCHED_EPISODE_IMAGES,
  SETTING_KEYS.CATALOG_HIDE_UNWATCHED_EPISODE_OVERVIEWS,
] as const;

export interface EpisodeSpoilerPrefs {
  /** Blur the still of an episode the profile has not started. */
  hideImages: boolean;
  /** Hide the description of an episode the profile has not started. */
  hideOverviews: boolean;
}

/**
 * The profile's spoiler protection for unwatched episodes. Pair it with
 * isEpisodeUnwatched from lib/episodeSpoilers to decide per episode.
 *
 * Both are off until the profile turns them on, and off against a server whose
 * manifest predates the settings, so an older server shows episodes as before.
 */
export function useEpisodeSpoilerPrefs(): EpisodeSpoilerPrefs {
  const capabilitiesQuery = useSettingsCapabilities();
  // Both keys share one introduced_in revision, so one check covers both.
  const isSupported = settingsCapabilitiesSupportKey(capabilitiesQuery.data, SPOILER_KEYS[0]);
  const query = useEffectiveSettings({ keys: SPOILER_KEYS, enabled: isSupported });
  // A disabled query can still hold data cached before a server downgrade.
  // Ignore it until the connected server proves it knows the keys.
  if (!isSupported) return { hideImages: false, hideOverviews: false };
  return {
    hideImages: query.data?.[SPOILER_KEYS[0]]?.value === true,
    hideOverviews: query.data?.[SPOILER_KEYS[1]]?.value === true,
  };
}
