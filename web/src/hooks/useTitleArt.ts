import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import {
  isSettingValueMissing,
  useClearSettingValue,
  useSetSettingValue,
  useSettingValue,
} from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";

const KEY = SETTING_KEYS.UI_TITLE_ART;

/**
 * Whether title pages may name a title with its logo artwork, or undefined
 * while the first read is in flight so a page can hold its title instead of
 * flashing the wrong one.
 *
 * A failed read or a server older than the key resolves to the contract
 * default, which is the logo behaviour every client had before the setting.
 */
export function useShowTitleArt(): boolean | undefined {
  const setting = useSettingValue<boolean>(KEY);
  return setting.isPending ? undefined : setting.value;
}

/**
 * The title-art switch and its "Apply to all devices" companion.
 *
 * ui.title_art resolves its profile value ahead of this device's own value, so
 * a profile value is the apply-to-all choice and its presence is the companion
 * switch's state. The manifest notes on ui.title_art are the cross-client
 * contract for the writes below.
 */
export function useTitleArtSetting() {
  const setting = useSettingValue<boolean>(KEY);
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();
  const showTitleArt = setting.value;
  const allDevices = setting.source === "profile";

  async function setShowTitleArt(next: boolean) {
    await setValue.mutateAsync({
      key: KEY,
      value: next,
      identity: { scope: allDevices ? "profile" : "profile_device" },
    });
  }

  async function setAllDevices(next: boolean) {
    if (next) {
      await setValue.mutateAsync({ key: KEY, value: showTitleArt, identity: { scope: "profile" } });
      return;
    }
    const profile = captureProfileRequestContext();
    // Pin this device first so it keeps its current look once the profile
    // value stops winning.
    await setValue.mutateAsync({
      key: KEY,
      value: showTitleArt,
      identity: { scope: "profile_device" },
    });
    // The clear goes out under whichever profile is active now; after a
    // profile switch it would remove the other profile's choice.
    if (profile && !isCapturedProfileAuthorityActive(profile)) return;
    try {
      await clearValue.mutateAsync({ key: KEY, identity: { scope: "profile" } });
    } catch (error) {
      if (!isSettingValueMissing(error)) throw error;
    }
  }

  return {
    showTitleArt,
    allDevices,
    isLoading: setting.isPending,
    isError: setting.isError,
    isSaving: setValue.isPending || clearValue.isPending,
    setShowTitleArt,
    setAllDevices,
  };
}
