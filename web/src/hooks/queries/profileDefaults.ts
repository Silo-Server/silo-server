import { useCallback } from "react";
import { ApiClientError } from "@/api/client";
import {
  useClearSettingValue,
  useSetSettingValue,
  type EffectiveSettingsMap,
  type SettingIdentity,
} from "@/hooks/queries/settingValues";
import { SETTING_DEFINITIONS, type SettingKey } from "@/lib/settingsContract";

/**
 * Writing a profile default when a device override may be shadowing it.
 *
 * Every "Defaults" screen reads the *resolved* value and writes the profile
 * row. The contract resolves profile_device above profile, so if a device row
 * exists — the settings migration converts legacy user_device_settings rows
 * into profile_device ones, and other clients write them directly — the
 * profile write lands but the displayed value never moves: the override keeps
 * winning and the control snaps back. Clearing this device's row alongside the
 * profile write is what makes the control mean what it says, and is the only
 * affordance the web has for removing a migrated override.
 *
 * useAutoPlayNextSetting arrived at this shape first, for the same reason;
 * this is that logic with the key as a parameter.
 */

const PROFILE_SCOPE: SettingIdentity = { scope: "profile" };
const DEVICE_SCOPE: SettingIdentity = { scope: "profile_device" };

/** Whether the contract lets this key be overridden per device. */
export function isDeviceOverridable(key: SettingKey): boolean {
  return SETTING_DEFINITIONS[key].scopes.includes("profile_device");
}

/**
 * Returns a writer that saves at profile scope and clears any override on this
 * device. Pass the effective map the screen already reads so the clear is
 * skipped when the resolved value did not come from a device row — the common
 * case, and a DELETE for a row that does not exist is a wasted round trip.
 */
export function useProfileDefaultWriter(effective: EffectiveSettingsMap | undefined) {
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();

  const save = useCallback(
    async (key: SettingKey, value: unknown) => {
      // The profile value is the durable expression, so it is written first: a
      // failed clear leaves the choice stored rather than losing it.
      await setValue.mutateAsync({ key, value, identity: PROFILE_SCOPE });

      if (!isDeviceOverridable(key)) return;
      if (effective?.[key]?.source !== "profile_device") return;
      try {
        await clearValue.mutateAsync({ key, identity: DEVICE_SCOPE });
      } catch (error) {
        // Nothing to clear is the state this asks for.
        if (error instanceof ApiClientError && error.status === 404) return;
        throw error;
      }
    },
    [clearValue, effective, setValue],
  );

  /** Clear the profile row, and any device row shadowing it, so the key inherits again. */
  const reset = useCallback(
    async (key: SettingKey) => {
      const clearScope = async (identity: SettingIdentity) => {
        try {
          await clearValue.mutateAsync({ key, identity });
        } catch (error) {
          if (error instanceof ApiClientError && error.status === 404) return;
          throw error;
        }
      };
      await clearScope(PROFILE_SCOPE);
      if (isDeviceOverridable(key)) await clearScope(DEVICE_SCOPE);
    },
    [clearValue],
  );

  return {
    save,
    reset,
    isSaving: setValue.isPending || clearValue.isPending,
  };
}
