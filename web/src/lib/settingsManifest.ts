export type SettingScope = "user" | "device";
export type SettingControl = "switch" | "select" | "slider" | "json";

export interface SettingOption {
  value: string;
  label: string;
}

export interface SettingDefinition {
  key: string;
  scope: SettingScope;
  label: string;
  description: string;
  control: SettingControl;
  defaultValue?: string;
  options?: SettingOption[];
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  summary?: (value: string | null | undefined) => string;
}

export const LANGUAGE_OPTIONS: SettingOption[] = [
  { value: "", label: "No preference" },
  { value: "en", label: "English" },
  { value: "es", label: "Spanish" },
  { value: "fr", label: "French" },
  { value: "de", label: "German" },
  { value: "it", label: "Italian" },
  { value: "pt", label: "Portuguese" },
  { value: "ja", label: "Japanese" },
  { value: "ko", label: "Korean" },
  { value: "zh", label: "Chinese" },
  { value: "ru", label: "Russian" },
  { value: "ar", label: "Arabic" },
  { value: "hi", label: "Hindi" },
];

const definitions: SettingDefinition[] = [
  {
    key: "playback.preferred_quality",
    scope: "device",
    label: "Preferred quality",
    description: "Pick the quality Silo should prefer on this device for the current profile.",
    control: "select",
    defaultValue: "auto",
    options: [
      { value: "auto", label: "Auto" },
      { value: "original", label: "Original quality" },
      { value: "2160p", label: "2160p / 4K" },
      { value: "1080p", label: "1080p" },
      { value: "720p", label: "720p" },
      { value: "480p", label: "480p" },
    ],
  },
  {
    key: "playback.audio_language",
    scope: "device",
    label: "Preferred audio language",
    description: "Choose which spoken language Silo should prefer first on this device.",
    control: "select",
    options: LANGUAGE_OPTIONS,
    summary: (value) =>
      LANGUAGE_OPTIONS.find((option) => option.value === (value ?? ""))?.label ?? (value || "None"),
  },
  {
    key: "playback.auto_skip_intro",
    scope: "device",
    label: "Auto-skip intros",
    description: "Jump past intros automatically on this device when Silo can detect them.",
    control: "switch",
    defaultValue: "false",
  },
  {
    key: "playback.auto_skip_credits",
    scope: "device",
    label: "Auto-skip credits",
    description: "Move through end credits automatically on this device when a skip is available.",
    control: "switch",
    defaultValue: "false",
  },
  {
    key: "playback.auto_skip_recap",
    scope: "device",
    label: "Auto-skip recaps",
    description: "Skip 'previously on…' recaps automatically when Silo can detect them.",
    control: "switch",
    defaultValue: "false",
  },
  {
    key: "playback.auto_play_next_preview",
    scope: "device",
    label: "Start next episode at preview",
    description:
      "Begin playing the next episode when the current one reaches its next-episode preview teaser, rather than waiting for the end credits.",
    control: "switch",
    defaultValue: "false",
  },
  {
    key: "playback.auto_play_next",
    scope: "device",
    label: "Auto-play next episode",
    description: "Start the next episode automatically on this device after the current one ends.",
    control: "switch",
    defaultValue: "true",
  },
  {
    key: "playback.next_up_prompt_seconds",
    scope: "device",
    label: "Next Up prompt",
    description: "Choose when the Next Up screen should appear near the end of playback.",
    control: "select",
    defaultValue: "30",
    options: [
      { value: "0", label: "At end" },
      { value: "10", label: "10 seconds before end" },
      { value: "30", label: "30 seconds before end" },
      { value: "60", label: "1 minute before end" },
      { value: "120", label: "2 minutes before end" },
    ],
  },
  {
    key: "subtitle_appearance",
    scope: "device",
    label: "Subtitle appearance",
    description: "Customize subtitles on this device.",
    control: "json",
    summary: (value) => (value ? "Custom subtitle appearance" : "Using fallback"),
  },
  {
    key: "player.hdr_enabled",
    scope: "device",
    label: "HDR enabled",
    description: "Allow HDR playback on this device when the player and display support it.",
    control: "switch",
    defaultValue: "true",
  },
  {
    key: "player.dv_profile7_hdr10_fallback",
    scope: "device",
    label: "Profile 7 HDR10 fallback",
    description: "Play Dolby Vision Profile 7 as the HDR10 base layer on this device.",
    control: "switch",
    defaultValue: "false",
  },
  {
    key: "player.playback_speed",
    scope: "device",
    label: "Playback speed",
    description: "Choose the default playback speed for this device.",
    control: "select",
    defaultValue: "1",
    options: [
      { value: "0.25", label: "0.25x" },
      { value: "0.5", label: "0.5x" },
      { value: "0.75", label: "0.75x" },
      { value: "1", label: "1x (Normal)" },
      { value: "1.25", label: "1.25x" },
      { value: "1.5", label: "1.5x" },
      { value: "1.75", label: "1.75x" },
      { value: "2", label: "2x" },
      { value: "2.5", label: "2.5x" },
      { value: "3", label: "3x" },
    ],
    summary: (value) => `${value ?? "1"}x`,
  },
  {
    key: "player.audio_sync_ms",
    scope: "device",
    label: "Audio sync",
    description: "Offset audio timing on this device.",
    control: "slider",
    defaultValue: "0",
    min: -5000,
    max: 5000,
    step: 50,
    unit: "ms",
    summary: (value) => `${value ?? "0"} ms`,
  },
  {
    key: "player.subtitle_sync_ms",
    scope: "device",
    label: "Subtitle sync",
    description: "Offset subtitle timing on this device.",
    control: "slider",
    defaultValue: "0",
    min: -10000,
    max: 10000,
    step: 50,
    unit: "ms",
    summary: (value) => `${value ?? "0"} ms`,
  },
  {
    key: "player.video_gravity",
    scope: "device",
    label: "Video fit",
    description: "Control how video should fill the screen on this device.",
    control: "select",
    defaultValue: "fit",
    options: [
      { value: "fit", label: "Fit" },
      { value: "fill", label: "Fill" },
      { value: "stretch", label: "Stretch" },
    ],
  },
  {
    key: "player.orientation_mode",
    scope: "device",
    label: "Orientation mode",
    description: "Choose whether playback stays locked or rotates freely on this device.",
    control: "select",
    defaultValue: "landscapeLocked",
    options: [
      { value: "landscapeLocked", label: "Landscape locked" },
      { value: "rotateFreely", label: "Rotate freely" },
    ],
  },
];

