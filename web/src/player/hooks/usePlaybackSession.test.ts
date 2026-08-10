import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
import {
  fixtureClientCapabilitiesV3,
  fixtureClientPlaybackContextV3,
  fixturePlanV3,
} from "../protocol-v3.fixtures";
import {
  buildReplanRequestV3,
  buildStartRequestV3,
  routeEventPlanIdentityV3,
} from "../playback-session-wire-v3";
import { usePlaybackSession } from "./usePlaybackSession";

const playerConfig: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile-1",
  getProfileToken: () => null,
};

function wrapper({ children }: { children: ReactNode }) {
  return createElement(PlayerConfigProvider, { config: playerConfig, children });
}

function jsonResponse(body: unknown, init: ResponseInit = {}) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
    ...init,
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

const startBase = {
  fileId: 42,
  profileId: "profile-1",
  playbackAttemptId: "attempt-0123456789",
  qualityPreference: "auto",
  position: 0,
  forceStartPosition: false,
  metered: false,
  clientCapabilities: fixtureClientCapabilitiesV3(),
  clientPlaybackContext: fixtureClientPlaybackContextV3(),
};

const replanBase = {
  plan: fixturePlanV3(),
  playbackAttemptId: "attempt-0123456789",
  replanRequestId: "replan-0123456789",
  planAttemptId: "plan-attempt-0123456789",
  qualityPreference: "auto",
  positionSeconds: 120,
  attemptedPlanKeys: [],
  attemptCount: 1,
  metered: false,
  clientCapabilities: fixtureClientCapabilitiesV3(),
  clientPlaybackContext: fixtureClientPlaybackContextV3(),
};

describe("buildStartRequestV3", () => {
  it("declares the protocol version and the plan feature", () => {
    expect(buildStartRequestV3(startBase)).toMatchObject({
      protocol_version: 3,
      client_features: ["playback_plan_v3"],
      file_id: 42,
      profile_id: "profile-1",
      playback_attempt_id: "attempt-0123456789",
      subtitle_fidelity_preference: "preserve",
    });
  });

  it("includes an explicit zero start position when forced", () => {
    expect(
      buildStartRequestV3({ ...startBase, position: 0, forceStartPosition: true }),
    ).toMatchObject({ start_position: 0 });
  });

  it("declares client-owned progress with an explicit zero anchor", () => {
    expect(
      buildStartRequestV3({
        ...startBase,
        position: 0,
        forceStartPosition: true,
        progressPersistence: "client",
      }),
    ).toMatchObject({ start_position: 0, progress_persistence: "client" });
  });

  it("omits the start position when playback should resume normally", () => {
    expect(buildStartRequestV3(startBase)).not.toHaveProperty("start_position");
  });

  it("clamps an absurd start position to the contract bound", () => {
    expect(buildStartRequestV3({ ...startBase, position: 1e12 })).toMatchObject({
      start_position: 31_536_000,
    });
  });

  it("includes an explicit audio track override when present", () => {
    expect(buildStartRequestV3({ ...startBase, explicitAudioTrackIndex: 2 })).toMatchObject({
      audio_track_index: 2,
    });
  });

  it("omits the bandwidth estimate when the browser reports none", () => {
    expect(buildStartRequestV3({ ...startBase, bandwidthEstimateKbps: null })).not.toHaveProperty(
      "bandwidth_estimate_kbps",
    );
  });

  it("sends the user bandwidth ceiling separately from the network estimate", () => {
    expect(
      buildStartRequestV3({
        ...startBase,
        bandwidthEstimateKbps: 25_000,
        bandwidthCapKbps: 6_000,
      }),
    ).toMatchObject({ bandwidth_estimate_kbps: 25_000, bandwidth_cap_kbps: 6_000 });
  });

  it("sends the quality preference verbatim for the server to normalize", () => {
    expect(buildStartRequestV3({ ...startBase, qualityPreference: "original" })).toMatchObject({
      quality_preference: "original",
    });
  });
});

