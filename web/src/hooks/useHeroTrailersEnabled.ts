import { useSettingValue } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";

const KEY = SETTING_KEYS.HERO_TRAILERS_ENABLED;

/**
 * Whether this profile plays a title's clip behind the hero, or `undefined`
 * while the first read is in flight so the hero holds its backdrop instead of
 * starting something the profile may not have asked for.
 *
 * The default is off: a clip is bandwidth a viewer should opt into, exactly like
 * theme music, and its absence is what the hero already handles.
 */
export function useHeroTrailersEnabled(): boolean | undefined {
  const setting = useSettingValue<boolean>(KEY);
  return setting.isPending ? undefined : setting.value;
}
