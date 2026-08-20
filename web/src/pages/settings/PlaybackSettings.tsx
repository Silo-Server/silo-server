import { useMemo } from "react";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { SettingRow } from "@/components/settings/SettingRow";
import { LanguageSelect } from "@/components/settings/LanguageSelect";
import { MetadataLanguageSetting } from "@/components/settings/MetadataLanguageSetting";
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
import { namedLanguageOptionsFor } from "@/lib/languageOptions";
import {
  normalizeMetadataLanguageOverrides,
  type MetadataLanguageOverrides,
} from "@/lib/metadataLanguagePreferences";
import { SETTING_DEFINITIONS, SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { bitrateSelectChoices } from "@/lib/bitrateOptions";
import {
  settingsCapabilitiesSupportKey,
  useEffectiveSettings,
  useSettingsCapabilities,
} from "@/hooks/queries/settingValues";
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
const BASE_PLAYBACK_KEYS: SettingKey[] = [
  SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE,
  SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT_PREVIEW,
  SETTING_KEYS.PLAYBACK_AUTO_SKIP_CREDITS,
  SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP,
  SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
  SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES,
  SETTING_KEYS.UI_NEXT_UP_MODE,
];

const NEXT_UP_MODES = optionsFor(SETTING_DEFINITIONS[SETTING_KEYS.UI_NEXT_UP_MODE]);
const INTRO_SKIP_MODES = optionsFor(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE]);

// Radix Select cannot represent "" as an item value, so the unset entry needs
// a sentinel that never collides with a stored bitrate.
const NO_BITRATE_LIMIT = "__no_limit__";

/**
 * Quality is the same two settings every other surface edits.
 *
 * The server stores a resolution cap and a bandwidth cap independently, and
 * the device screen already presents them as two controls. Offering the same
 * two rows here means a device override reads as an override of something the
 * user can see, rather than of half a compound preset — and combinations no
 * preset covered ("Original but capped at 40 Mbps" for a remote box) become
 * expressible instead of rendering as a disabled "custom" entry.
 */
