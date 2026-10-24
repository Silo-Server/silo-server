import { useEffectiveSettings } from "@/hooks/queries/settingValues";
import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import type { MissingEpisodes } from "@/hooks/queries/catalogRead";
import { SETTING_KEYS } from "@/lib/settingsContract";

const MISSING_KEY = SETTING_KEYS.CATALOG_SHOW_MISSING_EPISODES;

/**
 * Which episodes the library lacks this profile's series pages list: none
 * until the profile turns the setting on, then the aired ones, and also the
 * ones still to air while a download server takes series requests, since that
 * server can say whether they are already coming.
 *
 * No capability check: this web client ships with the server that defines
 * the setting, so the server always knows it.
 */
export function useShowMissingEpisodes(): MissingEpisodes {
  const query = useEffectiveSettings({ keys: [MISSING_KEY] });
  const show = query.data?.[MISSING_KEY]?.value === true;
  const status = useRequestFeatureStatus({ enabled: show, refetchOnMount: false });
  if (!show) return "none";
  return status.data?.requests_enabled && status.data.series_server_connected ? "all" : "aired";
}
