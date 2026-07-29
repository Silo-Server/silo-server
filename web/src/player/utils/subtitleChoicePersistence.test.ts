import { describe, expect, it } from "vitest";

import { SETTING_KEYS } from "@/lib/settingsContract";
import { resolveSettingValues, type StoredSettingRow } from "@/lib/settingsResolve";
import type { PlayerSubtitleInfo } from "../types";
import { buildSubtitleChoiceRequests } from "./subtitleChoicePersistence";

const TRACKS: PlayerSubtitleInfo[] = [
  {
    index: 0,
    language: "ja",
    codec: "subrip",
    label: "Japanese",
    source: "embedded",
    forced: false,
    hearing_impaired: false,
    url: "",
  },
];

function canonicalWrites(requests: ReturnType<typeof buildSubtitleChoiceRequests>) {
  return requests.filter((request) => request.path.startsWith("/settings/values/"));
}

/** The key one canonical write addresses, parsed back out of its path. */
function keyOf(path: string): string {
  return decodeURIComponent(path.slice("/settings/values/".length).split("?")[0]!);
}

describe("buildSubtitleChoiceRequests", () => {
  it("writes the chosen language and mode at profile_series", () => {
    const requests = buildSubtitleChoiceRequests({
      seriesId: "series-1",
      index: 0,
      tracks: TRACKS,
      showForcedSubtitles: true,
    });

    const canonical = canonicalWrites(requests);
    expect(canonical.map((request) => keyOf(request.path))).toEqual([
      SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE,
      SETTING_KEYS.PLAYBACK_SUBTITLE_MODE,
    ]);
    for (const request of canonical) {
      expect(request.path).toContain("scope=profile_series&series_id=series-1");
    }
    expect(canonical[0]?.body).toEqual({ value: "ja" });
    expect(canonical[1]?.body).toEqual({ value: "always" });
  });

  it("never writes show_forced_subtitles as a canonical row", () => {
    // The player has no forced-subtitle control: the prop it holds is the
    // *resolved* value, which is the contract default for anyone who never set
    // one. Persisting it at profile_series — the top of the resolution order —
    // would permanently shadow the Subtitles screen's profile-scope toggle for
    // that series, and no web surface can clear a profile_series row for it.
    for (const showForcedSubtitles of [true, false, undefined]) {
      const requests = buildSubtitleChoiceRequests({
        seriesId: "series-1",
        index: 0,
        tracks: TRACKS,
        showForcedSubtitles,
      });
      expect(canonicalWrites(requests).map((request) => keyOf(request.path))).not.toContain(
        SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES,
      );
    }
  });

  it("keeps the forced-subtitle value on the legacy composite row", () => {
    // That row is keyed to a concrete track selection and is not part of the
    // canonical ladder, so echoing the resolved value there is harmless and
    // preserves the behavior the route has always had.
    const [legacy] = buildSubtitleChoiceRequests({
      seriesId: "series-1",
      index: 0,
      tracks: TRACKS,
      showForcedSubtitles: false,
    }).filter((request) => request.path.startsWith("/subtitle-prefs/"));

    expect(legacy?.body).toMatchObject({
      subtitle_language: "ja",
      subtitle_track_index: 0,
      subtitle_mode: "always",
      show_forced_subtitles: false,
    });
  });

  it("turning subtitles off clears the language rather than storing an empty one", () => {
    const canonical = canonicalWrites(
      buildSubtitleChoiceRequests({ seriesId: "series-1", index: null, tracks: TRACKS }),
    );

    expect(canonical[0]?.body).toEqual({ value: null });
    expect(canonical[1]?.body).toEqual({ value: "off" });
  });

  it("persists nothing for an index that names no real track", () => {
    // The AI live track's sentinel index: storing it would clobber the saved
    // preference with a nonexistent track and an empty language.
    expect(
      buildSubtitleChoiceRequests({ seriesId: "series-1", index: 9999, tracks: TRACKS }),
    ).toEqual([]);
  });

  it("leaves the profile-scope forced-subtitle choice reachable afterwards", () => {
    // End to end through the shared resolver, which mirrors
    // internal/settingsresolve: the rows this pick stores must not shadow a
    // later profile-scope "off".
    const requests = canonicalWrites(
      buildSubtitleChoiceRequests({
        seriesId: "series-1",
        index: 0,
        tracks: TRACKS,
        showForcedSubtitles: true,
      }),
    );
    const stored: StoredSettingRow[] = requests.map((request) => ({
      key: keyOf(request.path),
      scope: "profile_series",
      profileId: "profile-1",
      seriesId: "series-1",
      value: (request.body as { value: unknown }).value,
    }));
    stored.push({
      key: SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES,
      scope: "profile",
      profileId: "profile-1",
      value: false,
    });

    const [forced] = resolveSettingValues([SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES], stored, {
      profileId: "profile-1",
      seriesIds: ["series-1"],
    });
    expect(forced?.value).toBe(false);
    expect(forced?.source).toBe("profile");
  });
});