describe("buildReplanRequestV3", () => {
  it("echoes the plan's identity so the server can detect a stale plan", () => {
    expect(buildReplanRequestV3({ ...replanBase, operation: "track_change" })).toMatchObject({
      protocol_version: 3,
      operation: "track_change",
      failed_plan_id: "plan:0123456789abcdef",
      plan_attempt_key: "v3:0123456789abcdef",
      position_seconds: 120,
    });
  });

  it("names a new audio track by index alone", () => {
    // An empty id makes the server resolve the ordinal against the *effective*
    // file, which the client cannot name: it changes on a version fallback.
    const body = buildReplanRequestV3({
      ...replanBase,
      operation: "track_change",
      audio: { id: "", index: 3 },
    });

    expect(body.selected_tracks.audio).toEqual({ id: "", index: 3 });
  });

  it("resends the untouched subtitle track on an audio-only change", () => {
    const plan = fixturePlanV3({
      selected_tracks: {
        audio: { id: "file:7:audio:0", index: 0 },
        subtitle: { id: "file:7:subtitle:2", index: 2 },
      },
    });

    // Omitting the subtitle would read as "subtitles off", not "unchanged".
    const body = buildReplanRequestV3({
      ...replanBase,
      plan,
      operation: "track_change",
      audio: { id: "", index: 1 },
    });

    expect(body.selected_tracks.subtitle).toEqual({ id: "file:7:subtitle:2", index: 2 });
  });

  it("clears the subtitle selection when the subtitle override is null", () => {
    const plan = fixturePlanV3({
      selected_tracks: {
        audio: { id: "file:7:audio:0", index: 0 },
        subtitle: { id: "file:7:subtitle:2", index: 2 },
      },
    });

    const body = buildReplanRequestV3({
      ...replanBase,
      plan,
      operation: "track_change",
      subtitle: null,
    });

    expect(body.selected_tracks).not.toHaveProperty("subtitle");
    expect(body.selected_tracks.audio).toEqual({ id: "file:7:audio:0", index: 0 });
  });

  it("echoes the plan's tracks byte-for-byte on a seek reanchor", () => {
    const plan = fixturePlanV3({
      selected_tracks: {
        audio: { id: "file:7:audio:1", index: 1 },
        subtitle: { id: "file:7:subtitle:0", index: 0 },
      },
    });

    // Seek recovery is validated against the current plan's tracks exactly, so
    // the shorthand identity used for a track change would be rejected here.
    const body = buildReplanRequestV3({
      ...replanBase,
      plan,
      operation: "seek_reanchor",
      positionSeconds: 900,
    });

    expect(body.selected_tracks).toEqual(plan.selected_tracks);
    expect(body).not.toHaveProperty("failure");
  });

  it("carries the loop guard and the failure classification on a recovery", () => {
    const body = buildReplanRequestV3({
      ...replanBase,
      operation: "failure_recovery",
      attemptedPlanKeys: ["v3:aaaaaaaaaaaaaaaa"],
      attemptCount: 2,
      failure: { classification: "decoder_error", message: "no decoder" },
    });

    expect(body).toMatchObject({
      operation: "failure_recovery",
      attempted_plan_keys: ["v3:aaaaaaaaaaaaaaaa"],
      attempt_count: 2,
      failure: { classification: "decoder_error", message: "no decoder" },
    });
  });

  it("omits failure when nothing failed", () => {
    const body = buildReplanRequestV3({ ...replanBase, operation: "quality_change" });

    expect(body).not.toHaveProperty("failure");
    expect(body.attempted_plan_keys).toEqual([]);
    expect(body.attempt_count).toBe(1);
  });

  it("sends the quality preference on a track change so it is not reset", () => {
    // On a track change an absent preference *keeps* the current quality, but
    // sending the current value is behaviourally identical and unambiguous.
    const body = buildReplanRequestV3({
      ...replanBase,
      operation: "track_change",
      qualityPreference: "original",
    });

    expect(body.quality_preference).toBe("original");
  });

  it("preserves the user bandwidth ceiling across replans", () => {
    const body = buildReplanRequestV3({
      ...replanBase,
      operation: "failure_recovery",
      bandwidthCapKbps: 4_000,
      failure: { classification: "playback_error" },
    });

    expect(body.bandwidth_cap_kbps).toBe(4_000);
  });
});

