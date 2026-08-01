import {
  SERIES_SUBTITLE_SETTING_KEYS,
  seriesSubtitleSettingPath,
  seriesSubtitleSettingValues,
} from "@/lib/seriesSubtitleSettings";
import type { PlayerSubtitleInfo, PlayerSubtitleTrackSignature } from "../types";
import { derivePersistedSubtitleMode } from "./subtitleMode";

/** One PUT the player issues to persist a subtitle choice. */
export interface SubtitleChoiceRequest {
  path: string;
  body: unknown;
}

/**
 * The requests an in-player subtitle pick turns into.
 *
 * The choice splits across two stores, along the line the contract draws. The
 * language and the on/off mode are preferences — "this show, in English,
 * always on" — so they are canonical settings written at profile_series, where
 * the manifest resolves them above a library, a device, and the profile. The
 * track index and signature are not preferences: they identify one concrete
 * track in one file, so they stay on the specialized per-series route that has
 * always held them.
 *
 * Only values the viewer actually chose become canonical rows. The keys come
 * from SERIES_SUBTITLE_SETTING_KEYS, the same list the item page's "Auto"
 * reset clears — profile_series outranks every other scope, so a row written
 * here that the reset does not know about would shadow every later choice with
 * nothing in the UI able to remove it.
 *
 * playback.show_forced_subtitles is deliberately not among them. The player
 * holds only the *resolved* value and has no control that edits it, so for a
 * viewer who never expressed a preference it is the contract default; writing
 * that back would pin the default at the top of the ladder and permanently
 * shadow the profile-scope toggle on the Subtitles settings screen. It still
 * rides along on the legacy composite row, which is keyed to a concrete track
 * selection and is not part of the canonical resolution ladder.
 *
 * Returns an empty list when the index names no real track (e.g. the
 * in-progress AI live track's sentinel): persisting it would store a
 * nonexistent track with an empty language and clobber the saved preference.
 */
export function buildSubtitleChoiceRequests({
  seriesId,
  index,
  tracks,
  showForcedSubtitles,
}: {
  seriesId: string;
  /** The chosen track's backend index, or null for "subtitles off". */
  index: number | null;
  tracks: readonly PlayerSubtitleInfo[];
  /** The resolved forced-subtitle behavior, preserved in the legacy row. */
  showForcedSubtitles?: boolean;
}): SubtitleChoiceRequest[] {
  if (!seriesId) return [];

  const track = index !== null ? tracks.find((candidate) => candidate.index === index) : null;
  if (index !== null && !track) return [];

  const trackSignature: PlayerSubtitleTrackSignature | null = track
    ? {
        source: track.source,
        language: track.language,
        codec: track.codec,
        label: track.label,
        forced: track.forced,
        hearing_impaired: track.hearing_impaired,
      }
    : null;

  const mode = derivePersistedSubtitleMode(index);
  // Turning subtitles off stores mode "off" and clears the language rather
  // than storing an empty one.
  const chosen = seriesSubtitleSettingValues({ language: track?.language ?? null, mode });

  const requests: SubtitleChoiceRequest[] = SERIES_SUBTITLE_SETTING_KEYS.map((key) => ({
    path: seriesSubtitleSettingPath(key, seriesId),
    body: { value: chosen[key] },
  }));

  requests.push({
    path: `/subtitle-prefs/${seriesId}`,
    body: {
      subtitle_language: track?.language ?? "",
      subtitle_track_index: index ?? -1,
      subtitle_mode: mode,
      track_signature: trackSignature,
      show_forced_subtitles: showForcedSubtitles,
    },
  });

  return requests;
}
