import { Info, MonitorSmartphone, RotateCcw, Type } from "lucide-react";
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
 * Title Pages page this is not only the profile's choice: it is this browser's
 * own choice, the profile's choice for every device, or, with neither, the
 * server default an administrator may set.
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
    <SettingsGroup title="Title" description="How a movie or show is named at the top of its page.">
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
      ) : titleArt.followsServerDefault ? (
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          Using the server default ({titleArt.showTitleArt ? "on" : "off"}). Change it here to
          choose for this browser.
        </p>
      ) : titleArt.hasOwnChoice ? (
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          Only affects this browser. Your other devices keep their own setting.
        </p>
      ) : null}
      {titleArt.hasOwnChoice ? (
        <button
          type="button"
          disabled={disabled}
          onClick={() => void save(titleArt.followServerDefault())}
          className="text-muted-foreground hover:text-foreground inline-flex min-h-11 items-center gap-1.5 text-[13px] transition-colors disabled:opacity-50 sm:min-h-0"
        >
          <RotateCcw aria-hidden="true" className="size-3.5" />
          Use server default
        </button>
      ) : null}
    </SettingsGroup>
  );
}