export const settingsManifest = definitions.reduce<Record<string, SettingDefinition>>(
  (acc, definition) => {
    acc[definition.key] = definition;
    return acc;
  },
  {},
);

export const USER_SETTING_KEYS = definitions
  .filter((definition) => definition.scope === "user")
  .map((definition) => definition.key);

export const DEVICE_SETTING_KEYS = definitions
  .filter((definition) => definition.scope === "device" && definition.key !== "subtitle_appearance")
  .map((definition) => definition.key);

/**
 * Every device-scoped setting in canonical manifest order, including the
 * subtitle_appearance JSON setting. The admin "show all overrides" view
 * iterates this list so admins can set an override on any setting,
 * regardless of whether one already exists.
 */
export const ALL_DEVICE_SETTING_KEYS: string[] = definitions
  .filter((definition) => definition.scope === "device")
  .map((definition) => definition.key);

export function listDeviceSettingDefinitions(): SettingDefinition[] {
  return definitions.filter((definition) => definition.scope === "device");
}

export function getSettingDefinition(key: string): SettingDefinition | null {
  return settingsManifest[key] ?? null;
}

export function formatSettingValue(key: string, value: string | null | undefined): string {
  const definition = getSettingDefinition(key);
  if (!definition) {
    return value ?? "Unset";
  }
  if (definition.summary) {
    return definition.summary(value);
  }
  if (definition.control === "switch") {
    return value === "true" ? "Enabled" : "Disabled";
  }
  if (definition.options) {
    return (
      definition.options.find((option) => option.value === (value ?? definition.defaultValue))
        ?.label ??
      value ??
      definition.defaultValue ??
      "Unset"
    );
  }
  if (definition.unit) {
    return `${value ?? definition.defaultValue ?? "0"} ${definition.unit}`;
  }
  return value ?? definition.defaultValue ?? "Unset";
}