describe("routeEventPlanIdentityV3", () => {
  it("omits every plan-scoped field for a terminal start", () => {
    expect(routeEventPlanIdentityV3(null, null, "plan-attempt-client-only")).toEqual({});
  });

  it("includes the complete identity after a plan is adopted", () => {
    const plan = fixturePlanV3();
    expect(routeEventPlanIdentityV3(plan, "session-1", "plan-attempt-1")).toEqual({
      sessionId: "session-1",
      planId: plan.plan_id,
      planAttemptId: "plan-attempt-1",
      planAttemptKey: plan.plan_attempt_key,
    });
  });
});

describe("usePlaybackSession quality changes", () => {
  it("rolls back a rejected quality and keeps it out of later replans", async () => {
    const plan = fixturePlanV3();
    let replanCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: plan,
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) {
        replanCount += 1;
        if (replanCount === 1) {
          return jsonResponse({ message: "temporary failure" }, { status: 500 });
        }
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:fedcba9876543210",
            plan_attempt_key: "v3:fedcba9876543210",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) {
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") {
        return new Response(null, { status: 204 });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "original"),
      { wrapper },
    );

    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.changeQuality("720p", 120));
    await waitFor(() => {
      expect(result.current.replanning).toBe(false);
      expect(result.current.qualityPreference).toBe("original");
      expect(result.current.error).toBeTruthy();
    });

    act(() => result.current.refreshSubtitles(120));
    await waitFor(() => expect(replanCount).toBe(2));

    const replanBodies = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith("/playback/session-1/replan"))
      .map(([, init]) => JSON.parse(String(init?.body)) as { quality_preference: string });
    expect(replanBodies.map((body) => body.quality_preference)).toEqual(["720p", "original"]);

    unmount();
  });
});

describe("usePlaybackSession version switches", () => {
  it("clears and stops the previous session when a new request ends terminally", async () => {
    let startCount = 0;
    const stoppedSessions: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startCount += 1;
        if (startCount === 2) {
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "terminal",
            terminal: {
              reason: "no_playable_route",
              message: "The next item has no playable route.",
              retryable: false,
            },
          });
        }
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({ session_id: "session-1" }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) {
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") {
        stoppedSessions.push(url);
        return new Response(null, { status: 204 });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, rerender, unmount } = renderHook(
      ({ requestKey, fileId }: { requestKey: string; fileId: number }) =>
        usePlaybackSession(requestKey, [], [], fileId, 0, false, "auto"),
      { wrapper, initialProps: { requestKey: "episode-1", fileId: 7 } },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    rerender({ requestKey: "episode-2", fileId: 8 });

    await waitFor(() => {
      expect(result.current.plan).toBeNull();
      expect(result.current.error).toBe("The next item has no playable route.");
    });
    expect(result.current.streamUrl).toBeNull();
    expect(result.current.sessionId).toBeNull();
    expect(stoppedSessions).toEqual(["/api/v1/playback/session-1"]);

    unmount();
  });

  it("clears and stops the previous session when a replacement start request fails", async () => {
    const startBodies: Array<{ playback_attempt_id: string; start_position?: number }> = [];
    const stoppedSessions: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as {
          playback_attempt_id: string;
          start_position?: number;
        };
        startBodies.push(body);
        if (startBodies.length === 2) {
          return jsonResponse(
            { error: "internal_error", message: "replacement failed" },
            { status: 500 },
          );
        }
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3(),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") {
        stoppedSessions.push(url);
        return new Response(null, { status: 204 });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.switchVersion(99, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));
    await waitFor(() => expect(result.current.sessionId).toBeNull());
    const originalStart = startBodies[0];
    const replacementStart = startBodies[1];
    if (!originalStart || !replacementStart) throw new Error("expected two start requests");
    expect(replacementStart.start_position).toBe(0);
    expect(replacementStart.playback_attempt_id).not.toBe(originalStart.playback_attempt_id);
    expect(result.current.plan).toBeNull();
    expect(result.current.streamUrl).toBeNull();
    expect(result.current.sessionId).toBeNull();
    expect(result.current.error).toContain("could not start playback");
    expect(stoppedSessions).toEqual(["/api/v1/playback/session-1"]);

    unmount();
  });
});

