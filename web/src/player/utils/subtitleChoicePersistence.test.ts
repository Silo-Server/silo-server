import { describe, expect, it } from "vitest";

import { SETTING_KEYS } from "@/lib/settingsContract";
import { resolveSettingValues, type StoredSettingRow } from "@/lib/settingsResolve";
import type { PlayerSubtitleInfo } from "../types";
import {
  buildSubtitleChoiceRequests,
  type SubtitleChoiceLegacyWrite,
  type SubtitleChoiceSettingWrite,
} from "./subtitleChoicePersistence";

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

function canonicalWrites(
  requests: ReturnType<typeof buildSubtitleChoiceRequests>,
): SubtitleChoiceSettingWrite[] {
  return requests.filter(
    (request): request is SubtitleChoiceSettingWrite => request.kind === "setting",
  );
}

function legacyWrite(
  requests: ReturnType<typeof buildSubtitleChoiceRequests>,
): SubtitleChoiceLegacyWrite | undefined {
  return requests.find(
    (request): request is SubtitleChoiceLegacyWrite => request.kind === "legacy",
  );
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
    expect(canonical.map((request) => request.key)).toEqual([
      SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE,
      SETTING_KEYS.PLAYBACK_SUBTITLE_MODE,
    ]);
    for (const request of canonical) {
      expect(request.identity).toEqual({ scope: "profile_series", series_id: "series-1" });
    }
    expect(canonical[0]?.value).toBe("ja");
    expect(canonical[1]?.value).toBe("always");
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
      expect(canonicalWrites(requests).map((request) => request.key)).not.toContain(
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
    }).filter((request) => request.kind === "legacy");

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

    expect(canonical[0]?.value).toBeNull();
    expect(canonical[1]?.value).toBe("off");
  });

  it("persists nothing for an index that names no real track", () => {
    // The AI live track's sentinel index: storing it would clobber the saved
    // preference with a nonexistent track and an empty language.
    expect(
      buildSubtitleChoiceRequests({ seriesId: "series-1", index: 9999, tracks: TRACKS }),
    ).toEqual([]);
  });

  it("persists a realtime track before the folded inventory renders", () => {
    const requests = buildSubtitleChoiceRequests({
      seriesId: "series-1",
      index: 4,
      tracks: TRACKS,
      inventoryTrack: {
        track_id: "downloaded:4",
        combined_index: 4,
        source: "downloaded",
        codec: "srt",
        language: "es",
        label: "Spanish (AI)",
        forced: false,
        default: false,
        hearing_impaired: false,
        delivery: "sidecar",
        url: "/subtitles/4",
      },
    });

    expect(canonicalWrites(requests)[0]?.value).toBe("es");
    expect(legacyWrite(requests)?.body).toMatchObject({
      subtitle_language: "es",
      subtitle_track_index: 4,
      track_signature: {
        source: "downloaded",
        language: "es",
        codec: "srt",
        label: "Spanish (AI)",
      },
    });
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
      key: request.key,
      scope: "profile_series",
      profileId: "profile-1",
      seriesId: "series-1",
      value: request.value,
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
