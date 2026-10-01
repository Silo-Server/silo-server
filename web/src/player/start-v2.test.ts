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
  it("requires advertised support before starting playback with a fixed version", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(json(capabilities));
    vi.stubGlobal("fetch", fetcher);
    await expect(
      startPlaybackV2(config, {
        ...fixtureStartRequestV3(),
        allow_alternate_versions: false,
      }),
    ).rejects.toThrow("same version");
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("sends the fixed version constraint when the server supports it", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        json({ ...capabilities, features: [...capabilities.features, "fixed_media_file_v1"] }),
      )
      .mockResolvedValueOnce(json(wireDecision(), 201));
    vi.stubGlobal("fetch", fetcher);
    await startPlaybackV2(config, { ...fixtureStartRequestV3(), allow_alternate_versions: false });
    expect(JSON.parse(fetcher.mock.calls[1]![1].body).allow_alternate_versions).toBe(false);
  });

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

  it("allows worker readiness to take longer than fifteen seconds", async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith("capabilities")) return json(capabilities);
      return new Promise<Response>((resolve, reject) => {
        const ready = setTimeout(() => resolve(json(wireDecision(), 201)), 25_000);
        init!.signal!.addEventListener(
          "abort",
          () => {
            clearTimeout(ready);
            reject(init!.signal!.reason);
          },
          { once: true },
        );
      });
    });
    vi.stubGlobal("fetch", fetcher);
    const result = startPlaybackV2(config, fixtureStartRequestV3()).catch(
      (error: unknown) => error,
    );
    await vi.advanceTimersByTimeAsync(25_000);
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(await result).toMatchObject({ outcome: "playable" });
  });

  it("retries a lost successful body with the identical start", async () => {
    vi.useFakeTimers();
    const lost = json(wireDecision(), 201);
    vi.spyOn(lost, "text").mockRejectedValue(new TypeError("lost response body"));
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(json(capabilities))
      .mockResolvedValueOnce(lost)
      .mockResolvedValueOnce(json(wireDecision(), 201));
    vi.stubGlobal("fetch", fetcher);
    const result = startPlaybackV2(config, fixtureStartRequestV3()).catch(
      (error: unknown) => error,
    );
    await vi.advanceTimersByTimeAsync(500);
    expect(await result).toMatchObject({ outcome: "playable" });
    expect(fetcher.mock.calls[1]![1].body).toBe(fetcher.mock.calls[2]![1].body);
  });

  it("bounds stalled response bodies and the final retry by the whole budget", async () => {
    vi.useFakeTimers();
    const started = Date.now();
    const requests: { at: number; body: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url.endsWith("capabilities")) return json(capabilities);
        requests.push({ at: Date.now() - started, body: init!.body });
        const response = json(wireDecision(), 201);
        vi.spyOn(response, "text").mockImplementation(
          () =>
            new Promise<string>((_resolve, reject) => {
              init!.signal!.addEventListener("abort", () => reject(init!.signal!.reason), {
                once: true,
              });
            }),
        );
        return response;
      }),
    );
    let finished: number | undefined;
    const result = startPlaybackV2(config, fixtureStartRequestV3()).catch((error: unknown) => {
      finished = Date.now() - started;
      return error;
    });
    await vi.advanceTimersByTimeAsync(60_000);
    expect(finished).toBe(60_000);
    expect(await result).toMatchObject({ name: "TimeoutError" });
    expect(requests).toHaveLength(2);
    expect(requests[0]!.body).toBe(requests[1]!.body);
    expect(sessionInstallation(wireDecision().session_id!)).toBeUndefined();
  });

  it("does not dispatch after backoff consumes the start budget", async () => {
    vi.useFakeTimers();
    const started = Date.now();
    const requests: number[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        if (url.endsWith("capabilities")) return json(capabilities);
        requests.push(Date.now() - started);
        return problem(503, "unavailable");
      }),
    );
    let finished: number | undefined;
    const result = startPlaybackV2(config, fixtureStartRequestV3()).catch((error: unknown) => {
      finished = Date.now() - started;
      return error;
    });
    await vi.advanceTimersByTimeAsync(60_000);
    expect(finished).toBe(60_000);
    expect(await result).toBeInstanceOf(PlayerFetchError);
    expect(requests.every((at) => at < 60_000)).toBe(true);
  });

  it("cancels during backoff without another request", async () => {
    vi.useFakeTimers();
    const controller = new AbortController();
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(json(capabilities))
      .mockRejectedValue(new TypeError("lost reply"));
    vi.stubGlobal("fetch", fetcher);
    let finished = false;
    const result = startPlaybackV2(config, fixtureStartRequestV3(), {
      signal: controller.signal,
    }).catch((error: unknown) => {
      finished = true;
      return error;
    });
    await vi.advanceTimersByTimeAsync(100);
    controller.abort();
    await vi.advanceTimersByTimeAsync(0);
    expect(finished).toBe(true);
    expect(await result).toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(60_000);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("allows a newly selected file after a missing-file refusal", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(json(capabilities))
      .mockResolvedValueOnce(problem(404, "not_found"))
      .mockResolvedValueOnce(json(wireDecision(), 201));
    vi.stubGlobal("fetch", fetcher);
    await expect(startPlaybackV2(config, fixtureStartRequestV3())).rejects.toMatchObject({
      status: 404,
    });
    await startPlaybackV2(config, {
      ...fixtureStartRequestV3(),
      file_id: 43,
      playback_attempt_id: "new-user-intent",
    });
    expect(JSON.parse(fetcher.mock.calls[2]![1].body)).toMatchObject({
      file_id: "43",
      playback_attempt_id: "new-user-intent",
    });
    expect(fetcher).toHaveBeenCalledTimes(3);
  });

  it("keeps a 4xx refusal final even when its body is interrupted", async () => {
    const refused = problem(422, "validation_failed");
    vi.spyOn(refused, "text").mockRejectedValue(new TypeError("lost problem body"));
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(json(capabilities))
      .mockResolvedValueOnce(refused);
    vi.stubGlobal("fetch", fetcher);
    await expect(startPlaybackV2(config, fixtureStartRequestV3())).rejects.toMatchObject({
      status: 422,
    });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("does not register a session when canceled while reading its decision", async () => {
    const controller = new AbortController();
    const response = json(wireDecision(), 201);
    vi.spyOn(response, "text").mockImplementation(async () => {
      controller.abort();
      return JSON.stringify(wireDecision());
    });
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(json(capabilities))
      .mockResolvedValueOnce(response);
    vi.stubGlobal("fetch", fetcher);
    await expect(
      startPlaybackV2(config, fixtureStartRequestV3(), { signal: controller.signal }),
    ).rejects.toMatchObject({ name: "AbortError" });
    expect(sessionInstallation(wireDecision().session_id!)).toBeUndefined();
    expect(fetcher).toHaveBeenCalledTimes(2);
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
