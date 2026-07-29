import { useCallback } from "react";
import { ApiClientError } from "@/api/client";
import {
  useClearSettingValue,
  useEffectiveSettings,
  useSetSettingValue,
  type SettingIdentity,
} from "@/hooks/queries/settingValues";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";

/**
 * The one place playback.auto_play_next is read and written.
 *
 * Two surfaces edit this setting — the Playback settings screen and the
 * post-roll "Auto-play is on/off" toggle — and both render the *resolved*
 * value. The contract resolves profile_device above profile, so a surface that
 * writes the device row while the other writes the profile row makes the
 * second control inert: it saves a profile value the device row keeps
 * shadowing, and the switch snaps back on the next read. Sharing one hook is
 * what stops the two disagreeing about which scope a save lands at.
 *
 * The chosen scope is the profile, matching the rest of the Playback screen:
 * "auto-play the next episode" is a viewing preference, not a property of the
 * browser it was expressed in. A device row can still exist — the settings
 * migration converts legacy user_device_settings rows into profile_device
 * ones, and the contract lets other clients write it — so a save clears this
 * device's row as well. Without that, a migrated override would silently
 * shadow every later choice with no web affordance to remove it.
 */

const KEY = SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT;

/** Both surfaces edit the profile layer. */
const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };

/** The scope a stale override can hide in, cleared on every save. */
const DEVICE_SCOPE: SettingIdentity = { scope: "profile_device" };

export function useAutoPlayNextSetting() {
  const query = useEffectiveSettings({ keys: [KEY] });
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();

  const entry = query.data?.[KEY];
  // The effective endpoint resolves an unset key to the contract default, so
  // an absent answer (first paint) reads the same as a stored default rather
  // than as a local literal that could disagree with the server.
  const enabled = (entry?.value ?? SETTING_DEFINITIONS[KEY].defaultValue) as boolean;

  /** Whether this device holds an override that outranks the profile row. */
  const hasDeviceOverride = entry?.source === "profile_device";

  const setEnabled = useCallback(
    async (next: boolean) => {
      await setValue.mutateAsync({ key: KEY, value: next, identity: PROFILE_SCOPE });
      // The profile value is the durable expression, so it is written first: a
      // failed clear leaves the choice stored rather than losing it. Nothing to
      // clear is the state this asks for, so a 404 is success.
      if (!hasDeviceOverride) return;
      try {
        await clearValue.mutateAsync({ key: KEY, identity: DEVICE_SCOPE });
      } catch (error) {
        if (error instanceof ApiClientError && error.status === 404) return;
        throw error;
      }
    },
    [clearValue, hasDeviceOverride, setValue],
  );

  return {
    enabled,
    hasDeviceOverride,
    setEnabled,
    isSaving: setValue.isPending || clearValue.isPending,
  };
}
