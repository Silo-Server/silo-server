import { SETTING_KEYS, type SettingKey } from "./settingsContract";

/**
 * The canonical settings an in-player subtitle pick stores for a series.
 *
 * The player writes these rows when the viewer picks a track (WatchPage).
 * "Auto" on the item page removes them through the server's subtitle-prefs
 * DELETE, which clears the language, mode, and forced rows at profile_series
 * in the same transaction. profile_series is the first scope in the
 * manifest's resolution order for these keys, so a key added here must also
 * be cleared by that DELETE (DeleteSubtitlePreference), or it would outrank
 * every later choice with nothing in the UI able to remove it.
 *
 * playback.show_forced_subtitles is deliberately absent. The player has no
 * forced-subtitle control: the value it holds is the *resolved* one, which for
 * a viewer who never expressed a preference is the contract default. Writing
 * it back would pin that default at the top of the ladder and permanently
 * shadow the profile-scope toggle on the Language & Subtitles settings screen. Writers
 * also leave it off the legacy composite row, whose PUT copies it into the
 * canonical profile_series row.
 */
export const SERIES_SUBTITLE_SETTING_KEYS = [
  SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE,
  SETTING_KEYS.PLAYBACK_SUBTITLE_MODE,
] as const satisfies readonly SettingKey[];

export type SeriesSubtitleSettingKey = (typeof SERIES_SUBTITLE_SETTING_KEYS)[number];

/**
 * The values one subtitle pick stores, keyed the way the writer sends them.
 *
 * Returning a full record (rather than letting the caller assemble one) is
 * what keeps the written set and {@link SERIES_SUBTITLE_SETTING_KEYS} from
 * drifting: a key added to the list without a value here fails to type-check.
 */
export function seriesSubtitleSettingValues(selection: {
  /** The chosen track's language, or null when subtitles were turned off. */
  language: string | null;
  mode: string;
}): Record<SeriesSubtitleSettingKey, unknown> {
  return {
    // The contract spells "no preference" as null, where the legacy route
    // spelled it as the empty string.
    [SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE]: selection.language || null,
    [SETTING_KEYS.PLAYBACK_SUBTITLE_MODE]: selection.mode,
  };
}

/** The canonical values identity addressing one series: the profile_series scope at that series. */
export function seriesSubtitleSettingIdentity(seriesId: string) {
  return { scope: "profile_series" as const, series_id: seriesId };
}
