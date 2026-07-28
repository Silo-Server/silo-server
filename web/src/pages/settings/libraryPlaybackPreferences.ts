import type { EffectiveSettingsMap, SettingIdentity } from "@/hooks/queries/settingValues";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { optionsFor } from "@/lib/settingsDisplay";
import { SETTING_DEFINITIONS } from "@/lib/settingsContract";
import { getLanguageName } from "@/player/utils/languageNames";

export const INHERIT_VALUE = "inherit";
export const NONE_VALUE = "none";
export const DEFAULT_SUBTITLE_MODE = "auto";
export const DEFAULT_SHOW_FORCED_SUBTITLES = true;

/** Behavior modes, from the contract rather than a parallel literal list. */
export const SUBTITLE_MODE_OPTIONS = optionsFor(
  SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SUBTITLE_MODE],
);

/**
 * The four playback preferences a library can override. All four are declared
 * at profile_library in the manifest, resolving above the device and profile
 * rows and below a per-series choice.
 */
export const LIBRARY_PLAYBACK_KEYS: SettingKey[] = [
  SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE,
  SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE,
  SETTING_KEYS.PLAYBACK_SUBTITLE_MODE,
  SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES,
];

/** The identity a per-library write addresses. */
export function libraryScope(libraryId: number): SettingIdentity {
  return { scope: "profile_library", libraryId };
}

export type LibraryPlaybackEditorState = {
  audioLanguage: string;
  subtitleLanguage: string;
  subtitleMode: string;
  showForcedSubtitles: string;
};

/** One canonical write implied by an editor change: a set, or a clear. */
export interface LibraryPlaybackMutation {
  key: SettingKey;
  /** Absent means "clear the row at this scope so the library inherits again". */
  value?: unknown;
}

export function getLanguageLabel(code: string) {
  if (!code) {
    return code;
  }
  return getLanguageName(code);
}

export function getSubtitleModeLabel(mode: string) {
  return SUBTITLE_MODE_OPTIONS.find((option) => option.value === mode)?.label ?? mode;
}

export function getSubtitleLanguageLabel(value: string) {
  return value === "" || value === NONE_VALUE ? "None" : getLanguageLabel(value);
}

export function getForcedSubtitlesLabel(value: string) {
  return value === "on" ? "On" : "Off";
}

export function buildLibraryPlaybackSummaryFromState(state: LibraryPlaybackEditorState) {
  const parts: string[] = [];

  if (state.audioLanguage !== INHERIT_VALUE) {
    parts.push(`Audio: ${getLanguageLabel(state.audioLanguage)}`);
  }
  if (state.subtitleLanguage !== INHERIT_VALUE) {
    parts.push(
      `Subtitles: ${state.subtitleLanguage === NONE_VALUE ? "None" : getLanguageLabel(state.subtitleLanguage)}`,
    );
  }
  if (state.subtitleMode !== INHERIT_VALUE) {
    parts.push(`Behavior: ${getSubtitleModeLabel(state.subtitleMode)}`);
  }
  if (state.showForcedSubtitles !== INHERIT_VALUE) {
    parts.push(`Forced subtitles: ${getForcedSubtitlesLabel(state.showForcedSubtitles)}`);
  }

  return parts.length > 0 ? parts.join(" • ") : "Uses profile defaults";
}

/**
 * Reads the editor state for one library out of a resolved settings map.
 *
 * The distinction the editor needs — "this library overrides the value" versus
 * "it inherits" — is the resolved source, not the value: a library row holding
 * the same value as the profile is still an override, and a library row holding
 * null is an explicit "no subtitles" rather than an absent choice. Reading
 * source rather than comparing values is what keeps those three cases apart.
 */
export function createLibraryPlaybackEditorState(
  effective: EffectiveSettingsMap | undefined,
): LibraryPlaybackEditorState {
  const overridden = (key: SettingKey) =>
    effective?.[key]?.source === "profile_library" ? effective[key] : undefined;

  const audio = overridden(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE);
  const subtitleLanguage = overridden(SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE);
  const subtitleMode = overridden(SETTING_KEYS.PLAYBACK_SUBTITLE_MODE);
  const forced = overridden(SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES);

  return {
    audioLanguage: (audio?.value as string | null) ?? (audio ? NONE_VALUE : INHERIT_VALUE),
    subtitleLanguage:
      (subtitleLanguage?.value as string | null) ?? (subtitleLanguage ? NONE_VALUE : INHERIT_VALUE),
    subtitleMode: (subtitleMode?.value as string | undefined) ?? INHERIT_VALUE,
    showForcedSubtitles:
      forced === undefined ? INHERIT_VALUE : (forced.value as boolean) ? "on" : "off",
  };
}

export function hasLibraryPlaybackOverride(state: LibraryPlaybackEditorState) {
  return (
    state.audioLanguage !== INHERIT_VALUE ||
    state.subtitleLanguage !== INHERIT_VALUE ||
    state.subtitleMode !== INHERIT_VALUE ||
    state.showForcedSubtitles !== INHERIT_VALUE
  );
}

/**
 * The canonical writes one editor state implies, one per key.
 *
 * "Inherit" is the absence of a row, so it plans a clear rather than a stored
 * sentinel — which is what lets a later change to the profile default reach a
 * library the user never overrode.
 */
export function buildLibraryPlaybackMutations(
  state: LibraryPlaybackEditorState,
): LibraryPlaybackMutation[] {
  return [
    planLanguage(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE, state.audioLanguage),
    planLanguage(SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE, state.subtitleLanguage),
    state.subtitleMode === INHERIT_VALUE
      ? { key: SETTING_KEYS.PLAYBACK_SUBTITLE_MODE }
      : { key: SETTING_KEYS.PLAYBACK_SUBTITLE_MODE, value: state.subtitleMode },
    state.showForcedSubtitles === INHERIT_VALUE
      ? { key: SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES }
      : {
          key: SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES,
          value: state.showForcedSubtitles === "on",
        },
  ];
}

// A language field has three states, and the contract spells each differently:
// inherit is no row, "None" is a stored null, and a code is a stored tag.
function planLanguage(key: SettingKey, value: string): LibraryPlaybackMutation {
  if (value === INHERIT_VALUE) return { key };
  if (value === NONE_VALUE) return { key, value: null };
  return { key, value };
}

export function buildInheritedLanguageLabel(_value: string) {
  return "Profile default";
}

export function buildInheritedSubtitleLanguageLabel(_value: string) {
  return "Profile default";
}

export function buildInheritedSubtitleModeLabel(_value: string) {
  return "Profile default";
}

export function buildInheritedShowForcedSubtitlesLabel(_value: boolean | undefined) {
  return "Profile default";
}

export function getProfileDefaultLanguageHint(value: string | null | undefined) {
  return `Default: ${value ? getLanguageLabel(value) : "No preference"}`;
}

export function getProfileDefaultSubtitleLanguageHint(value: string | null | undefined) {
  return `Default: ${value ? getLanguageLabel(value) : "None"}`;
}

export function getProfileDefaultSubtitleModeHint(value: string | null | undefined) {
  return `Default: ${getSubtitleModeLabel(value || DEFAULT_SUBTITLE_MODE)}`;
}

export function getProfileDefaultForcedSubtitlesHint(value: boolean | undefined) {
  return `Default: ${getForcedSubtitlesLabel((value ?? DEFAULT_SHOW_FORCED_SUBTITLES) ? "on" : "off")}`;
}
