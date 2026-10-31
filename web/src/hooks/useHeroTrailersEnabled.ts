import { useSettingValue } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";

const KEY = SETTING_KEYS.HERO_TRAILERS_ENABLED;

/**
 * Whether this profile plays a title's clip behind the hero, or `undefined`
 * while the first read is in flight so the hero holds its backdrop instead of
 * starting something the profile may not have asked for.
 *
 * The default is on: a clip behind the featured row is what the row is for. A
 * profile or a device turns it off when it would rather keep the still backdrop,
 * and a deployment can move the default for its whole household with the admin
 * default.
 */
export function useHeroTrailersEnabled(): boolean | undefined {
  const setting = useSettingValue<boolean>(KEY);
  return setting.isPending ? undefined : setting.value;
}
