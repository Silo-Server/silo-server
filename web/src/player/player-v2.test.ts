import { afterEach, describe, expect, it, vi } from "vitest";

import type { PlayerConfig } from "./context/PlayerConfigContext";
import { PlayerFetchError } from "./player-fetch";
import { playerV2, playerV2Origin } from "./player-v2";

const config: PlayerConfig = {
  apiBaseUrl: "/api/v2",
  getAccessToken: () => "token-1",
  getProfileId: () => "profile-1",
  getDeviceId: () => "web-player-device",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("playerV2Origin", () => {
  it.each(["v1", "v2"])(
    "derives the installation origin from the host's %s base URL",
    (version) => {
      expect(playerV2Origin({ ...config, apiBaseUrl: `/api/${version}` })).toBe("");
      expect(playerV2Origin({ ...config, apiBaseUrl: `/api/${version}/` })).toBe("");
      expect(playerV2Origin({ ...config, apiBaseUrl: `https://silo.example/api/${version}` })).toBe(
        "https://silo.example",
      );
    },
  );
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

  it("refreshes an expired access token before retrying the v2 request", async () => {
    let token = "expired";
    const refreshToken = vi.fn(async () => {
      token = "fresh";
      return true;
    });
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response("expired", { status: 401 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await playerV2(
      { ...config, getAccessToken: () => token, refreshToken },
      "PUT /api/v2/subtitle-prefs/{series_id}",
      {
        path: { series_id: "series-1" },
        body: { subtitle_mode: "off", subtitle_track_index: -1 },
      },
    );

    expect(refreshToken).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1]?.[1]).toMatchObject({
      headers: expect.objectContaining({ Authorization: "Bearer fresh" }),
    });
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

it("sends subtitle multipart bytes once without a JSON content type", async () => {
  const fetchMock = vi.fn(
    async () =>
      new Response(JSON.stringify({ subtitle: { id: "7", media_file_id: "42" } }), { status: 200 }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const file = new File(["synthetic subtitle"], "synthetic.en.srt", {
    type: "application/octet-stream",
  });
  await playerV2(config, "POST /api/v2/subtitles/upload", {
    form: { media_file_id: "42", file, language_override: "true", language: "fr" },
  });
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
  expect(url).toBe("/api/v2/subtitles/upload");
  expect(new Headers(init.headers).get("Content-Type")).toBeNull();
  expect(new Headers(init.headers).get("Authorization")).toBe("Bearer token-1");
  expect(init.body).toBeInstanceOf(FormData);
  const form = init.body as FormData;
  expect(form.get("file")).toBe(file);
  expect(form.get("language_override")).toBe("true");
  expect(form.get("media_file_id")).toBe("42");
});
