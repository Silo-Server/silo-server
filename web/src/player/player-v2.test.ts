import { afterEach, describe, expect, it, vi } from "vitest";

import type { PlayerConfig } from "./context/PlayerConfigContext";
import { PlayerFetchError } from "./player-fetch";
import { playerV2, playerV2Origin } from "./player-v2";

const config: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token-1",
  getProfileId: () => "profile-1",
  getDeviceId: () => "web-player-device",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("playerV2Origin", () => {
  it("derives the v2 origin from the host's v1 base URL", () => {
    expect(playerV2Origin({ ...config, apiBaseUrl: "/api/v1" })).toBe("");
    expect(playerV2Origin({ ...config, apiBaseUrl: "/api/v1/" })).toBe("");
    expect(playerV2Origin({ ...config, apiBaseUrl: "https://silo.example/api/v1" })).toBe(
      "https://silo.example",
    );
  });
});

describe("playerV2", () => {
  it("sends the player's credentials and the typed body to the v2 route", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(
      playerV2(config, "PUT /api/v2/subtitle-prefs/{series_id}", {
        path: { series_id: "series/1" },
        body: { subtitle_track_index: -1, subtitle_mode: "off" },
      }),
    ).resolves.toBeUndefined();

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v2/subtitle-prefs/series%2F1",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ subtitle_track_index: -1, subtitle_mode: "off" }),
        headers: expect.objectContaining({
          Accept: "application/json",
          "Content-Type": "application/json",
          Authorization: "Bearer token-1",
          "X-Profile-Id": "profile-1",
          "X-Silo-Device-Id": "web-player-device",
        }),
      }),
    );
  });

  it("encodes typed setting queries and keeps the configured player identity", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    await playerV2(
      { ...config, apiBaseUrl: "https://silo.example/api/v1" },
      "PUT /api/v2/settings/values/{key}",
      {
        path: { key: "playback.subtitle_mode" },
        query: { scope: "profile_series", series_id: "series /1" },
        body: { value: "off" },
      },
    );
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(
      "https://silo.example/api/v2/settings/values/playback.subtitle_mode?scope=profile_series&series_id=series+%2F1",
    );
    expect(init.headers).toMatchObject({
      Authorization: "Bearer token-1",
      "X-Profile-Id": "profile-1",
      "X-Silo-Device-Id": "web-player-device",
    });
    expect(init).not.toHaveProperty("query");
    expect(init).not.toHaveProperty("path");
  });

  it("serializes array queries as repeated keys", async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify({ values: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);
    await playerV2(config, "GET /api/v2/settings/values", {
      query: { keys: ["playback.subtitle_mode", "playback.subtitle_language"], scope: "profile" },
    });
    const [url] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(new URL(url, "https://silo.example").searchParams.getAll("keys")).toEqual([
      "playback.subtitle_mode",
      "playback.subtitle_language",
    ]);
  });

  it("surfaces a Problem Details answer as a PlayerFetchError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              type: "https://siloserver.org/docs/api/v2/problems/profile_verification_required",
              title: "Profile verification required",
              status: 403,
              detail: "The profile is locked.",
            }),
            { status: 403, headers: { "Content-Type": "application/problem+json" } },
          ),
      ),
    );

    const failure = await playerV2(config, "PUT /api/v2/subtitle-prefs/{series_id}", {
      path: { series_id: "series-1" },
      body: { subtitle_track_index: 0 },
    }).catch((err: unknown) => err);

    expect(failure).toBeInstanceOf(PlayerFetchError);
    expect(failure).toMatchObject({
      status: 403,
      code: "profile_verification_required",
      message: "The profile is locked.",
    });
  });
});
