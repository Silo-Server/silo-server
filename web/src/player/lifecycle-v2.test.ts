import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { replanV2 } from "./lifecycle-v2";
import { registerSessionMutations, resetSessionMutations } from "./session-mutations";
import { PlayerFetchError } from "./player-fetch";
import type { PlayerConfig } from "./context/PlayerConfigContext";
import { buildReplanRequestV3 } from "./playback-session-wire-v3";
import {
  fixtureClientCapabilitiesV3,
  fixtureClientPlaybackContextV3,
  fixturePlanV3,
} from "./protocol-v3.fixtures";

const config: PlayerConfig = {
  apiBaseUrl: "http://localhost:3000/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile",
  getDeviceId: () => "device",
};
const sessionId = "11111111-1111-4111-8111-111111111111";
const installationId = "22222222-2222-4222-8222-222222222222";
const plan = fixturePlanV3();
const replan = buildReplanRequestV3({
  operation: "track_change",
  positionSeconds: 42.5,
  plan,
  playbackAttemptId: "attempt-0123456789",
  replanRequestId: "replan-0123456789",
  planAttemptId: "plan-attempt-0123",
  qualityPreference: "auto",
  attemptedPlanKeys: [],
  attemptCount: 1,
  metered: false,
  clientCapabilities: fixtureClientCapabilitiesV3(),
  clientPlaybackContext: fixtureClientPlaybackContextV3(),
});
const wireDecision = {
  protocol_version: 3,
  server_features: ["sequenced_progress_v1"],
  outcome: "playable",
  session_id: sessionId,
  playback_plan: {
    ...plan,
    session_id: sessionId,
    requested_media_file_id: "42",
    effective_media_file_id: "42",
    source: { ...plan.source, media_file_id: "42" },
  },
};
const reply = (body: unknown, status = 200, type = "application/json") =>
  new Response(body === null ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": type },
  });

beforeEach(() => {
  registerSessionMutations(sessionId, installationId);
});
afterEach(() => {
  resetSessionMutations();
  vi.unstubAllGlobals();
});

it("replans through v2 with the session's installation", async () => {
  const fetcher = vi.fn().mockResolvedValue(reply(wireDecision));
  vi.stubGlobal("fetch", fetcher);
  const decision = await replanV2(config, sessionId, replan);
  const [url, init] = fetcher.mock.calls[0] as [string, RequestInit];
  expect(url).toBe(`http://localhost:3000/api/v2/playback/${sessionId}/replan`);
  expect(init.method).toBe("POST");
  const headers = new Headers(init.headers);
  expect(headers.get("Authorization")).toBe("Bearer token");
  expect(headers.get("X-Profile-Id")).toBe("profile");
  const sent = JSON.parse(String(init.body)) as Record<string, unknown>;
  expect(sent.installation_id).toBe(installationId);
  expect(sent.replan_request_id).toBe("replan-0123456789");
  expect(sent.operation).toBe("track_change");
  expect(decision.playback_plan?.requested_media_file_id).toBe(42);
  expect(decision.playback_plan?.source.media_file_id).toBe(42);
});

it("surfaces a problem refusal as a player fetch error with its code", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      reply(
        {
          type: "https://siloserver.org/docs/api/v2/problems/idempotency_conflict",
          title: "Idempotency conflict",
          status: 409,
          detail: "The replan request ID belongs to a different request",
        },
        409,
        "application/problem+json",
      ),
    ),
  );
  const failure = await replanV2(config, sessionId, replan).catch((e: unknown) => e);
  expect(failure).toBeInstanceOf(PlayerFetchError);
  expect((failure as PlayerFetchError).status).toBe(409);
  expect((failure as PlayerFetchError).code).toBe("idempotency_conflict");
  expect((failure as PlayerFetchError).message).toContain("different request");
});

it("refuses a session that was not started on v2", async () => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  await expect(replanV2(config, "other", replan)).rejects.toThrow("not started on API v2");
  expect(fetcher).not.toHaveBeenCalled();
});
