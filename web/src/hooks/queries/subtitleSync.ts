import { useCallback } from "react";
import {
  isSettingValueMissing,
  useClearSettingValue,
  useEffectiveSettings,
  useSetSettingValue,
  useSettingsCapabilities,
  type SettingIdentity,
} from "@/hooks/queries/settingValues";
import { useProfileDefaultWriter } from "@/hooks/queries/profileDefaults";
import { SETTING_DEFINITIONS, SETTING_KEYS, SETTINGS_API_VERSION } from "@/lib/settingsContract";

const KEY = SETTING_KEYS.PLAYER_SUBTITLE_SYNC_MS;
const KEYS = [KEY];
const DEVICE_SCOPE: SettingIdentity = { scope: "profile_device" };

/** The manifest revision that added the profile scope for this key. */
const PROFILE_SCOPE_REVISION =
  SETTING_DEFINITIONS[KEY].scopeIntroducedIn[SETTING_DEFINITIONS[KEY].scopes.indexOf("profile")]!;

/** The contract's bounds for the offset, so a nudge past them is clamped rather than refused. */
export const SUBTITLE_SYNC_MAX_MS = 10_000;

export function clampSubtitleSyncMs(ms: number): number {
  return Math.max(-SUBTITLE_SYNC_MAX_MS, Math.min(SUBTITLE_SYNC_MAX_MS, Math.round(ms)));
}

/**
 * The profile's subtitle timing offset (player.subtitle_sync_ms), in
 * milliseconds; positive shows subtitles later.
 *
 * Timing is one of the subtitle display settings that follow the profile to
 * every device, so the settings screen and the player's Delay control both
 * write the profile row. The shared writer also clears a row this browser may
 * hold, which would otherwise outrank the profile value here. A server older
 * than the profile scope refuses that write, so there the offset is saved for
 * this browser only, as it was before.
 */
export function useSubtitleSyncSetting() {
  const query = useEffectiveSettings({ keys: KEYS });
  const capabilities = useSettingsCapabilities();
  const profileWriter = useProfileDefaultWriter(query.data);
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();
  const supportsProfileScope =
    capabilities.data?.api_version === SETTINGS_API_VERSION &&
    capabilities.data.manifest_revision >= PROFILE_SCOPE_REVISION;

  const value = query.data?.[KEY]?.value;
  const offsetMs =
    typeof value === "number" ? value : (SETTING_DEFINITIONS[KEY].defaultValue as number);

  const { save: saveProfile, reset: resetProfile } = profileWriter;
  const { mutateAsync: setDeviceValue } = setValue;
  const { mutateAsync: clearDeviceValue } = clearValue;
  const save = useCallback(
    async (ms: number) => {
      const next = clampSubtitleSyncMs(ms);
      if (supportsProfileScope) return saveProfile(KEY, next);
      await setDeviceValue({ key: KEY, value: next, identity: DEVICE_SCOPE });
    },
    [saveProfile, setDeviceValue, supportsProfileScope],
  );
  const reset = useCallback(async () => {
    if (supportsProfileScope) return resetProfile(KEY);
    try {
      await clearDeviceValue({ key: KEY, identity: DEVICE_SCOPE });
    } catch (error) {
      if (isSettingValueMissing(error)) return;
      throw error;
    }
  }, [clearDeviceValue, resetProfile, supportsProfileScope]);

  return {
    offsetMs,
    /**
     * False until the server's value and contract revision have arrived, so a
     * caller does not act on the default or write a scope the server refuses.
     */
    isLoaded: query.data !== undefined && capabilities.data !== undefined,
    save,
    reset,
    isSaving: profileWriter.isSaving || setValue.isPending || clearValue.isPending,
  };
}
