import { afterEach, describe, expect, it, vi } from "vitest";

import type { PlayerConfig } from "./context/PlayerConfigContext";
import { playerFetch } from "./player-fetch";

const config: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => null,
  getProfileId: () => null,
  getDeviceId: () => "web-player-device",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("playerFetch", () => {
  it("refreshes once and preserves the original profile and request body", async () => {
    let token = "expired";
    let profile = "original-profile";
    const refreshToken = vi.fn(async () => {
      token = "fresh";
      profile = "new-profile";
      return true;
    });
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response("expired", { status: 401 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    await playerFetch(
      { ...config, getAccessToken: () => token, getProfileId: () => profile, refreshToken },
      "/playback/heartbeat",
      { method: "POST", body: '{"position":10}' },
    );
    expect(refreshToken).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1]?.[1]).toMatchObject({
      method: "POST",
      body: '{"position":10}',
      headers: { Authorization: "Bearer fresh", "X-Profile-Id": "original-profile" },
    });
  });

  it.each([401, 403])("does not loop or refresh a forbidden request (%s)", async (status) => {
    const refreshToken = vi.fn(async () => true);
    const fetchMock = vi.fn(async () => new Response("refused", { status }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      playerFetch({ ...config, getAccessToken: () => "token", refreshToken }, "/test"),
    ).rejects.toMatchObject({ status });
    expect(refreshToken).toHaveBeenCalledTimes(status === 401 ? 1 : 0);
    expect(fetchMock).toHaveBeenCalledTimes(status === 401 ? 2 : 1);
  });

  it("keeps authentication failure when refresh fails", async () => {
    const fetchMock = vi.fn(async () => new Response("expired", { status: 401 }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      playerFetch({ ...config, refreshToken: async () => false }, "/test"),
    ).rejects.toMatchObject({ status: 401 });
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it.each(["request", "refresh"])(
    "does not replay across an account switch during %s",
    async (phase) => {
      let generation = 1;
      const refreshToken = vi.fn(async () => {
        generation++;
        return true;
      });
      const fetchMock = vi.fn(async () => {
        if (phase === "request") generation++;
        return new Response("expired", { status: 401 });
      });
      vi.stubGlobal("fetch", fetchMock);
      await expect(
        playerFetch(
          {
            ...config,
            getAccessToken: () => "token",
            getAuthContext: () => generation,
            refreshToken,
          },
          "/test",
        ),
      ).rejects.toMatchObject({ status: 401 });
      expect(fetchMock).toHaveBeenCalledOnce();
      expect(refreshToken).toHaveBeenCalledTimes(phase === "request" ? 0 : 1);
    },
  );

  it("uses an already-rotated token for a late 401 without refreshing again", async () => {
    let token = "expired";
    const refreshToken = vi.fn(async () => true);
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementationOnce(async () => {
        token = "fresh";
        return new Response("expired", { status: 401 });
      })
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    await playerFetch({ ...config, getAccessToken: () => token, refreshToken }, "/test");
    expect(refreshToken).not.toHaveBeenCalled();
    expect(fetchMock.mock.calls[1]?.[1]?.headers).toMatchObject({ Authorization: "Bearer fresh" });
  });

  it("parses a JSON body returned with 202 Accepted", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ job: { status: "running" } }), {
            status: 202,
            headers: { "Content-Type": "application/json" },
          }),
      ),
    );

    await expect(
      playerFetch<{ job: { status: string } }>(config, "/subtitles/ai/translate"),
    ).resolves.toEqual({
      job: { status: "running" },
    });
  });

  it("accepts an empty 202 response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 202 })),
    );

    await expect(playerFetch<void>(config, "/playback/route-events")).resolves.toBeUndefined();
  });

  it("sends the host application's stable device identity", async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await playerFetch<void>(config, "/playback/start", { method: "POST" });

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/playback/start",
      expect.objectContaining({
        headers: expect.objectContaining({ "X-Silo-Device-Id": "web-player-device" }),
      }),
    );
  });
});
