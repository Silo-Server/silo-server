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
import { useT } from "@/i18n";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";
import { settingLabel } from "@/lib/settingsText";

/**
 * Title art and its "Apply to all devices" companion. Unlike the rest of the
 * Title Pages page this is not only the profile's choice: it is this browser's
 * own choice, the profile's choice for every device, or, with neither, the
 * server default an administrator may set.
 */
export function TitleArtSettingsGroup() {
  const t = useT();
  const capabilities = useSettingsCapabilities();
  const titleArt = useTitleArtSetting();

  if (!settingsCapabilitiesSupportKey(capabilities.data, SETTING_KEYS.UI_TITLE_ART)) {
    return null;
  }

  const disabled = titleArt.isLoading || titleArt.isError || titleArt.isSaving;
  const save = (write: Promise<void>) =>
    write.catch(() => toast.error(t("web.settings.titleArt.saveFailed")));

  return (
    <SettingsGroup
      title={t("web.settings.titleArt.title")}
      description={t("web.settings.titleArt.description")}
    >
      <SettingRow
        icon={<Type />}
        label={settingLabel(SETTING_DEFINITIONS[SETTING_KEYS.UI_TITLE_ART])}
        description={t("web.settings.titleArt.titleArtDescription")}
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
        label={t("web.settings.titleArt.allDevices")}
        description={t("web.settings.titleArt.allDevicesDescription")}
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
            {titleArt.showTitleArt
              ? t("web.settings.titleArt.allDevicesOnNote")
              : t("web.settings.titleArt.allDevicesOffNote")}
          </span>
        </p>
      ) : titleArt.followsServerDefault ? (
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          {titleArt.showTitleArt
            ? t("web.settings.titleArt.serverDefaultOnNote")
            : t("web.settings.titleArt.serverDefaultOffNote")}
        </p>
      ) : titleArt.hasOwnChoice ? (
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          {t("web.settings.titleArt.ownChoiceNote")}
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
          {t("web.settings.titleArt.useServerDefault")}
        </button>
      ) : null}
    </SettingsGroup>
  );
}
