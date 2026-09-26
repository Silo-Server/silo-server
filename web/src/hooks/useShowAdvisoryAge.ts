import {
  settingsCapabilitiesSupportKey,
  useEffectiveSettings,
  useSettingsCapabilities,
} from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";

const ADVISORY_KEY = SETTING_KEYS.CATALOG_SHOW_ADVISORY_AGE;

/**
 * Whether this profile asked to see advisory ages on item detail.
 *
 * Pass `enabled` false when the item carries no advisory: there is nothing to
 * show either way, so the setting is not worth a round trip. Advisory coverage
 * is partial by nature — the providers that supply it are rate limited — so
 * most items skip the lookup entirely.
 *
 * Off for every profile that has not opted in, and off against a server whose
 * manifest predates the setting, so an older server renders nothing rather
 * than a badge it never described. This decides only whether the badge shows;
 * the separate profile advisory-age limit decides what a profile may watch.
 */
export function useShowAdvisoryAge(enabled = true): boolean {
  const capabilitiesQuery = useSettingsCapabilities({ enabled });
  const isSupported =
    enabled && settingsCapabilitiesSupportKey(capabilitiesQuery.data, ADVISORY_KEY);
  const query = useEffectiveSettings({ keys: [ADVISORY_KEY], enabled: isSupported });
  // A disabled query can still hold data cached before a server downgrade.
  // Ignore it until the connected server proves it knows the key.
  if (!isSupported) return false;
  return query.data?.[ADVISORY_KEY]?.value === true;
}
