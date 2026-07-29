import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { SettingRow } from "@/components/settings/SettingRow";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { optionsFor } from "@/lib/settingsDisplay";
import { NAMED_LANGUAGE_OPTIONS } from "@/lib/languageOptions";
import { SETTING_DEFINITIONS, SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { QUALITY_PRESETS, describeQuality, presetById, presetIdFor } from "@/lib/qualityPresets";
import { useEffectiveSettings } from "@/hooks/queries/settingValues";
import { useAutoPlayNextSetting } from "@/hooks/queries/autoPlayNext";
import { useProfileDefaultWriter } from "@/hooks/queries/profileDefaults";
import { toast } from "sonner";

/**
 * Every preference on this screen is the profile's own choice. A device, a
 * library, or a series can override several of them, and the contract resolves
 * those above the profile row — so useProfileDefaultWriter writes the profile
 * layer and clears any device override that would otherwise keep shadowing it.
 */
/**
 * Read in one batch: the screen wants every key at once.
 *
 * playback.auto_play_next is deliberately absent — it is read and written
 * through useAutoPlayNextSetting, shared with the post-roll toggle so the two
 * surfaces cannot land on different scopes.
 */
const PLAYBACK_KEYS: SettingKey[] = [
  SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE,
  SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT_PREVIEW,
  SETTING_KEYS.PLAYBACK_AUTO_SKIP_CREDITS,
  SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO,
  SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP,
  SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
  SETTING_KEYS.UI_NEXT_UP_MODE,
];

const NEXT_UP_MODES = optionsFor(SETTING_DEFINITIONS[SETTING_KEYS.UI_NEXT_UP_MODE]);

/**
 * Quality is two settings behind one picker.
 *
 * The server stores a resolution cap and a bandwidth cap independently, which
 * is what the player has always sent on the wire. Presenting them as one list
 * keeps the choice simple while leaving the two axes free: a preset is a
 * client-side pairing, so retuning what "High" means never needs the server to
 * agree, and someone who sets the axes separately through the API still gets a
 * truthful label rather than a picker showing the wrong entry.
 */
function QualitySetting() {
  const { data: effective } = useEffectiveSettings({
    keys: ["playback.preferred_quality", "playback.max_bitrate_kbps"],
  });
  const { save, reset, isSaving } = useProfileDefaultWriter(effective);

  const resolution = effective?.["playback.preferred_quality"]?.value as string | undefined;
  const bitrate = effective?.["playback.max_bitrate_kbps"]?.value as number | null | undefined;
  const selected = presetIdFor(resolution, bitrate);

  const apply = (presetId: string) => {
    const preset = presetById(presetId);
    if (!preset) return;

    save("playback.preferred_quality", preset.resolution).catch(() =>
      toast.error("Failed to save video quality"),
    );

    // An uncapped preset clears the bitrate rather than storing a sentinel, so
    // "no cap" stays the absence of a value at every layer. Both axes are
    // device-overridable, so the reset clears the device row too — otherwise
    // an override would keep capping playback after the picker said it did not.
    if (preset.bitrateKbps === null) {
      reset("playback.max_bitrate_kbps").catch(() =>
        toast.error("Failed to clear the bitrate cap"),
      );
    } else {
      save("playback.max_bitrate_kbps", preset.bitrateKbps).catch(() =>
        toast.error("Failed to save maximum bitrate"),
      );
    }
  };

  const pending = isSaving;

  return (
    <SettingRow
      label="Video quality"
      description="Choose the quality your profile should request when playback begins."
      control={(id) => (
        <div className="w-full">
          <Select value={selected ?? "__custom"} onValueChange={apply}>
            <SelectTrigger id={id} className="w-full sm:w-[260px]" disabled={pending}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {selected === null && (
                <SelectItem value="__custom" disabled>
                  {describeQuality(resolution, bitrate)}
                </SelectItem>
              )}
              {QUALITY_PRESETS.map((preset) => (
                <SelectItem key={preset.id} value={preset.id}>
                  {preset.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-muted-foreground mt-1.5 text-xs">
            {describeQuality(resolution, bitrate)}
          </p>
        </div>
      )}
    />
  );
}

/**
 * Auto-play shares its hook with the post-roll toggle.
 *
 * Both surfaces render the resolved value, and the contract resolves
 * profile_device above profile — so if this screen wrote the profile row while
 * the player wrote the device row, this switch would save a value the device
 * row keeps shadowing and snap straight back. The shared hook writes the
 * profile and clears any device row, which is also the only way a migrated
 * per-device override becomes reachable from the web UI.
 */
function AutoPlayNextSetting() {
  const { enabled, setEnabled, isSaving } = useAutoPlayNextSetting();

  return (
    <SettingRow
      label="Auto-play next episode"
      description="Start the next episode automatically after the current one ends."
      control={(id) => (
        <Switch
          id={id}
          checked={enabled}
          disabled={isSaving}
          onCheckedChange={(checked) => {
            setEnabled(checked).catch(() => toast.error("Failed to save playback setting"));
          }}
        />
      )}
    />
  );
}

export default function PlaybackSettings() {
  const { data: effective } = useEffectiveSettings({ keys: PLAYBACK_KEYS });
  const {
    save: saveProfileDefault,
    reset: resetProfileDefault,
    isSaving,
  } = useProfileDefaultWriter(effective);

  // The effective endpoint resolves an unset key to the contract default, so
  // every control below reads its value from the same answer rather than from
  // a local literal that could disagree with the server about "unset".
  const read = <T,>(key: SettingKey) =>
    (effective?.[key]?.value ?? SETTING_DEFINITIONS[key].defaultValue) as T;

  // Saves at profile scope and clears any device override shadowing it —
  // several of these keys are device-overridable, and without the clear the
  // control would snap back to the override it cannot see.
  const saveValue = (key: SettingKey, value: unknown) => {
    saveProfileDefault(key, value).catch(() => toast.error("Failed to save playback setting"));
  };

  const nextUpMode = read<string>(SETTING_KEYS.UI_NEXT_UP_MODE);
  // Whether the profile has actually chosen, which is what gates the reset
  // affordance: the resolved value is the default until a row exists.
  const nextUpChosen = effective?.[SETTING_KEYS.UI_NEXT_UP_MODE]?.source === "profile";
  const pending = isSaving;

  async function resetNextUpMode() {
    // Nothing stored is the state a reset asks for, which the shared reset
    // already treats as success.
    await resetProfileDefault(SETTING_KEYS.UI_NEXT_UP_MODE).catch(() =>
      toast.error("Failed to reset the next up preference"),
    );
  }

  return (
    <div className="space-y-6">
      <div className="space-y-4">
        <div className="space-y-3">
          <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Playback</h2>
          <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
            Choose the defaults Silo should use when playback starts.
          </p>
        </div>
      </div>

      <SettingsGroup
        title="Defaults"
        description="These preferences apply unless a library or item has a more specific playback choice."
      >
        <QualitySetting />

        <SettingRow
          label="Spoken language"
          description="Prefer a spoken language for this profile when multiple tracks are available."
          control={(id) => (
            <div className="w-full">
              <Select
                value={read<string | null>(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE) ?? "none"}
                onValueChange={(value) =>
                  // The contract spells "no preference" as null, where the
                  // legacy profile column spelled it as the empty string.
                  saveValue(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE, value === "none" ? null : value)
                }
              >
                <SelectTrigger id={id} className="w-full sm:w-[220px]" disabled={pending}>
                  <SelectValue placeholder="No preference" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">No preference</SelectItem>
                  {NAMED_LANGUAGE_OPTIONS.map((language) => (
                    <SelectItem key={language.value} value={language.value}>
                      {language.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
        />

        <SettingRow
          label="Metadata language"
          description="Show titles and descriptions in this language when available. Descriptions can be translated automatically when the server has AI translation enabled."
          control={(id) => (
            <div className="w-full">
              <Select
                value={read<string | null>(SETTING_KEYS.CATALOG_METADATA_LANGUAGE) ?? "none"}
                onValueChange={(value) =>
                  saveValue(SETTING_KEYS.CATALOG_METADATA_LANGUAGE, value === "none" ? null : value)
                }
              >
                <SelectTrigger id={id} className="w-full sm:w-[220px]" disabled={pending}>
                  <SelectValue placeholder="Library default" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">Library default</SelectItem>
                  {NAMED_LANGUAGE_OPTIONS.map((language) => (
                    <SelectItem key={language.value} value={language.value}>
                      {language.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
        />

        <SettingRow
          label="Auto-skip intros"
          description="Jump past intros automatically when Silo can detect them."
          control={(id) => (
            <Switch
              id={id}
              checked={read<boolean>(SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO)}
              disabled={pending}
              onCheckedChange={(checked) =>
                saveValue(SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO, checked)
              }
            />
          )}
        />

        <SettingRow
          label="Auto-skip credits"
          description="Move through end credits automatically when a skip is available."
          control={(id) => (
            <Switch
              id={id}
              checked={read<boolean>(SETTING_KEYS.PLAYBACK_AUTO_SKIP_CREDITS)}
              disabled={pending}
              onCheckedChange={(checked) =>
                saveValue(SETTING_KEYS.PLAYBACK_AUTO_SKIP_CREDITS, checked)
              }
            />
          )}
        />

        <SettingRow
          label="Auto-skip recaps"
          description="Skip 'previously on…' recaps automatically when Silo can detect them."
          control={(id) => (
            <Switch
              id={id}
              checked={read<boolean>(SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP)}
              disabled={pending}
              onCheckedChange={(checked) =>
                saveValue(SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP, checked)
              }
            />
          )}
        />

        <SettingRow
          label="Start next at preview"
          description="Begin the next episode when the current one reaches its next-episode preview teaser, rather than waiting for the end credits."
          control={(id) => (
            <Switch
              id={id}
              checked={read<boolean>(SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT_PREVIEW)}
              disabled={pending}
              onCheckedChange={(checked) =>
                saveValue(SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT_PREVIEW, checked)
              }
            />
          )}
        />

        <AutoPlayNextSetting />

        <SettingRow
          label="Next up episodes"
          description="Choose whether upcoming episodes stay with Continue Watching or get their own row."
          control={(id) => (
            <div className="flex items-center gap-2">
              <Select
                value={nextUpMode}
                onValueChange={(value) => saveValue(SETTING_KEYS.UI_NEXT_UP_MODE, value)}
                disabled={pending}
              >
                <SelectTrigger id={id} className="w-full sm:w-[240px]">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {NEXT_UP_MODES.map((mode) => (
                    <SelectItem key={mode.value} value={mode.value}>
                      {mode.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {nextUpChosen ? (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-9 rounded-full px-3"
                  onClick={resetNextUpMode}
                  disabled={pending}
                >
                  Reset
                </Button>
              ) : null}
            </div>
          )}
        />
      </SettingsGroup>
    </div>
  );
}
