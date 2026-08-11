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
