import { Info, MonitorSmartphone, Type } from "lucide-react";
import { toast } from "sonner";

import { SettingRow } from "@/components/settings/SettingRow";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Switch } from "@/components/ui/switch";
import {
  settingsCapabilitiesSupportKey,
  useSettingsCapabilities,
} from "@/hooks/queries/settingValues";
import { useTitleArtSetting } from "@/hooks/useTitleArt";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";

/**
 * Title art and its "Apply to all devices" companion. Unlike the rest of the
 * Navigation & Cards page this is not a web-family setting: it is this
 * browser's own choice, or the profile's choice for every device.
 */
export function TitleArtSettingsGroup() {
  const capabilities = useSettingsCapabilities();
  const titleArt = useTitleArtSetting();

  if (!settingsCapabilitiesSupportKey(capabilities.data, SETTING_KEYS.UI_TITLE_ART)) {
    return null;
  }

  const disabled = titleArt.isLoading || titleArt.isError || titleArt.isSaving;
  const save = (write: Promise<void>) =>
    write.catch(() => toast.error("Could not save the title art setting"));

  return (
    <SettingsGroup
      title="Title pages"
      description="How a movie or show is named at the top of its page."
    >
      <SettingRow
        icon={<Type />}
        label={SETTING_DEFINITIONS[SETTING_KEYS.UI_TITLE_ART].label}
        description="Use logo artwork as the title when available."
        control={(id) => (
          <Switch
            id={id}
            checked={titleArt.showTitleArt}
            disabled={disabled}
            onCheckedChange={(next) => void save(titleArt.setShowTitleArt(next))}
          />
        )}
      />
      <SettingRow
        icon={<MonitorSmartphone />}
        label="Apply to all devices"
        description="Use this choice on every device signed into this profile."
        control={(id) => (
          <Switch
            id={id}
            checked={titleArt.allDevices}
            disabled={disabled}
            onCheckedChange={(next) => void save(titleArt.setAllDevices(next))}
          />
        )}
      />
      {titleArt.allDevices ? (
        <p className="bg-foreground/5 text-foreground/85 flex gap-2.5 rounded-xl px-3.5 py-3 text-[13px] leading-relaxed">
          <Info aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
          <span>
            Title art is {titleArt.showTitleArt ? "on" : "off"} on every device signed into this
            profile. Changing it here changes it everywhere. Turn off “Apply to all devices” to
            choose per device again.
          </span>
        </p>
      ) : (
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          Only affects this browser. Your other devices keep their own setting.
        </p>
      )}
    </SettingsGroup>
  );
}
