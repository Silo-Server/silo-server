import { useId, useState, type ReactNode } from "react";
import { Minus, Plus } from "lucide-react";
import { PageHeader } from "@/components/page/PageHeader";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { LanguageSelect } from "@/components/settings/LanguageSelect";
import { MetadataLanguageSetting } from "@/components/settings/MetadataLanguageSetting";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { usePercentDraft } from "@/hooks/usePercentDraft";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSubtitleAppearanceSetting } from "@/hooks/queries/subtitleAppearance";
import { SUBTITLE_SYNC_MAX_MS, useSubtitleSyncSetting } from "@/hooks/queries/subtitleSync";
import { useEffectiveSettings } from "@/hooks/queries/settingValues";
import { useProfileDefaultWriter } from "@/hooks/queries/profileDefaults";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { optionsFor } from "@/lib/settingsDisplay";
import { namedLanguageOptionsFor } from "@/lib/languageOptions";
import {
  normalizeMetadataLanguageOverrides,
  type MetadataLanguageOverrides,
} from "@/lib/metadataLanguagePreferences";
import { SETTING_DEFINITIONS } from "@/lib/settingsContract";
import {
  BACKGROUND_STYLE_OPTIONS,
  BG_COLOR_PALETTE,
  computeSubtitleStyles,
  FONT_COLOR_PALETTE,
  FONT_FAMILY_OPTIONS,
  FONT_SIZE_OPTIONS,
  POSITION_OPTIONS,
} from "@/lib/subtitleAppearance";
import type { SubtitleAppearance } from "@/lib/subtitleAppearance";
import { toast } from "sonner";

import { ResetButton } from "./ResetButton";

/* Languages, subtitle behavior and subtitle appearance are profile preferences. */
/**
 * The behavior modes come from the contract rather than a literal list, so a
 * member added to the manifest appears here without a matching edit.
 */
const SUBTITLE_MODES = optionsFor(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SUBTITLE_MODE]);

/** Read in one batch: the language and subtitle rows want every key at once. */
const PROFILE_KEYS: SettingKey[] = [
  SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE,
  SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
  SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES,
  SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE,
  SETTING_KEYS.PLAYBACK_SUBTITLE_MODE,
  SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES,
];

interface ColorPaletteProps {
  colors: { hex: string; label: string }[];
  selected: string;
  onChange: (hex: string) => void;
  disabled?: boolean;
  labelId?: string;
  descriptionId?: string;
}

function ColorPalette({
  colors,
  selected,
  onChange,
  disabled,
  labelId,
  descriptionId,
}: ColorPaletteProps) {
  return (
    <div
      role="group"
      aria-labelledby={labelId}
      aria-describedby={descriptionId}
      className={`flex flex-wrap gap-2 ${disabled ? "opacity-40" : ""}`}
    >
      {colors.map((color) => (
        <button
          key={color.hex}
          type="button"
          title={color.label}
          aria-label={color.label}
          onClick={() => onChange(color.hex)}
          disabled={disabled}
          className="h-7 w-7 rounded-full border-2 transition-transform hover:scale-110"
          style={{
            backgroundColor: color.hex,
            borderColor: selected === color.hex ? "var(--primary)" : "transparent",
            boxShadow:
              color.hex === "#000000" ? "inset 0 0 0 1px rgba(255,255,255,0.2)" : undefined,
          }}
        />
      ))}
    </div>
  );
}

/** A typed percentage value (1-100). A slider makes the low end — where a few
 * percent is the difference between legible and invisible — fiddly to hit;
 * typing the number directly doesn't have that problem. */
function PercentInput({
  value,
  onChange,
  min = 1,
  disabled,
  id,
  descriptionId,
}: {
  value: number;
  onChange: (v: number) => void;
  min?: number;
  disabled?: boolean;
  id?: string;
  descriptionId?: string;
}) {
  const { draft, setDraft, commit } = usePercentDraft(value, min, onChange);

  return (
    <div className="flex items-center gap-2">
      <Input
        id={id}
        aria-describedby={descriptionId}
        type="number"
        inputMode="numeric"
        min={min}
        max={100}
        disabled={disabled}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={(e) => commit(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") e.currentTarget.blur();
        }}
        className="w-20 [appearance:textfield] text-right [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none"
      />
      <span className="text-muted-foreground text-sm">%</span>
    </div>
  );
}

