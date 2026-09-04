import { afterEach, expect, it, vi } from "vitest";
import {
  api,
  getAccessToken,
  getAuthContextVersion,
  getProfileToken,
  refreshAuthentication,
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
} from "../api/client";
import { playerFetch } from "./player-fetch";

afterEach(() => {
  vi.unstubAllGlobals();
  setAccessToken(null);
  setRefreshToken(null);
  setProfileToken(null);
  setProfileId(null);
});

it("shares one refresh between concurrent player and ordinary API requests", async () => {
  setAccessToken("expired");
  setRefreshToken("refresh-original");
  setProfileId("profile-original");
  setProfileToken("pin-original");
  let finishRefresh!: (value: Response) => void;
  const refreshResponse = new Promise<Response>((resolve) => {
    finishRefresh = resolve;
  });
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    if (String(input) === "/api/v1/auth/refresh") return refreshResponse;
    const headers = init?.headers as Record<string, string>;
    expect(headers["X-Profile-Id"]).toBe("profile-original");
    expect(headers["X-Profile-Token"]).toBe("pin-original");
    return headers.Authorization === "Bearer fresh"
      ? new Response(null, { status: 204 })
      : new Response("expired", { status: 401 });
  });
  vi.stubGlobal("fetch", fetchMock);
  const config = {
    apiBaseUrl: "/api/v1",
    getAccessToken,
    getAuthContext: getAuthContextVersion,
    getProfileId: () => "profile-original",
    getProfileToken,
    getDeviceId: () => "test-device",
    refreshToken: refreshAuthentication,
  };
  const requests = Promise.all([
    playerFetch(config, "/playback/heartbeat", { method: "POST" }),
    playerFetch(config, "/subtitles/ai/status"),
    api("/profiles"),
  ]);
  await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(4));
  finishRefresh(
    new Response(JSON.stringify({ access_token: "fresh", refresh_token: "refresh-rotated" }), {
      status: 200,
    }),
  );
  await expect(requests).resolves.toEqual([undefined, undefined, undefined]);
  expect(
    fetchMock.mock.calls.filter(([input]) => String(input) === "/api/v1/auth/refresh"),
  ).toHaveLength(1);
  expect(fetchMock).toHaveBeenCalledTimes(7);
});
