import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { reportRouteEventV2, reportSessionRouteEventV2 } from "./route-events-v2";
import { registerSessionMutations, resetSessionMutations } from "./session-mutations";
import type { PlayerConfig } from "./context/PlayerConfigContext";

const config: PlayerConfig = {
  apiBaseUrl: "http://localhost:3000/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile",
  getDeviceId: () => "device",
};
const sessionId = "11111111-1111-4111-8111-111111111111";
const installationId = "22222222-2222-4222-8222-222222222222";
const reply = (body: unknown, status = 200, type = "application/json") =>
  new Response(body === null ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": type },
  });
const event = {
  protocol_version: 3,
  playback_attempt_id: "attempt-0123456789",
  session_id: sessionId,
  event: "first_frame" as const,
  diagnostics: { decoder_name: "synthetic" },
};

beforeEach(() => {
  registerSessionMutations(sessionId, installationId);
});
afterEach(() => {
  resetSessionMutations();
  vi.unstubAllGlobals();
});

it("reports route events once with a fresh id and never throws", async () => {
  const fetcher = vi.fn().mockResolvedValue(reply({ event_id: "x", outcome: "accepted" }, 202));
  vi.stubGlobal("fetch", fetcher);
  await reportSessionRouteEventV2(config, sessionId, event);
  await reportSessionRouteEventV2(config, sessionId, event);
  expect(fetcher).toHaveBeenCalledTimes(2);
  const bodies = fetcher.mock.calls.map(
    ([, init]) => JSON.parse(String((init as RequestInit).body)) as Record<string, unknown>,
  );
  expect(bodies[0]!.installation_id).toBe(installationId);
  expect(typeof bodies[0]!.event_id).toBe("string");
  expect(bodies[0]!.event_id).not.toBe(bodies[1]!.event_id);
  expect(fetcher.mock.calls[0]![0]).toBe("http://localhost:3000/api/v2/playback/route-events");
  fetcher.mockRejectedValue(new TypeError("offline"));
  await expect(reportRouteEventV2(config, installationId, event)).resolves.toBeUndefined();
  expect(fetcher).toHaveBeenCalledTimes(3);
});

it("skips a session that was not started on v2", async () => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  await reportSessionRouteEventV2(config, "other", event);
  expect(fetcher).not.toHaveBeenCalled();
});
