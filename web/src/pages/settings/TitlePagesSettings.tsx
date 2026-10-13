import { useMemo } from "react";
import { toast } from "sonner";

import { PageHeader } from "@/components/page/PageHeader";
import { SettingRow } from "@/components/settings/SettingRow";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Switch } from "@/components/ui/switch";
import { useProfileDefaultWriter } from "@/hooks/queries/profileDefaults";
import {
  settingsCapabilitiesSupportKey,
  useEffectiveSettings,
  useSettingsCapabilities,
} from "@/hooks/queries/settingValues";
import { SETTING_DEFINITIONS, SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";

import { TitleArtSettingsGroup } from "./TitleArtSettingsGroup";

const EPISODE_SPOILER_KEYS = [
  SETTING_KEYS.CATALOG_HIDE_UNWATCHED_EPISODE_IMAGES,
  SETTING_KEYS.CATALOG_HIDE_UNWATCHED_EPISODE_OVERVIEWS,
] as const;

/**
 * Settings > Title Pages: what a movie or show page shows and plays when it
 * opens. Every value here is the profile's own choice except title art, which
 * can also be this browser's alone (see TitleArtSettingsGroup).
 */
export default function TitlePagesSettings() {
  const capabilities = useSettingsCapabilities();
  const supportsThemeMusic = settingsCapabilitiesSupportKey(
    capabilities.data,
    SETTING_KEYS.UI_THEME_MUSIC_ENABLED,
  );
  const supportsThemeMusicLoop = settingsCapabilitiesSupportKey(
    capabilities.data,
    SETTING_KEYS.UI_THEME_MUSIC_LOOP,
  );
  // Both spoiler keys share one introduced_in revision.
  const supportsEpisodeSpoilers = settingsCapabilitiesSupportKey(
    capabilities.data,
    SETTING_KEYS.CATALOG_HIDE_UNWATCHED_EPISODE_IMAGES,
  );
  const keys = useMemo<SettingKey[]>(
    () => [
      SETTING_KEYS.CATALOG_SHOW_ADVISORY_AGE,
      ...(supportsThemeMusic ? [SETTING_KEYS.UI_THEME_MUSIC_ENABLED] : []),
      ...(supportsThemeMusicLoop ? [SETTING_KEYS.UI_THEME_MUSIC_LOOP] : []),
      ...(supportsEpisodeSpoilers ? EPISODE_SPOILER_KEYS : []),
    ],
    [supportsEpisodeSpoilers, supportsThemeMusic, supportsThemeMusicLoop],
  );
  const { data: effective, isPending, isError } = useEffectiveSettings({ keys });
  // Saves at profile scope and clears any device override shadowing it, the
  // same writer Playback uses, so a device row cannot snap a switch back.
  const { save, isSaving } = useProfileDefaultWriter(effective);
  const disabled = isSaving || isPending || isError;

  // The effective endpoint resolves an unset key to the contract default.
  const read = (key: SettingKey) =>
    (effective?.[key]?.value ?? SETTING_DEFINITIONS[key].defaultValue) as boolean;

  const switchRow = (key: SettingKey, description = SETTING_DEFINITIONS[key].description) => (
    <SettingRow
      key={key}
      label={SETTING_DEFINITIONS[key].label}
      description={description}
      control={(id) => (
        <Switch
          id={id}
          checked={read(key)}
          disabled={disabled}
          onCheckedChange={(value) =>
            save(key, value).catch(() => toast.error("Failed to save title page setting"))
          }
        />
      )}
    />
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title="Title Pages"
        subtitle="What a movie or show page shows and plays when you open it."
      />

      <TitleArtSettingsGroup />

      {supportsThemeMusic ? (
        <SettingsGroup title="Sound" description="Plays while a title's page is open.">
          {switchRow(SETTING_KEYS.UI_THEME_MUSIC_ENABLED)}
          {supportsThemeMusicLoop ? switchRow(SETTING_KEYS.UI_THEME_MUSIC_LOOP) : null}
        </SettingsGroup>
      ) : null}

      {supportsEpisodeSpoilers ? (
        <SettingsGroup
          title="Spoilers"
          description="Applies to episodes you have not started, on every device that uses this profile."
        >
          {EPISODE_SPOILER_KEYS.map((key) => switchRow(key))}
        </SettingsGroup>
      ) : null}

      <SettingsGroup title="Age guidance">
        {switchRow(
          SETTING_KEYS.CATALOG_SHOW_ADVISORY_AGE,
          "Show a suggested minimum viewer age, such as Common Sense Media's, on a title's page. This only shows the age; a household manager can limit a profile by it in the profile's access settings.",
        )}
      </SettingsGroup>
    </div>
  );
}
