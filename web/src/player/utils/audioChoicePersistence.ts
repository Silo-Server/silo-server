import { canonicalLanguageWireValue, normalizeLanguageCode } from "@/lib/languageTags";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import { playerV2, type PlayerV2Body } from "../player-v2";
import type { PlayerAudioTrack } from "../types";
import { runPlayerTrackPreferenceWrite } from "./trackPreferenceWrites";

export type AudioPreferenceBody = PlayerV2Body<"PUT /api/v2/audio-prefs/{series_id}">;

// Codes that name no spoken language. Saving one as the series language would
// invent a preference the viewer never expressed.
const NON_LANGUAGE_CODES = new Set(["und", "mul", "zxx", "mis"]);

function preferredAudioLanguage(language: string | undefined): string | undefined {
  const tag = canonicalLanguageWireValue(language);
  if (!tag || NON_LANGUAGE_CODES.has(normalizeLanguageCode(tag))) return undefined;
  return tag;
}

function trimmed(value: string | undefined): string | undefined {
  return value?.trim() || undefined;
}

/**
 * The body that saves `track`, at `trackIndex` in its file's audio list, as
 * the series' audio choice.
 *
 * The signature carries the catalog's own fields for the track: the server
 * recognizes the track in other episodes by matching them exactly against its
 * probed tracks, so values the browser reports would never match. `default`
 * is left out because it says nothing about which track the viewer wants.
 */
export function buildAudioPreferenceBody(
  trackIndex: number,
  track: PlayerAudioTrack,
): AudioPreferenceBody {
  const signature = {
    language: trimmed(track.language),
    title: trimmed(track.title),
    embedded_title: trimmed(track.embedded_title),
    codec: trimmed(track.codec),
    layout: trimmed(track.layout),
    channels: track.channels || undefined,
  };
  return {
    audio_track_index: trackIndex,
    audio_language: preferredAudioLanguage(track.language),
    track_signature: Object.values(signature).some((value) => value !== undefined)
      ? signature
      : undefined,
  };
}

/**
 * Saves an in-player audio pick for the series, after earlier writes for it,
 * for the profile that made it.
 */
export function sendAudioChoiceRequest(
  config: PlayerConfig,
  seriesId: string,
  body: AudioPreferenceBody,
): Promise<unknown> {
  return runPlayerTrackPreferenceWrite(config, "audio", seriesId, (ownerConfig) =>
    playerV2(ownerConfig, "PUT /api/v2/audio-prefs/{series_id}", {
      path: { series_id: seriesId },
      body,
    }),
  );
}