describe("usePlaybackSession replans", () => {
  it("runs a queued failure recovery after the in-flight replan settles", async () => {
    const initialPlan = fixturePlanV3();
    let resolveFirstReplan: ((response: Response) => void) | undefined;
    const firstReplanResponse = new Promise<Response>((resolve) => {
      resolveFirstReplan = resolve;
    });
    const replanBodies: Array<{ operation: string; position_seconds: number }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: initialPlan,
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) {
        replanBodies.push(
          JSON.parse(String(init?.body)) as { operation: string; position_seconds: number },
        );
        if (replanBodies.length === 1) return firstReplanResponse;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:2222222222222222",
            plan_attempt_key: "v3:2222222222222222",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) {
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") {
        return new Response(null, { status: 204 });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.refreshSubtitles(120));
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    act(() => {
      result.current.reanchorSeek(300);
      result.current.recoverFromFailure({ classification: "decoder_error" }, 450);
      result.current.reanchorSeek(600);
    });
    expect(replanBodies).toHaveLength(1);

    await act(async () => {
      resolveFirstReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:1111111111111111",
            plan_attempt_key: "v3:1111111111111111",
          }),
        }),
      );
      await firstReplanResponse;
    });

    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(
      replanBodies.map(({ operation, position_seconds }) => ({ operation, position_seconds })),
    ).toEqual([
      { operation: "track_change", position_seconds: 120 },
      { operation: "failure_recovery", position_seconds: 450 },
    ]);

    unmount();
  });

  it("coalesces reanchor seeks behind an in-flight replan and keeps the latest position", async () => {
    const initialPlan = fixturePlanV3();
    const firstReplannedPlan = fixturePlanV3({
      plan_id: "plan:1111111111111111",
      plan_attempt_key: "v3:1111111111111111",
    });
    let resolveFirstReplan: ((response: Response) => void) | undefined;
    const firstReplanResponse = new Promise<Response>((resolve) => {
      resolveFirstReplan = resolve;
    });
    const replanBodies: Array<{ operation: string; position_seconds: number }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: initialPlan,
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) {
        replanBodies.push(
          JSON.parse(String(init?.body)) as { operation: string; position_seconds: number },
        );
        if (replanBodies.length === 1) return firstReplanResponse;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:2222222222222222",
            plan_attempt_key: "v3:2222222222222222",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) {
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") {
        return new Response(null, { status: 204 });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.refreshSubtitles(120));
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    act(() => {
      result.current.reanchorSeek(300);
      result.current.reanchorSeek(450);
    });
    expect(replanBodies).toHaveLength(1);

    await act(async () => {
      resolveFirstReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: firstReplannedPlan,
        }),
      );
      await firstReplanResponse;
    });

    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(
      replanBodies.map(({ operation, position_seconds }) => ({ operation, position_seconds })),
    ).toEqual([
      { operation: "track_change", position_seconds: 120 },
      { operation: "seek_reanchor", position_seconds: 450 },
    ]);

    unmount();
  });

  it("surfaces an error after the failure-recovery cap is exhausted", async () => {
    let replanCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3(),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) {
        replanCount += 1;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3(),
        });
      }
      if (url.endsWith("/playback/route-events")) {
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") {
        return new Response(null, { status: 204 });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.planRevision).toBe(1));

    for (let attempt = 0; attempt < 8; attempt += 1) {
      act(() => {
        result.current.recoverFromFailure({ classification: "decoder_error" }, 120 + attempt);
      });
      await waitFor(() => expect(result.current.planRevision).toBe(attempt + 2));
    }

    act(() => {
      result.current.recoverFromFailure({ classification: "decoder_error" }, 200);
    });
    await waitFor(() => {
      expect(result.current.errorTitle).toBe("Playback failed");
      expect(result.current.error).toBe("Playback failed after repeated recovery attempts.");
    });
    expect(replanCount).toBe(8);

    unmount();
  });
});
