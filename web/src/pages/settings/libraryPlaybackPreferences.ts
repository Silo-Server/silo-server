import type { LibraryPlaybackPreference } from "@/api/types";
import type { UpdateLibraryPlaybackPreferenceRequest } from "@/hooks/queries/libraryPlaybackPreferences";
import { getLanguageName, LANGUAGES } from "@/player/utils/languageNames";

export const INHERIT_VALUE = "inherit";
export const NONE_VALUE = "none";
export const ORIGINAL_LANGUAGE_VALUE = "original";
export const ORIGINAL_LANGUAGE_LABEL = "Original Language";
export const DEFAULT_SUBTITLE_MODE = "auto";
export const DEFAULT_SHOW_FORCED_SUBTITLES = true;

/**
 * Language choices for per-library playback overrides, re-exported from the
 * canonical language list so every supported language stays selectable.
 */
export const LANGUAGE_OPTIONS = LANGUAGES;

export const SUBTITLE_MODE_OPTIONS = [
  { value: "auto", label: "Auto" },
  { value: "always", label: "Always" },
  { value: "off", label: "Off" },
] as const;

export type LibraryPlaybackEditorState = {
  audioLanguage: string;
  subtitleLanguage: string;
  subtitleMode: string;
  showForcedSubtitles: string;
};

export function getLanguageLabel(code: string) {
  if (code === ORIGINAL_LANGUAGE_VALUE) {
    return ORIGINAL_LANGUAGE_LABEL;
  }
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

export function buildLibraryPlaybackSummary(preference: LibraryPlaybackPreference | null) {
  if (!preference) {
    return "Uses profile defaults";
  }

  const parts: string[] = [];
  if (preference.audio_language !== undefined) {
    parts.push(`Audio: ${getLanguageLabel(preference.audio_language)}`);
  }
  if (preference.subtitle_language !== undefined) {
    parts.push(`Subtitles: ${getSubtitleLanguageLabel(preference.subtitle_language)}`);
  }
  if (preference.subtitle_mode !== undefined) {
    parts.push(`Behavior: ${getSubtitleModeLabel(preference.subtitle_mode)}`);
  }
  if (preference.show_forced_subtitles !== undefined) {
    parts.push(`Forced subtitles: ${preference.show_forced_subtitles ? "On" : "Off"}`);
  }

  return parts.length > 0 ? parts.join(" • ") : "Uses profile defaults";
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

export function createLibraryPlaybackEditorState(
  preference: LibraryPlaybackPreference | null,
): LibraryPlaybackEditorState {
  if (!preference) {
    return {
      audioLanguage: INHERIT_VALUE,
      subtitleLanguage: INHERIT_VALUE,
      subtitleMode: INHERIT_VALUE,
      showForcedSubtitles: INHERIT_VALUE,
    };
  }

  return {
    audioLanguage: preference.audio_language ?? INHERIT_VALUE,
    subtitleLanguage:
      preference.subtitle_language === undefined
        ? INHERIT_VALUE
        : preference.subtitle_language === ""
          ? NONE_VALUE
          : preference.subtitle_language,
    subtitleMode: preference.subtitle_mode ?? INHERIT_VALUE,
    showForcedSubtitles:
      preference.show_forced_subtitles === undefined
        ? INHERIT_VALUE
        : preference.show_forced_subtitles
          ? "on"
          : "off",
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

export function buildLibraryPlaybackRequest(
  state: LibraryPlaybackEditorState,
): UpdateLibraryPlaybackPreferenceRequest {
  const request: UpdateLibraryPlaybackPreferenceRequest = {};

  if (state.audioLanguage !== INHERIT_VALUE) {
    request.audio_language = state.audioLanguage;
  }
  if (state.subtitleLanguage !== INHERIT_VALUE) {
    request.subtitle_language = state.subtitleLanguage === NONE_VALUE ? "" : state.subtitleLanguage;
  }
  if (state.subtitleMode !== INHERIT_VALUE) {
    request.subtitle_mode = state.subtitleMode;
  }
  if (state.showForcedSubtitles !== INHERIT_VALUE) {
    request.show_forced_subtitles = state.showForcedSubtitles === "on";
  }

  return request;
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

export function getProfileDefaultLanguageHint(value: string) {
  return `Default: ${getLanguageLabel(value)}`;
}

export function getProfileDefaultSubtitleLanguageHint(value: string) {
  return `Default: ${value ? getLanguageLabel(value) : "None"}`;
}

export function getProfileDefaultSubtitleModeHint(value: string) {
  return `Default: ${getSubtitleModeLabel(value || DEFAULT_SUBTITLE_MODE)}`;
}

export function getProfileDefaultForcedSubtitlesHint(value: boolean | undefined) {
  return `Default: ${getForcedSubtitlesLabel((value ?? DEFAULT_SHOW_FORCED_SUBTITLES) ? "on" : "off")}`;
}
