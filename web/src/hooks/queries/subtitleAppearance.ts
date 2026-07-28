import { useCallback, useMemo } from "react";
import { ApiClientError } from "@/api/client";
import {
  useClearSettingValue,
  useEffectiveSettings,
  useSetSettingValue,
  type SettingIdentity,
} from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { parseSubtitleAppearance, type SubtitleAppearance } from "@/lib/subtitleAppearance";

/**
 * The one place subtitle appearance is read and written.
 *
 * The value used to live behind three bespoke endpoints
 * (/settings/subtitle_appearance/effective and the PUT/DELETE pair on
 * /settings/device/subtitle_appearance) that existed only because the legacy
 * string API had no way to express "this device's copy of an object-valued
 * setting". The contract does: playback.subtitle_appearance is an object typed
 * by the manifest, resolving profile_device → profile → default, so a device
 * override is an ordinary write at profile_device and "use the fallback" is an
 * ordinary delete at that scope.
 *
 * Three surfaces render this value — the settings screen, the in-player panel,
 * and the cue renderer — and before the cutover each parsed the effective
 * response itself. Sharing one hook is what keeps them from disagreeing about
 * which scope a save lands at.
 */

/** Both the settings screen and the in-player panel override per device. */
const DEVICE_SCOPE: SettingIdentity = { scope: "profile_device" };

export function useSubtitleAppearanceSetting() {
  const query = useEffectiveSettings({ keys: [SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE] });
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();

  const entry = query.data?.[SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE];
  // The manifest default is the same object DEFAULT_SUBTITLE_APPEARANCE spells,
  // and the effective endpoint always answers, so an unset value arrives as the
  // default rather than as nothing to parse.
  const appearance = useMemo(() => parseSubtitleAppearance(entry?.value), [entry?.value]);

  /**
   * Whether this device holds its own override, which is what gates the
   * "reset" affordance. The resolved scope names the row the value came from,
   * so this needs no separate read of the stored value.
   */
  const hasDeviceOverride = entry?.source === "profile_device";

  const save = useCallback(
    (next: SubtitleAppearance) =>
      setValue.mutateAsync({
        key: SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE,
        value: next,
        identity: DEVICE_SCOPE,
      }),
    [setValue],
  );

  const reset = useCallback(async () => {
    try {
      await clearValue.mutateAsync({
        key: SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE,
        identity: DEVICE_SCOPE,
      });
    } catch (error) {
      // Nothing stored at this scope is the state a reset asks for, so the
      // canonical DELETE's 404 is success, matching how the legacy per-device
      // delete behaved.
      if (error instanceof ApiClientError && error.status === 404) return;
      throw error;
    }
  }, [clearValue]);

  return {
    appearance,
    hasDeviceOverride,
    save,
    reset,
    isSaving: setValue.isPending,
    isResetting: clearValue.isPending,
    isLoading: query.isLoading,
  };
}