interface SettingRowProps {
  label: string;
  description?: string;
  labelForControl?: boolean;
  children: (props: { id: string; labelId: string; descriptionId: string }) => ReactNode;
}

function SettingRow({ label, description, labelForControl = true, children }: SettingRowProps) {
  const controlId = useId();
  const labelId = useId();
  const descriptionId = useId();

  return (
    <div className="border-border/50 grid gap-3 border-t pt-4 first:border-t-0 first:pt-0 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
      <div className="min-w-0 space-y-0.5">
        <Label
          id={labelId}
          htmlFor={labelForControl ? controlId : undefined}
          className="text-sm font-medium"
        >
          {label}
        </Label>
        {description ? (
          <p id={descriptionId} className="text-muted-foreground text-[13px] leading-relaxed">
            {description}
          </p>
        ) : null}
      </div>
      <div className="flex md:justify-end">
        {children({ id: controlId, labelId, descriptionId })}
      </div>
    </div>
  );
}

export default function SubtitleAppearanceSettings() {
  const {
    appearance: effectiveSettings,
    isCustomized,
    save: saveAppearance,
    reset: resetAppearance,
    isSaving,
  } = useSubtitleAppearanceSetting();
  const subtitleSync = useSubtitleSyncSetting();
  const { data: profileValues } = useEffectiveSettings({ keys: PROFILE_KEYS });
  const {
    save: saveProfileDefault,
    reset: resetProfileDefault,
    isSaving: profilePending,
  } = useProfileDefaultWriter(profileValues);

  // The effective endpoint resolves an unset key to the contract default, so a
  // control can read its value straight off the answer without a local literal.
  const read = <T,>(key: SettingKey) =>
    (profileValues?.[key]?.value ?? SETTING_DEFINITIONS[key].defaultValue) as T;
  const audioLanguage = read<string | null>(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE);
  const metadataLanguage = read<string | null>(SETTING_KEYS.CATALOG_METADATA_LANGUAGE);
  const audioLanguageOptions = namedLanguageOptionsFor(
    SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE,
    audioLanguage,
    profileValues?.[SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE]?.suggested_values,
  );
  const metadataLanguageOptions = namedLanguageOptionsFor(
    SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
    metadataLanguage,
    profileValues?.[SETTING_KEYS.CATALOG_METADATA_LANGUAGE]?.suggested_values,
  );
  const subtitleLanguage =
    (profileValues?.[SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE]?.value as
      | string
      | null
      | undefined) ?? "";
  const subtitleLanguageOptions = namedLanguageOptionsFor(
    SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE,
    subtitleLanguage,
    profileValues?.[SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE]?.suggested_values,
  );
  const subtitleMode =
    (profileValues?.[SETTING_KEYS.PLAYBACK_SUBTITLE_MODE]?.value as string | undefined) ?? "auto";
  const showForcedSubtitles =
    (profileValues?.[SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES]?.value as boolean | undefined) ??
    true;

  const [draftState, setDraftState] = useState<{
    key: string;
    settings: SubtitleAppearance;
  }>({
    key: JSON.stringify(effectiveSettings),
    settings: effectiveSettings,
  });

  const baselineKey = JSON.stringify(effectiveSettings);
  const settings = draftState.key === baselineKey ? draftState.settings : effectiveSettings;

  function update<K extends keyof SubtitleAppearance>(key: K, value: SubtitleAppearance[K]) {
    setDraftState((prev) => ({
      key: baselineKey,
      settings: {
        ...(prev.key === baselineKey ? prev.settings : effectiveSettings),
        [key]: value,
      },
    }));
  }

  function discardLocalChanges() {
    setDraftState({
      key: baselineKey,
      settings: effectiveSettings,
    });
  }

  async function handleSave() {
    try {
      await saveAppearance(settings);
      toast.success("Subtitle appearance saved");
    } catch {
      toast.error("Failed to save subtitle appearance");
    }
  }

  // Clears the profile's look (and this browser's own copy), so the built-in
  // one applies on every device again. Any unsaved edits go with it.
  async function handleResetAppearance() {
    try {
      await resetAppearance();
      discardLocalChanges();
      toast.success("Subtitle appearance reset to the default");
    } catch {
      toast.error("Failed to reset subtitle appearance");
    }
  }

  /**
   * Languages and subtitle behavior write at profile scope: they are the
   * household member's choice, not the screen's. A device override would
   * otherwise keep shadowing the write and snap the control back, so the
   * shared writer clears one when the resolved value came from this device. A
   * library or series override still ranks above the profile row, but those
   * are edited where the content is and stay deliberately untouched here.
   */
  function saveBehavior(key: SettingKey, value: unknown) {
    saveProfileDefault(key, value).catch(() => toast.error("Failed to save subtitle setting"));
  }

  function saveLanguage(key: SettingKey, value: unknown) {
    saveProfileDefault(key, value).catch(() => toast.error("Failed to save language setting"));
  }

  function saveMetadataOverrides(overrides: MetadataLanguageOverrides) {
    const request =
      Object.keys(overrides).length === 0
        ? resetProfileDefault(SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES)
        : saveProfileDefault(SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES, overrides);
    return request.catch((error) => {
      toast.error("Failed to save metadata language exceptions");
      throw error;
    });
  }

  const hasUnsavedChanges = JSON.stringify(settings) !== JSON.stringify(effectiveSettings);
  const usesTextOutline = settings.textOutline || settings.backgroundStyle === "outline";
  const isBoxStyle = settings.backgroundStyle === "box";
  const { containerStyle, cueStyle } = computeSubtitleStyles(settings);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Language & Subtitles"
        subtitle="The languages this profile prefers, when subtitles appear, and how they look."
      />

      <SettingsGroup title="Language">
        <SettingRow
          label="Spoken language"
          description="Prefer a spoken language for this profile when multiple tracks are available."
        >
          {({ id, descriptionId }) => (
            <div className="w-full sm:w-[220px]">
              <LanguageSelect
                id={id}
                aria-describedby={descriptionId}
                value={audioLanguage ?? "none"}
                options={audioLanguageOptions}
                disabled={profilePending}
                placeholder="No preference"
                className="w-full"
                onValueChange={(value) =>
                  // The contract spells "no preference" as null, where the
                  // legacy profile column spelled it as the empty string.
                  saveLanguage(
                    SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE,
                    value === "none" ? null : value,
                  )
                }
              >
                <SelectItem value="none">No preference</SelectItem>
              </LanguageSelect>
            </div>
          )}
        </SettingRow>

        <MetadataLanguageSetting
          fallback={metadataLanguage}
          overrides={normalizeMetadataLanguageOverrides(
            read<unknown>(SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES),
          )}
          languageOptions={metadataLanguageOptions}
          disabled={profilePending}
          onFallbackChange={(language) =>
            saveLanguage(SETTING_KEYS.CATALOG_METADATA_LANGUAGE, language)
          }
          onOverridesChange={saveMetadataOverrides}
        />
      </SettingsGroup>

      <SettingsGroup title="Subtitles" description="Which subtitles Silo picks for this profile.">
        <SettingRow
          label="Subtitle language"
          description="Pick a subtitle language or leave subtitles off by default."
        >
          {({ id, descriptionId }) => (
            <div className="w-full sm:w-[220px]">
              <LanguageSelect
                id={id}
                aria-describedby={descriptionId}
                value={subtitleLanguage || "none"}
                options={subtitleLanguageOptions}
                disabled={profilePending}
                placeholder="None"
                className="w-full"
                onValueChange={(value) =>
                  // The contract spells "no preference" as null, not the empty
                  // string the legacy profile column used.
                  saveBehavior(
                    SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE,
                    value === "none" ? null : value,
                  )
                }
              >
                <SelectItem value="none">None</SelectItem>
              </LanguageSelect>
            </div>
          )}
        </SettingRow>

        <SettingRow label="Subtitle behavior" description="Decide when subtitles should appear.">
          {({ id, descriptionId }) => (
            <Select
              value={subtitleMode}
              onValueChange={(value) => saveBehavior(SETTING_KEYS.PLAYBACK_SUBTITLE_MODE, value)}
            >
              <SelectTrigger
                id={id}
                aria-describedby={descriptionId}
                className="w-full sm:w-[220px]"
                disabled={profilePending}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SUBTITLE_MODES.map((mode) => (
                  <SelectItem key={mode.value} value={mode.value}>
                    {mode.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </SettingRow>

        <SettingRow
          label="Show forced subtitles"
          description="Display forced subtitles for foreign-language dialogue."
        >
          {({ id, descriptionId }) => (
            <Switch
              id={id}
              aria-describedby={descriptionId}
              checked={showForcedSubtitles}
              disabled={profilePending}
              onCheckedChange={(checked) =>
                saveBehavior(SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES, checked)
              }
            />
          )}
        </SettingRow>
      </SettingsGroup>

      <SettingsGroup
        title="Preview"
        description="How subtitles look for this profile on every device."
        actions={
          isCustomized ? (
            <ResetButton
              label="Reset subtitle appearance to the default"
              disabled={isSaving}
              onClick={() => void handleResetAppearance()}
            />
          ) : null
        }
      >
        <div
          className="surface-panel-subtle relative overflow-hidden rounded-[1.3rem]"
          style={{ aspectRatio: "16 / 9", background: "linear-gradient(135deg, #0f0f1a, #1a1a3e)" }}
        >
          <div
            className="absolute inset-x-0 z-10 flex flex-col items-center gap-1"
            style={containerStyle}
          >
            <span
              className="inline-block rounded px-3 py-1 text-center leading-snug"
              style={{ ...cueStyle, whiteSpace: "pre-line" }}
            >
              Sample subtitle text
            </span>
            <span
              className="inline-block rounded px-3 py-1 text-center leading-snug"
              style={{ ...cueStyle, whiteSpace: "pre-line" }}
            >
              tuned for readability
            </span>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button onClick={handleSave} disabled={!hasUnsavedChanges || isSaving}>
            Save Appearance
          </Button>
          <Button
            variant="outline"
            onClick={discardLocalChanges}
            disabled={!hasUnsavedChanges || isSaving}
          >
            Discard Changes
          </Button>
        </div>
      </SettingsGroup>

      <SettingsGroup title="Text" description="Adjust the look and readability of subtitle text.">
        <SettingRow label="Font size">
          {({ id, descriptionId }) => (
            <Select
              value={settings.fontSize}
              onValueChange={(value) => update("fontSize", value as SubtitleAppearance["fontSize"])}
            >
              <SelectTrigger id={id} aria-describedby={descriptionId} className="w-full sm:w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {FONT_SIZE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </SettingRow>

        <SettingRow label="Font family">
          {({ id, descriptionId }) => (
            <Select
              value={settings.fontFamily}
              onValueChange={(value) =>
                update("fontFamily", value as SubtitleAppearance["fontFamily"])
              }
            >
              <SelectTrigger id={id} aria-describedby={descriptionId} className="w-full sm:w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {FONT_FAMILY_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </SettingRow>

        <SettingRow label="Font color" labelForControl={false}>
          {({ labelId, descriptionId }) => (
            <ColorPalette
              colors={FONT_COLOR_PALETTE}
              selected={settings.fontColor}
              onChange={(hex) => update("fontColor", hex)}
              labelId={labelId}
              descriptionId={descriptionId}
            />
          )}
        </SettingRow>

        <SettingRow label="Text opacity" description="How transparent the subtitle text itself is.">
          {({ id, descriptionId }) => (
            <PercentInput
              id={id}
              descriptionId={descriptionId}
              value={settings.textOpacity}
              onChange={(v) => update("textOpacity", v)}
            />
          )}
        </SettingRow>

        <SettingRow label="Text outline">
          {({ id, descriptionId }) => (
            <Switch
              id={id}
              aria-describedby={descriptionId}
              checked={settings.textOutline}
              onCheckedChange={(checked) => update("textOutline", checked)}
            />
          )}
        </SettingRow>

        <SettingRow
          label="Outline color"
          labelForControl={false}
          description="Only used when text outline is enabled."
        >
          {({ labelId, descriptionId }) => (
            <ColorPalette
              colors={FONT_COLOR_PALETTE}
              selected={settings.textOutlineColor}
              onChange={(hex) => update("textOutlineColor", hex)}
              disabled={!usesTextOutline}
              labelId={labelId}
              descriptionId={descriptionId}
            />
          )}
        </SettingRow>
      </SettingsGroup>

      <SettingsGroup
        title="Background & Position"
        description="Tune subtitle placement and contrast."
      >
        <SettingRow label="Background style">
          {({ id, descriptionId }) => (
            <Select
              value={settings.backgroundStyle}
              onValueChange={(value) =>
                update("backgroundStyle", value as SubtitleAppearance["backgroundStyle"])
              }
            >
              <SelectTrigger id={id} aria-describedby={descriptionId} className="w-full sm:w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {BACKGROUND_STYLE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </SettingRow>

        <SettingRow label="Background opacity" description="Only used for boxed subtitles.">
          {({ id, descriptionId }) => (
            <PercentInput
              id={id}
              descriptionId={descriptionId}
              min={0}
              disabled={!isBoxStyle}
              value={settings.backgroundOpacity}
              onChange={(v) => update("backgroundOpacity", v)}
            />
          )}
        </SettingRow>

        <SettingRow label="Background color" labelForControl={false}>
          {({ labelId, descriptionId }) => (
            <ColorPalette
              colors={BG_COLOR_PALETTE}
              selected={settings.backgroundColor}
              onChange={(hex) => update("backgroundColor", hex)}
              disabled={!isBoxStyle}
              labelId={labelId}
              descriptionId={descriptionId}
            />
          )}
        </SettingRow>

        <SettingRow label="Subtitle position">
          {({ id, descriptionId }) => (
            <Select
              value={settings.position}
              onValueChange={(value) => update("position", value as SubtitleAppearance["position"])}
            >
              <SelectTrigger id={id} aria-describedby={descriptionId} className="w-full sm:w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {POSITION_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </SettingRow>
      </SettingsGroup>

      <SettingsGroup
        title="Timing"
        description="Shift every subtitle earlier or later. Applies to this profile on every device."
      >
        <SettingRow
          label="Subtitle offset"
          labelForControl={false}
          description="Negative values show subtitles earlier, positive values later."
        >
          {({ labelId, descriptionId }) => (
            <SubtitleOffsetControl
              labelId={labelId}
              descriptionId={descriptionId}
              valueMs={subtitleSync.offsetMs}
              disabled={!subtitleSync.isLoaded || subtitleSync.isSaving}
              onChange={(ms) =>
                subtitleSync.save(ms).catch(() => toast.error("Failed to save subtitle offset"))
              }
            />
          )}
        </SettingRow>
      </SettingsGroup>
    </div>
  );
}

const SUBTITLE_OFFSET_STEP_MS = 100;

function formatSubtitleOffset(ms: number): string {
  if (ms === 0) return "0 ms";
  return `${ms > 0 ? "+" : "−"}${Math.abs(ms)} ms`;
}

function SubtitleOffsetControl({
  labelId,
  descriptionId,
  valueMs,
  disabled,
  onChange,
}: {
  labelId: string;
  descriptionId: string;
  valueMs: number;
  disabled: boolean;
  onChange: (ms: number) => void;
}) {
  return (
    <div
      role="group"
      aria-labelledby={labelId}
      aria-describedby={descriptionId}
      className="flex items-center gap-1"
    >
      <Button
        variant="outline"
        size="icon"
        aria-label={`Show subtitles ${SUBTITLE_OFFSET_STEP_MS} ms earlier`}
        disabled={disabled || valueMs <= -SUBTITLE_SYNC_MAX_MS}
        onClick={() => onChange(valueMs - SUBTITLE_OFFSET_STEP_MS)}
      >
        <Minus className="h-4 w-4" />
      </Button>
      <output
        aria-live="polite"
        className="min-w-[5.5rem] text-center font-mono text-sm tabular-nums"
      >
        {formatSubtitleOffset(valueMs)}
      </output>
      <Button
        variant="outline"
        size="icon"
        aria-label={`Show subtitles ${SUBTITLE_OFFSET_STEP_MS} ms later`}
        disabled={disabled || valueMs >= SUBTITLE_SYNC_MAX_MS}
        onClick={() => onChange(valueMs + SUBTITLE_OFFSET_STEP_MS)}
      >
        <Plus className="h-4 w-4" />
      </Button>
      <Button
        variant="ghost"
        size="sm"
        disabled={disabled || valueMs === 0}
        onClick={() => onChange(0)}
      >
        Reset
      </Button>
    </div>
  );
}
