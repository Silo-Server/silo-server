import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PlayerConfig } from "./context/PlayerConfigContext";
import { PlayerFetchError } from "./player-fetch";
import { buildStartRequestV3 } from "./playback-session-wire-v3";
import {
  fixtureClientCapabilitiesV3,
  fixtureClientPlaybackContextV3,
  fixturePlanV3,
} from "./protocol-v3.fixtures";
import { resetSessionMutations, sessionInstallation } from "./session-mutations";
import { playbackCapabilitiesV2, resetPlaybackCapabilitiesV2, startPlaybackV2 } from "./start-v2";

const config: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile",
  getDeviceId: () => "device",
};
const capabilities = {
  installation_id: "7f7d1c6e-3b2f-4b7e-9a3d-2a4c7e1f0b11",
  revision: "r",
  state: "available",
  allowed: true,
  protocol_versions: [3],
  features: ["sequenced_progress_v1"],
  deliveries: ["original_http"],
};
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
const fixtureStartRequestV3 = () =>
  buildStartRequestV3({
    fileId: 42,
    profileId: "profile",
    playbackAttemptId: "attempt-0123456789",
    qualityPreference: "auto",
    position: 0,
    forceStartPosition: false,
    metered: false,
    clientCapabilities: fixtureClientCapabilitiesV3(),
    clientPlaybackContext: fixtureClientPlaybackContextV3(),
  });
const wireDecision = () => {
  const plan = fixturePlanV3();
  return {
    protocol_version: 3,
    server_features: ["sequenced_progress_v1"],
    outcome: "playable",
    session_id: plan.session_id,
    playback_plan: {
      ...plan,
      requested_media_file_id: String(plan.requested_media_file_id),
      effective_media_file_id: String(plan.effective_media_file_id),
      source: { ...plan.source, media_file_id: String(plan.source.media_file_id) },
    },
  };
};
const problem = (status: number, type: string) =>
  json({ type: `https://silo.example.test/problems/${type}`, detail: `${type} detail` }, status);

beforeEach(() => {
  resetPlaybackCapabilitiesV2();
  resetSessionMutations();
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("startPlaybackV2", () => {
  it("reads capabilities once, sends installation_id and string file_id, and registers the session", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(json(capabilities))
      .mockResolvedValueOnce(json(wireDecision(), 201))
      .mockResolvedValueOnce(json(wireDecision(), 201));
    vi.stubGlobal("fetch", fetcher);
    const request = fixtureStartRequestV3();
    const decision = await startPlaybackV2(config, request);
    await startPlaybackV2(config, request);
    expect(fetcher).toHaveBeenCalledTimes(3);
    expect(fetcher.mock.calls[0]![0]).toBe("/api/v2/playback/capabilities");
    expect(fetcher.mock.calls[1]![0]).toBe("/api/v2/playback/start");
    const body = JSON.parse(fetcher.mock.calls[1]![1].body);
    expect(body.installation_id).toBe(capabilities.installation_id);
    expect(body.file_id).toBe(String(request.file_id));
    expect(body.playback_attempt_id).toBe(request.playback_attempt_id);
    expect(typeof decision.playback_plan!.effective_media_file_id).toBe("number");
    expect(typeof decision.playback_plan!.source.media_file_id).toBe("number");
    expect(sessionInstallation(decision.playback_plan!.session_id!)).toBe(
      capabilities.installation_id,
    );
  });

  it("retries the identical body on 5xx and network failures within the budget", async () => {
    vi.useFakeTimers();
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(json(capabilities))
      .mockResolvedValueOnce(problem(503, "dependency_unavailable"))
      .mockRejectedValueOnce(new TypeError("network"))
      .mockResolvedValueOnce(json(wireDecision(), 201));
    vi.stubGlobal("fetch", fetcher);
    const pending = startPlaybackV2(config, fixtureStartRequestV3());
    await vi.advanceTimersByTimeAsync(500);
    await vi.advanceTimersByTimeAsync(1000);
    const decision = await pending;
    expect(decision.playback_plan).toBeDefined();
    expect(fetcher).toHaveBeenCalledTimes(4);
    expect(fetcher.mock.calls[1]![1].body).toBe(fetcher.mock.calls[3]![1].body);
  });

  it("does not retry a 4xx and reports the problem code", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(json(capabilities))
      .mockResolvedValueOnce(problem(409, "idempotency_conflict"));
    vi.stubGlobal("fetch", fetcher);
    const error = await startPlaybackV2(config, fixtureStartRequestV3()).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(PlayerFetchError);
    expect(error).toMatchObject({ status: 409, code: "idempotency_conflict" });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("forgets memoized capabilities after installation_changed", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(json(capabilities))
      .mockResolvedValueOnce(problem(409, "installation_changed"))
      .mockResolvedValueOnce(json({ ...capabilities, installation_id: "new" }))
      .mockResolvedValueOnce(json(wireDecision(), 201));
    vi.stubGlobal("fetch", fetcher);
    await expect(startPlaybackV2(config, fixtureStartRequestV3())).rejects.toMatchObject({
      code: "installation_changed",
    });
    await startPlaybackV2(config, fixtureStartRequestV3());
    expect(fetcher.mock.calls[2]![0]).toBe("/api/v2/playback/capabilities");
    expect(JSON.parse(fetcher.mock.calls[3]![1].body).installation_id).toBe("new");
  });

  it("refuses a server without v2 playback or protocol 3", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        json({ ...capabilities, state: "not_configured", allowed: false, installation_id: "" }),
      );
    vi.stubGlobal("fetch", fetcher);
    await expect(startPlaybackV2(config, fixtureStartRequestV3())).rejects.toThrow(
      "not configured",
    );
    resetPlaybackCapabilitiesV2();
    fetcher.mockResolvedValueOnce(json({ ...capabilities, protocol_versions: [2] }));
    await expect(startPlaybackV2(config, fixtureStartRequestV3())).rejects.toThrow("protocol v3");
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("memoizes capabilities per origin and forces a refresh on demand", async () => {
    const fetcher = vi.fn().mockImplementation(async () => json(capabilities));
    vi.stubGlobal("fetch", fetcher);
    await playbackCapabilitiesV2(config);
    await playbackCapabilitiesV2(config);
    await playbackCapabilitiesV2(config, { force: true });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});
