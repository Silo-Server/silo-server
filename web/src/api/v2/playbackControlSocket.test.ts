import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
  StaleApiRequestContextError,
} from "@/api/client";
import {
  mintPlaybackControlSocketTicket,
  playbackControlSocketProtocols,
  playbackControlSocketURL,
  PLAYBACK_CONTROL_SOCKET_PROTOCOL,
} from "./playbackControlSocket";

const ticket = "b".repeat(43);
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}
const okTicket = {
  ticket,
  expires_in: 30,
  max_connection_seconds: 14400,
  protocol: PLAYBACK_CONTROL_SOCKET_PROTOCOL,
};

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setRefreshToken("refresh");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());

it("mints under captured authority with the installation and offers the protocol pair", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () => response(okTicket));
  vi.stubGlobal("fetch", fetch);
  const minted = await mintPlaybackControlSocketTicket("s-1", "inst-1");
  expect(minted).toEqual(okTicket);
  expect(String(fetch.mock.calls[0]![0])).toContain(
    "/api/v2/playback/sessions/s-1/control/ws-ticket",
  );
  expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({ installation_id: "inst-1" });
  fetch.mockClear();
  await mintPlaybackControlSocketTicket("s-1", undefined);
  expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({});
  expect(playbackControlSocketProtocols(ticket)).toEqual([
    PLAYBACK_CONTROL_SOCKET_PROTOCOL,
    `silo.ticket.${ticket}`,
  ]);
  expect(playbackControlSocketURL("s/1", "https://silo.example.test")).toBe(
    "wss://silo.example.test/api/v2/playback/sessions/s%2F1/control/ws",
  );
  expect(playbackControlSocketURL("s-1", "http://localhost:5173")).toBe(
    "ws://localhost:5173/api/v2/playback/sessions/s-1/control/ws",
  );
});

it("refuses a wrong protocol or malformed credential", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn<typeof globalThis.fetch>()
      .mockImplementationOnce(async () => response({ ...okTicket, protocol: "silo.events.v2" }))
      .mockImplementationOnce(async () => response({ ...okTicket, ticket: "short" })),
  );
  await expect(mintPlaybackControlSocketTicket("s-1", "inst-1")).rejects.toThrow(
    "Invalid playback control credential",
  );
  await expect(mintPlaybackControlSocketTicket("s-1", "inst-1")).rejects.toThrow(
    "Invalid playback control credential",
  );
});

it("throws the server's refusal without retrying and surfaces stale authority first", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () =>
    response(
      {
        type: "https://siloserver.org/docs/api/v2/problems/conflict",
        title: "Conflict",
        status: 409,
        detail: "The session's control authority is stale; start playback again.",
      },
      409,
    ),
  );
  vi.stubGlobal("fetch", fetch);
  await expect(mintPlaybackControlSocketTicket("s-1", "inst-1")).rejects.toMatchObject({
    status: 409,
    problemType: "conflict",
  });
  expect(fetch).toHaveBeenCalledTimes(1);

  const authority = captureProfileRequestContext();
  setProfileId("someone-else");
  fetch.mockClear();
  await expect(mintPlaybackControlSocketTicket("s-1", "inst-1", authority)).rejects.toBeInstanceOf(
    StaleApiRequestContextError,
  );
  expect(fetch).not.toHaveBeenCalled();
});

it("refuses a delayed credential after the profile changes underneath the mint", async () => {
  let finish!: (r: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof globalThis.fetch>().mockImplementation(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve;
        }),
    ),
  );
  const pending = mintPlaybackControlSocketTicket("s-1", "inst-1");
  setProfileId("someone-else");
  finish(response(okTicket));
  await expect(pending).rejects.toBeInstanceOf(StaleApiRequestContextError);
});