function QualitySetting() {
  const { data: effective } = useEffectiveSettings({
    keys: [SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY, SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS],
  });
  const { save, reset, isSaving } = useProfileDefaultWriter(effective);

  const qualityDefinition = SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY];
  const bitrateDefinition = SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS];

  const resolution = (effective?.[SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY]?.value ??
    qualityDefinition.defaultValue) as string;
  const bitrate = effective?.[SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS]?.value as
    | number
    | null
    | undefined;
  const bitrateValue = bitrate == null ? "" : String(bitrate);
  const bitrateChoices = bitrateSelectChoices(bitrateDefinition, bitrateValue, "No limit");

  return (
    <>
      <SettingRow
        label="Preferred quality"
        description="The resolution your profile should request when playback begins. Auto lets Silo pick based on your connection."
        control={(id) => (
          <Select
            value={resolution}
            disabled={isSaving}
            onValueChange={(value) =>
              save(SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY, value).catch(() =>
                toast.error("Failed to save preferred quality"),
              )
            }
          >
            <SelectTrigger id={id} className="w-full sm:w-[220px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {optionsFor(qualityDefinition).map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      />

      <SettingRow
        label="Maximum bitrate"
        description="Cap how much bandwidth playback may use. No limit means Silo picks for the chosen resolution."
        control={(id) => (
          <Select
            value={bitrateValue === "" ? NO_BITRATE_LIMIT : bitrateValue}
            disabled={isSaving}
            onValueChange={(next) => {
              // "No limit" clears the rows rather than storing a sentinel, so
              // "no cap" stays the absence of a value at every layer. The reset
              // clears the device row too — otherwise an override would keep
              // capping playback after this control said it did not.
              const request =
                next === NO_BITRATE_LIMIT
                  ? reset(SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS)
                  : save(SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS, Number(next));
              request.catch(() => toast.error("Failed to save maximum bitrate"));
            }}
          >
            <SelectTrigger id={id} className="w-full sm:w-[220px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {bitrateChoices.map((choice) => (
                <SelectItem
                  key={choice.value || NO_BITRATE_LIMIT}
                  value={choice.value === "" ? NO_BITRATE_LIMIT : choice.value}
                >
                  {choice.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      />
    </>
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
  const capabilities = useSettingsCapabilities();
  const supportsIntroSkipMode = settingsCapabilitiesSupportKey(
    capabilities.data,
    SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE,
  );
  /**
   * Which intro control this server can honestly show.
   *
   * The legacy switch is not a safe default while the answer is unknown. On a
   * revision-7 server a profile set to `never` is mirrored into the deprecated
   * boolean as false; touching the switch would write that key, and the
   * server's mirror would turn a deliberate `never` into `ask` or `always`
   * with no way back. So the fallback is only reached once a successful
   * capabilities response has proved the enum is genuinely unavailable — a
   * failed request means unknown, not old.
   */
  const introSkipControl: "mode" | "legacy" | "unknown" = supportsIntroSkipMode
    ? "mode"
    : capabilities.isSuccess
      ? "legacy"
      : "unknown";
  const playbackKeys = useMemo(
    () => [
      ...BASE_PLAYBACK_KEYS,
      supportsIntroSkipMode
        ? SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE
        : SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO,
    ],
    [supportsIntroSkipMode],
  );
  const { data: effective } = useEffectiveSettings({ keys: playbackKeys });
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
  const audioLanguage = read<string | null>(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE);
  const metadataLanguage = read<string | null>(SETTING_KEYS.CATALOG_METADATA_LANGUAGE);
  const audioLanguageOptions = namedLanguageOptionsFor(
    SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE,
    audioLanguage,
    effective?.[SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE]?.suggested_values,
  );
  const metadataLanguageOptions = namedLanguageOptionsFor(
    SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
    metadataLanguage,
    effective?.[SETTING_KEYS.CATALOG_METADATA_LANGUAGE]?.suggested_values,
  );
  // Whether the profile has actually chosen, which is what gates the reset
  // affordance: the resolved value is the default until a row exists.
  const nextUpChosen = effective?.[SETTING_KEYS.UI_NEXT_UP_MODE]?.source === "profile";
  const pending = isSaving;

  const saveMetadataOverrides = (overrides: MetadataLanguageOverrides) => {
    const request =
      Object.keys(overrides).length === 0
        ? resetProfileDefault(SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES)
        : saveProfileDefault(SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES, overrides);
    return request.catch((error) => {
      toast.error("Failed to save metadata language exceptions");
      throw error;
    });
  };

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
            <div className="w-full sm:w-[220px]">
              <LanguageSelect
                id={id}
                value={audioLanguage ?? "none"}
                options={audioLanguageOptions}
                disabled={pending}
                placeholder="No preference"
                className="w-full"
                onValueChange={(value) =>
                  // The contract spells "no preference" as null, where the
                  // legacy profile column spelled it as the empty string.
                  saveValue(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE, value === "none" ? null : value)
                }
              >
                <SelectItem value="none">No preference</SelectItem>
              </LanguageSelect>
            </div>
          )}
        />

        <MetadataLanguageSetting
          fallback={metadataLanguage}
          overrides={normalizeMetadataLanguageOverrides(
            read<unknown>(SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES),
          )}
          languageOptions={metadataLanguageOptions}
          disabled={pending}
          onFallbackChange={(language) =>
            saveValue(SETTING_KEYS.CATALOG_METADATA_LANGUAGE, language)
          }
          onOverridesChange={saveMetadataOverrides}
        />

        {introSkipControl === "mode" ? (
          <SettingRow
            label="Skip intros"
            description="Leave intros alone, offer a Skip Intro button, or skip automatically with an undo."
            control={(id) => (
              <Select
                value={read<string>(SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE)}
                disabled={pending}
                onValueChange={(value) => saveValue(SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE, value)}
              >
                <SelectTrigger id={id} className="w-full sm:w-[220px]">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {INTRO_SKIP_MODES.map((mode) => (
                    <SelectItem key={mode.value} value={mode.value}>
                      {mode.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          />
        ) : introSkipControl === "unknown" ? (
          // Neither control can be rendered truthfully yet, so the row keeps
          // its place and asserts no value at all rather than showing a switch
          // whose position would be a guess the user could act on.
          <SettingRow
            label="Skip intros"
            description="Available once Silo has checked what this server supports."
            control={(id) => (
              <Select disabled>
                <SelectTrigger id={id} className="w-full sm:w-[220px]">
                  <SelectValue placeholder="Unavailable" />
                </SelectTrigger>
                <SelectContent>
                  {INTRO_SKIP_MODES.map((mode) => (
                    <SelectItem key={mode.value} value={mode.value}>
                      {mode.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          />
        ) : (
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
        )}

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
