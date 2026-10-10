import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
import { markPlaybackIntent } from "../first-frame";
import {
  buildReplanRequestV3,
  buildStartRequestV3,
  routeEventPlanIdentityV3,
  VIDEO_CLIENT_FEATURES_V3,
} from "../playback-session-wire-v3";
import {
  fixtureClientCapabilitiesV3,
  fixtureClientPlaybackContextV3,
  fixturePlanV3,
  fixtureSubtitleInventoryItemV3,
} from "../protocol-v3.fixtures";
import { resetSessionMutations } from "../session-mutations";
import { resetCodecDetectionForTests } from "./useCodecDetection";
import {
  RECONNECT_BASE_DELAY_MS,
  RECONNECT_MAX_ATTEMPTS,
  RECONNECT_MAX_DELAY_MS,
  reconnectDelayMs,
  usePlaybackSession,
} from "./usePlaybackSession";

// These hook tests exercise plan adoption and replacement against a transport
// boundary. The v2 start/replan helpers are covered by their own tests; here
// they forward to the same fetch stubs under the v2 base.
vi.mock("../start-v2", () => ({
  startPlaybackV2: async (config: PlayerConfig, body: unknown) => {
    const { playerFetch } = await import("../player-fetch");
    const { registerSessionMutations } = await import("../session-mutations");
    const decision = await playerFetch<import("../protocol-v3").DecisionResponseV3>(
      { ...config, apiBaseUrl: "/api/v2" },
      "/playback/start",
      { method: "POST", body: JSON.stringify(body) },
    );
    const sessionId = decision.playback_plan?.session_id ?? decision.session_id;
    if (decision.playback_plan && sessionId) registerSessionMutations(sessionId, "installation");
    return decision;
  },
}));
vi.mock("../lifecycle-v2", () => ({
  replanV2: async (config: PlayerConfig, sessionId: string, body: unknown) => {
    const { playerFetch } = await import("../player-fetch");
    return playerFetch({ ...config, apiBaseUrl: "/api/v2" }, `/playback/${sessionId}/replan`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  },
}));

const playerConfig: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "token",
  getProfileId: () => "profile-1",
  getDeviceId: () => "test-device",
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
  resetCodecDetectionForTests();
  resetSessionMutations();
  vi.restoreAllMocks();
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
  it("pins the room source without changing the requested streaming quality", () => {
    expect(
      buildStartRequestV3({
        ...startBase,
        allowAlternateVersions: false,
        qualityPreference: "720p",
      }),
    ).toMatchObject({ allow_alternate_versions: false, quality_preference: "720p" });
    expect(buildStartRequestV3(startBase)).not.toHaveProperty("allow_alternate_versions");
  });

  // Feature tokens are promises the server enforces, so a surface advertises
  // only what it implements: the base set alone unless the caller names more.
  it("advertises only the surface's own features", () => {
    expect(
      buildStartRequestV3({ ...startBase, extraClientFeatures: VIDEO_CLIENT_FEATURES_V3 })
        .client_features,
    ).toEqual(["playback_plan_v3", "plan_invalidated_v1"]);
    expect(buildStartRequestV3(startBase).client_features).toEqual(["playback_plan_v3"]);
  });

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

  // A replan that sends `client_features` replaces the negotiated list, so a
  // replan which advertised less than the start did would silently withdraw the
  // promise the server gates the invalidation command on.
  it("re-advertises the same features a start negotiated", () => {
    expect(
      buildReplanRequestV3({
        ...replanBase,
        operation: "failure_recovery",
        extraClientFeatures: VIDEO_CLIENT_FEATURES_V3,
      }).client_features,
    ).toEqual(["playback_plan_v3", "plan_invalidated_v1"]);
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
        return jsonResponse({ outcome: "stopped" });
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

describe("usePlaybackSession initial bitmap subtitles", () => {
  it("adopts a successful bitmap subtitle start without replanning", async () => {
    const subtitlePlan = fixturePlanV3({
      session_id: "session-1",
      plan_id: "plan:bitmap-subtitle",
      plan_attempt_key: "v3:bitmap-subtitle",
      selected_tracks: {
        audio: { id: "file:7:audio:0", index: 0 },
        subtitle: { id: "file:7:subtitle:0", index: 0 },
      },
    });
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: subtitlePlan,
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () =>
        usePlaybackSession(
          "request-1",
          [],
          [],
          7,
          0,
          false,
          "auto",
          null,
          undefined,
          null,
          { 7: 0 },
          { 7: 0 },
        ),
      { wrapper },
    );

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:bitmap-subtitle"));
    expect(startBodies).toHaveLength(1);
    expect(startBodies[0]).toMatchObject({ subtitle_track_index: 0 });
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/replan"))).toBe(false);
    expect(result.current.planRevision).toBe(1);
    expect(result.current.initialSubtitleError).toBeNull();
    unmount();
  });

  it("retries a refused bitmap subtitle start without subtitles", async () => {
    const fallbackPlan = fixturePlanV3({ session_id: "session-2" });
    const startBodies: Array<Record<string, unknown>> = [];
    let replanCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        if (startBodies.length === 1) {
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "terminal",
            session_id: "session-1",
            terminal: {
              reason: "hdr_transcode_unsupported",
              message: "Enable HDR transcoding to use this subtitle.",
            },
          });
        }
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-2",
            playback_plan: fallbackPlan,
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-2/replan")) {
        replanCount += 1;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: `plan:replan-${replanCount}`,
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () =>
        usePlaybackSession(
          "request-1",
          [],
          [],
          7,
          0,
          false,
          "auto",
          null,
          undefined,
          null,
          { 7: 0 },
          { 7: 0 },
        ),
      { wrapper },
    );

    await waitFor(() => expect(result.current.plan?.plan_id).toBe(fallbackPlan.plan_id));
    expect(startBodies).toHaveLength(2);
    expect(startBodies[0]).toMatchObject({ subtitle_track_index: 0 });
    expect(startBodies[1]).not.toHaveProperty("subtitle_track_index");
    expect(startBodies[0]?.playback_attempt_id).not.toBe(startBodies[1]?.playback_attempt_id);
    expect(result.current.planRevision).toBe(1);
    expect(result.current.error).toBeNull();
    expect(result.current.initialSubtitleErrorTitle).toBe("This HDR format can't be converted");
    expect(result.current.initialSubtitleError).toContain("dynamic range can't be converted");

    act(() => result.current.changeQuality("1080p", 30));
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:replan-1"));
    expect(result.current.initialSubtitleError).toContain("dynamic range can't be converted");

    act(() => result.current.changeSubtitleTrack(0, 45));
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:replan-2"));
    expect(result.current.initialSubtitleErrorTitle).toBeNull();
    expect(result.current.initialSubtitleError).toBeNull();
    unmount();
  });
});

describe("usePlaybackSession output capability changes", () => {
  function outputProbe(initialHDR: boolean) {
    let hdr = initialHDR;
    const listeners = new Set<() => void>();
    const query = {
      get matches() {
        return hdr;
      },
      addEventListener: (_: string, listener: () => void) => listeners.add(listener),
      removeEventListener: (_: string, listener: () => void) => listeners.delete(listener),
    };
    vi.stubGlobal("matchMedia", () => query);
    // Decode answers are probed independently of the media query, so move the
    // simulated decoder along with the output this fixture switches between.
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      hdr && mime === 'video/mp4; codecs="dvh1.08.06"' ? "probably" : "",
    );
    return (nextHDR: boolean) => {
      hdr = nextHDR;
      for (const listener of listeners) listener();
    };
  }

  it("waits for the initial HDR10 probe before starting playback", async () => {
    outputProbe(false);
    let resolveProbe!: (value: MediaCapabilitiesDecodingInfo) => void;
    const probeResult = new Promise<MediaCapabilitiesDecodingInfo>((resolve) => {
      resolveProbe = resolve;
    });
    vi.stubGlobal("navigator", {
      userAgent: "test-browser",
      mediaCapabilities: { decodingInfo: vi.fn(() => probeResult) },
    });

    const startBodies: Array<{
      client_playback_context: { output: { hdr_details: { hdr10: boolean } } };
    }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startBodies.push(JSON.parse(String(init?.body)) as (typeof startBodies)[number]);
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "output_change_v1"],
            outcome: "playable",
            session_id: "session-hdr",
            playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "1080p"),
      { wrapper },
    );
    await act(async () => Promise.resolve());
    expect(startBodies).toHaveLength(0);

    await act(async () => {
      resolveProbe({
        supported: true,
        smooth: true,
        powerEfficient: true,
        keySystemAccess: null,
      });
      await probeResult;
    });
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));
    expect(startBodies).toHaveLength(1);
    expect(startBodies[0]?.client_playback_context.output.hdr_details.hdr10).toBe(true);
    unmount();
  });

  it("retries a terminal start when the window moves onto an HDR output", async () => {
    const setHDR = outputProbe(false);
    const startBodies: Array<{
      start_position?: number;
      client_capabilities: { hdr_details?: { dolby_vision_profiles: number[] } };
    }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as (typeof startBodies)[number];
        startBodies.push(body);
        if (startBodies.length === 1) {
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3", "output_change_v1"],
            outcome: "terminal",
            terminal: { reason: "hdr_transcode_unsupported", message: "HDR unsupported" },
          });
        }
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.error).not.toBeNull());

    act(() => setHDR(true));

    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));
    expect(startBodies).toHaveLength(2);
    expect(startBodies[0]).not.toHaveProperty("start_position");
    expect(startBodies[1]).not.toHaveProperty("start_position");
    expect(startBodies[0]?.client_capabilities.hdr_details?.dolby_vision_profiles).toEqual([]);
    expect(startBodies[1]?.client_capabilities.hdr_details?.dolby_vision_profiles).toEqual([8]);
    unmount();
  });

  it("keeps the Play tap's clock when an output change retries a start that never played", async () => {
    const setHDR = outputProbe(false);
    let starts = 0;
    const routeEvents: Array<{ event: string; diagnostics: Record<string, string> }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        starts += 1;
        if (starts === 1) {
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3", "output_change_v1"],
            outcome: "terminal",
            terminal: { reason: "hdr_transcode_unsupported", message: "HDR unsupported" },
          });
        }
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        });
      }
      if (url.endsWith("/playback/route-events")) {
        routeEvents.push(JSON.parse(String(init?.body)) as (typeof routeEvents)[number]);
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const tap = performance.now();
    markPlaybackIntent("request-1", tap);
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.error).not.toBeNull());

    act(() => setHDR(true));
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));
    const clock = vi.spyOn(performance, "now").mockReturnValue(tap + 3_000);
    try {
      act(() => result.current.reportFirstFrame());
    } finally {
      clock.mockRestore();
    }

    await waitFor(() =>
      expect(routeEvents.filter((event) => event.event === "first_frame")).toHaveLength(1),
    );
    expect(routeEvents.find((event) => event.event === "first_frame")?.diagnostics).toEqual({
      first_frame_ms: "3000",
    });
    unmount();
  });

  it("replans an active HDR route with its tracks and paused state after moving to SDR", async () => {
    const setHDR = outputProbe(true);
    const audio = { id: "file:7:audio:1", index: 1 };
    const subtitle = { id: "file:7:subtitle:2", index: 2 };
    const initialPlan = fixturePlanV3({
      session_id: "session-hdr",
      selected_tracks: { audio, subtitle },
    });
    const replanBodies: Array<{
      operation: string;
      position_seconds: number;
      selected_tracks: { audio?: typeof audio; subtitle?: typeof subtitle };
      client_capabilities: { hdr: boolean };
    }> = [];
    let startCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startCount += 1;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: initialPlan,
        });
      }
      if (url.endsWith("/playback/session-hdr/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as (typeof replanBodies)[number]);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({
            session_id: "session-hdr",
            plan_id: "plan:outputchanged001",
            plan_attempt_key: "v3:outputchanged001",
            selected_tracks: { audio, subtitle },
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));
    act(() => {
      result.current.updatePlaybackState(300, true);
      result.current.updatePlaybackState(321, false);
    });

    act(() => setHDR(false));

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:outputchanged001"));
    expect(startCount).toBe(1);
    expect(replanBodies).toHaveLength(1);
    expect(replanBodies[0]).toMatchObject({
      operation: "output_change",
      position_seconds: 321,
      selected_tracks: { audio, subtitle },
      client_capabilities: { hdr: false },
    });
    expect(result.current.sessionId).toBe("session-hdr");
    expect(result.current.shouldAutoPlay).toBe(false);
    unmount();
  });

  it("seeds an early output replan from the server-resolved source position", async () => {
    const setHDR = outputProbe(true);
    const resumedPlan = fixturePlanV3({ session_id: "session-hdr" });
    resumedPlan.timeline = {
      ...resumedPlan.timeline,
      source_start_seconds: 275,
      player_start_seconds: 5,
      timeline_offset_seconds: 270,
    };
    const replanBodies: Array<{ position_seconds: number }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: resumedPlan,
        });
      }
      if (url.endsWith("/playback/session-hdr/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as { position_seconds: number });
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({
            session_id: "session-hdr",
            plan_id: "plan:resumedoutput001",
            plan_attempt_key: "v3:resumedoutput001",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));

    act(() => result.current.updatePlaybackState(0, false));
    act(() => setHDR(false));

    await waitFor(() => expect(replanBodies).toHaveLength(1));
    expect(replanBodies[0]?.position_seconds).toBe(275);
    unmount();
  });

  it("retires an active plan when the refreshed output has no playable route", async () => {
    const setHDR = outputProbe(true);
    const stoppedSessions: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        });
      }
      if (url.endsWith("/playback/session-hdr/replan")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "terminal",
          terminal: { reason: "hdr_transcode_unsupported", message: "HDR unsupported" },
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") {
        stoppedSessions.push(url);
        return jsonResponse({ outcome: "stopped" });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));

    act(() => setHDR(false));

    await waitFor(() => expect(result.current.plan).toBeNull());
    expect(result.current.sessionId).toBeNull();
    expect(result.current.error).not.toBeNull();
    await waitFor(() => expect(stoppedSessions).toContain("/api/v2/playback/session-hdr"));
    unmount();
  });

  it("uses the latest capabilities when an output change queues behind another replan", async () => {
    const setHDR = outputProbe(true);
    let resolveFirstReplan: ((response: Response) => void) | undefined;
    const firstReplanResponse = new Promise<Response>((resolve) => {
      resolveFirstReplan = resolve;
    });
    const replanBodies: Array<{
      operation: string;
      position_seconds: number;
      client_capabilities: { hdr: boolean };
    }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        });
      }
      if (url.endsWith("/playback/session-hdr/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as (typeof replanBodies)[number]);
        if (replanBodies.length === 1) return firstReplanResponse;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({
            session_id: "session-hdr",
            plan_id: "plan:queuedoutput0001",
            plan_attempt_key: "v3:queuedoutput0001",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));

    act(() => result.current.refreshSubtitles(120));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    act(() => setHDR(false));
    act(() => {
      void result.current.reanchorSeek(555);
    });
    expect(replanBodies).toHaveLength(1);

    await act(async () => {
      resolveFirstReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({
            session_id: "session-hdr",
            plan_id: "plan:trackrefresh0001",
            plan_attempt_key: "v3:trackrefresh0001",
          }),
        }),
      );
      await firstReplanResponse;
    });

    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(replanBodies[1]).toMatchObject({
      operation: "output_change",
      position_seconds: 555,
      client_capabilities: { hdr: false },
    });
    unmount();
  });

  it("runs the latest output refresh before retiring a terminal replan", async () => {
    const setHDR = outputProbe(true);
    let resolveFirstReplan: ((response: Response) => void) | undefined;
    const firstReplanResponse = new Promise<Response>((resolve) => {
      resolveFirstReplan = resolve;
    });
    const replanBodies: Array<{ client_capabilities: { hdr: boolean } }> = [];
    const stoppedSessions: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        });
      }
      if (url.endsWith("/playback/session-hdr/replan")) {
        replanBodies.push(
          JSON.parse(String(init?.body)) as { client_capabilities: { hdr: boolean } },
        );
        if (replanBodies.length === 1) return firstReplanResponse;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({
            session_id: "session-hdr",
            plan_id: "plan:latestoutput0001",
            plan_attempt_key: "v3:latestoutput0001",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") {
        stoppedSessions.push(url);
        return jsonResponse({ outcome: "stopped" });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));

    act(() => setHDR(false));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    act(() => setHDR(true));

    await act(async () => {
      resolveFirstReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "terminal",
          terminal: { reason: "hdr_transcode_unsupported", message: "HDR unsupported" },
        }),
      );
      await firstReplanResponse;
    });

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:latestoutput0001"));
    expect(replanBodies).toHaveLength(2);
    expect(replanBodies[1]).toMatchObject({ client_capabilities: { hdr: true } });
    expect(stoppedSessions).toEqual([]);
    unmount();
  });

  it("retires after a queued user intent also refuses refreshed output", async () => {
    const setHDR = outputProbe(true);
    let resolveOutputReplan: ((response: Response) => void) | undefined;
    const outputReplanResponse = new Promise<Response>((resolve) => {
      resolveOutputReplan = resolve;
    });
    const replanOperations: string[] = [];
    const stoppedSessions: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        });
      }
      if (url.endsWith("/playback/session-hdr/replan")) {
        replanOperations.push((JSON.parse(String(init?.body)) as { operation: string }).operation);
        if (replanOperations.length === 1) return outputReplanResponse;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "terminal",
          terminal: { reason: "hdr_transcode_unsupported", message: "HDR unsupported" },
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") {
        stoppedSessions.push(url);
        return jsonResponse({ outcome: "stopped" });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));

    act(() => setHDR(false));
    await waitFor(() => expect(replanOperations).toEqual(["output_change"]));
    act(() => result.current.changeQuality("1080p", 91));
    expect(replanOperations).toEqual(["output_change"]);

    await act(async () => {
      resolveOutputReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "terminal",
          terminal: { reason: "hdr_transcode_unsupported", message: "HDR unsupported" },
        }),
      );
      await outputReplanResponse;
    });

    await waitFor(() => expect(replanOperations).toEqual(["output_change", "quality_change"]));
    await waitFor(() => expect(result.current.plan).toBeNull());
    expect(stoppedSessions).toContain("/api/v2/playback/session-hdr");
    unmount();
  });

  it("keeps the active plan when an output refresh request fails", async () => {
    const setHDR = outputProbe(true);
    const stoppedSessions: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        });
      }
      if (url.endsWith("/playback/session-hdr/replan")) {
        return jsonResponse({ error: "temporary failure" }, { status: 503 });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") {
        stoppedSessions.push(url);
        return jsonResponse({ outcome: "stopped" });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));
    const activePlan = result.current.plan;

    act(() => setHDR(false));

    await waitFor(() => expect(result.current.replanning).toBe(false));
    expect(result.current.plan).toBe(activePlan);
    expect(result.current.sessionId).toBe("session-hdr");
    expect(result.current.error).toBeNull();
    expect(stoppedSessions).toEqual([]);
    unmount();
  });

  it("queues the latest user intent behind an output refresh", async () => {
    const setHDR = outputProbe(true);
    let resolveOutputReplan: ((response: Response) => void) | undefined;
    const outputReplanResponse = new Promise<Response>((resolve) => {
      resolveOutputReplan = resolve;
    });
    const replanBodies: Array<{
      operation: string;
      quality_preference: string;
      selected_tracks: { audio?: { index?: number } };
    }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        });
      }
      if (url.endsWith("/playback/session-hdr/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as (typeof replanBodies)[number]);
        if (replanBodies.length === 1) return outputReplanResponse;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({
            session_id: "session-hdr",
            plan_id: "plan:userintent00001",
            plan_attempt_key: "v3:userintent00001",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));

    act(() => setHDR(false));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    act(() => result.current.switchAudioTrack(2, 90));
    act(() => result.current.changeQuality("1080p", 91));
    expect(replanBodies).toHaveLength(1);

    await act(async () => {
      resolveOutputReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        }),
      );
      await outputReplanResponse;
    });

    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(replanBodies[1]).toMatchObject({
      operation: "quality_change",
      quality_preference: "1080p",
    });
    expect(result.current.qualityPreference).toBe("1080p");
    unmount();
  });

  it("drops a predecessor failure after an output refresh adopts a new plan", async () => {
    const setHDR = outputProbe(true);
    let resolveOutputReplan: ((response: Response) => void) | undefined;
    const outputReplanResponse = new Promise<Response>((resolve) => {
      resolveOutputReplan = resolve;
    });
    const replanOperations: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        });
      }
      if (url.endsWith("/playback/session-hdr/replan")) {
        replanOperations.push((JSON.parse(String(init?.body)) as { operation: string }).operation);
        return outputReplanResponse;
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));

    act(() => setHDR(false));
    await waitFor(() => expect(replanOperations).toEqual(["output_change"]));
    act(() => result.current.recoverFromFailure({ classification: "decoder_failure" }, 120));

    await act(async () => {
      resolveOutputReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3", "output_change_v1"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({
            session_id: "session-hdr",
            plan_id: "plan:replacement0001",
            plan_attempt_key: "v3:replacement0001",
          }),
        }),
      );
      await outputReplanResponse;
    });

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:replacement0001"));
    expect(replanOperations).toEqual(["output_change"]);
    unmount();
  });

  it("keeps playback unchanged when the server lacks output-change support", async () => {
    const setHDR = outputProbe(true);
    const replanOperations: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-hdr",
          playback_plan: fixturePlanV3({ session_id: "session-hdr" }),
        });
      }
      if (url.endsWith("/playback/session-hdr/replan")) {
        replanOperations.push((JSON.parse(String(init?.body)) as { operation: string }).operation);
        return jsonResponse({ error: "unsupported operation" }, { status: 400 });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-hdr"));

    await act(async () => {
      setHDR(false);
      await Promise.resolve();
    });

    expect(replanOperations).toEqual([]);
    expect(result.current.sessionId).toBe("session-hdr");
    expect(result.current.plan).not.toBeNull();
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
        return jsonResponse({ outcome: "stopped" });
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
      expect(result.current.errorReason).toBe("no_playable_route");
    });
    expect(result.current.streamUrl).toBeNull();
    expect(result.current.sessionId).toBeNull();
    expect(stoppedSessions).toEqual(["/api/v2/playback/session-1"]);

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
        return jsonResponse({ outcome: "stopped" });
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
    expect(stoppedSessions).toEqual(["/api/v2/playback/session-1"]);

    unmount();
  });
});

describe("usePlaybackSession replans", () => {
  it("drops a queued predecessor failure after the in-flight replan adopts a new plan", async () => {
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
        return firstReplanResponse;
      }
      if (url.endsWith("/playback/route-events")) {
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") {
        return jsonResponse({ outcome: "stopped" });
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
      void result.current.reanchorSeek(300);
      result.current.recoverFromFailure({ classification: "decoder_error" }, 450);
      void result.current.reanchorSeek(600);
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

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:1111111111111111"));
    expect(
      replanBodies.map(({ operation, position_seconds }) => ({ operation, position_seconds })),
    ).toEqual([{ operation: "track_change", position_seconds: 120 }]);

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
        return jsonResponse({ outcome: "stopped" });
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
      void result.current.reanchorSeek(300);
      void result.current.reanchorSeek(450);
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
        return jsonResponse({ outcome: "stopped" });
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

describe("usePlaybackSession server-invalidated plans", () => {
  function invalidationFetchMock(replanBodies: Array<Record<string, unknown>>, replan: unknown) {
    return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
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
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse(replan);
      }
      if (url.endsWith("/playback/route-events")) {
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") {
        return jsonResponse({ outcome: "stopped" });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
  }

  it("recovers off the invalidated plan and excludes its attempt key", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      invalidationFetchMock(replanBodies, {
        protocol_version: 3,
        server_features: ["playback_plan_v3"],
        outcome: "playable",
        session_id: "session-1",
        playback_plan: fixturePlanV3({
          plan_id: "plan:2222222222222222",
          plan_attempt_key: "v3:2222222222222222",
          delivery: "server_transcode_hls",
        }),
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef"));

    let outcome: boolean | undefined;
    await act(async () => {
      outcome = await result.current.invalidatePlan(
        "plan:0123456789abcdef",
        "video_copy_unsafe",
        450,
      );
    });

    // The server only pushes the command to a session that promised to handle
    // it, so the promise has to be on the wire for any of this to be reachable.
    const startCall = vi
      .mocked(fetch)
      .mock.calls.find(([url]) => String(url).endsWith("/playback/start"));
    const startBody = JSON.parse(String(startCall?.[1]?.body)) as { client_features: string[] };
    expect(startBody.client_features).toContain("plan_invalidated_v1");

    expect(outcome).toBe(true);
    expect(replanBodies).toHaveLength(1);
    expect(replanBodies[0]).toMatchObject({
      operation: "failure_recovery",
      failed_plan_id: "plan:0123456789abcdef",
      position_seconds: 450,
      // The invalidated route is excluded by key, so the replacement plan
      // cannot be the same copy route the server just disqualified.
      attempted_plan_keys: ["v3:0123456789abcdef"],
      failure: { classification: "video_copy_unsafe" },
    });
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:2222222222222222"));

    unmount();
  });

  function deferred<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>((settle) => {
      resolve = settle;
    });
    return { promise, resolve };
  }

  /**
   * A start and a replan whose response is held open, so a test can act while
   * the client has a decision in flight — the state the server is always in
   * when it pushes an invalidation: it commits the replacement plan and starts
   * the copy-safety scan behind it before the response is on the wire.
   */
  function gatedReplanFetchMock(
    replanBodies: Array<Record<string, unknown>>,
    replans: unknown[],
    gate: Promise<void>,
  ) {
    return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
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
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        if (replanBodies.length === 1) await gate;
        return jsonResponse(replans.shift());
      }
      if (url.endsWith("/playback/route-events")) {
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") {
        return jsonResponse({ outcome: "stopped" });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
  }

  function playableDecision(plan: ReturnType<typeof fixturePlanV3>) {
    return {
      protocol_version: 3,
      server_features: ["playback_plan_v3"],
      outcome: "playable",
      session_id: "session-1",
      playback_plan: plan,
    };
  }

  // The server commits the replacement plan and starts the scan behind it
  // before the client can read the response, so the invalidation can name a
  // plan this client has not adopted yet. Deciding against the plan on screen
  // would complete the command as a no-op and then let the pending response
  // install the very route the server withdrew.
  it("waits out an in-flight replan and recovers off the plan it adopts", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
    const gate = deferred<void>();
    vi.stubGlobal(
      "fetch",
      gatedReplanFetchMock(
        replanBodies,
        [
          playableDecision(
            fixturePlanV3({
              plan_id: "plan:2222222222222222",
              plan_attempt_key: "v3:2222222222222222",
            }),
          ),
          playableDecision(
            fixturePlanV3({
              plan_id: "plan:3333333333333333",
              plan_attempt_key: "v3:3333333333333333",
              delivery: "server_transcode_hls",
            }),
          ),
        ],
        gate.promise,
      ),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef"));

    act(() => {
      result.current.changeQuality("720p", 100);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    let invalidation: Promise<boolean> | undefined;
    act(() => {
      invalidation = result.current.invalidatePlan(
        "plan:2222222222222222",
        "video_copy_unsafe",
        100,
      );
    });
    // Nothing may be decided yet: the plan the command names is still in the
    // response the client has not read.
    expect(replanBodies).toHaveLength(1);

    let outcome: boolean | undefined;
    await act(async () => {
      gate.resolve();
      outcome = await invalidation;
    });

    expect(outcome).toBe(true);
    expect(replanBodies).toHaveLength(2);
    expect(replanBodies[1]).toMatchObject({
      operation: "failure_recovery",
      failed_plan_id: "plan:2222222222222222",
      // The plan that was invalidated mid-adoption is the one excluded, not the
      // one that was on screen when the command arrived.
      attempted_plan_keys: ["v3:2222222222222222"],
      failure: { classification: "video_copy_unsafe" },
    });
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:3333333333333333"));

    unmount();
  });

  // The mirror image: the client really did move past the invalidated plan
  // while the command was in flight. Waiting must not turn that into a replan —
  // it would evict a route the server never complained about.
  it("stays a no-op for a plan the in-flight replan replaced", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
    const gate = deferred<void>();
    vi.stubGlobal(
      "fetch",
      gatedReplanFetchMock(
        replanBodies,
        [
          playableDecision(
            fixturePlanV3({
              plan_id: "plan:2222222222222222",
              plan_attempt_key: "v3:2222222222222222",
            }),
          ),
        ],
        gate.promise,
      ),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef"));

    act(() => {
      result.current.changeQuality("720p", 100);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    let invalidation: Promise<boolean> | undefined;
    act(() => {
      invalidation = result.current.invalidatePlan(
        "plan:0123456789abcdef",
        "video_copy_unsafe",
        100,
      );
    });

    let outcome: boolean | undefined;
    await act(async () => {
      gate.resolve();
      outcome = await invalidation;
    });

    // Reported as handled, with no second replan: the invalidated route is gone.
    expect(outcome).toBe(true);
    expect(replanBodies).toHaveLength(1);
    expect(result.current.plan?.plan_id).toBe("plan:2222222222222222");

    unmount();
  });

  it("reports failure when the replan produces no replacement plan", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      invalidationFetchMock(replanBodies, {
        protocol_version: 3,
        server_features: ["playback_plan_v3"],
        outcome: "adaptation_unavailable",
        terminal: {
          reason: "video_conversion_unsupported",
          message: "No executor can transcode this source.",
          retryable: false,
        },
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef"));

    let outcome: boolean | undefined;
    await act(async () => {
      outcome = await result.current.invalidatePlan(
        "plan:0123456789abcdef",
        "video_copy_unsafe",
        30,
      );
    });

    // The caller rejects the realtime command on false, which is what makes the
    // server stop the session instead of leaving the copy route playing.
    expect(outcome).toBe(false);
    expect(replanBodies).toHaveLength(1);

    unmount();
  });
});

describe("usePlaybackSession server-invalidated plans", () => {
  // An invalidation waits out whatever is still being adopted, so it decides
  // against the plan that actually won rather than the one on screen. The wait
  // has to be scoped to the request that can still own the session: a start
  // abandoned by a version switch cannot install anything any more, and a hung
  // one would otherwise hold the invalidation past the server's 8s deadline —
  // which stops the very session that is playing fine.
  it("does not wait on a superseded start that never settles", async () => {
    let startCount = 0;
    const replanBodies: Array<{ operation: string }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startCount += 1;
        if (startCount === 1) {
          // The abandoned request: it never settles, and nothing will ever
          // count it out.
          return new Promise<Response>(() => {});
        }
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-2",
            playback_plan: fixturePlanV3({ session_id: "session-2" }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-2/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as { operation: string });
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:2222222222222222",
            plan_attempt_key: "v3:2222222222222222",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, rerender, unmount } = renderHook(
      ({ requestKey, fileId }: { requestKey: string; fileId: number }) =>
        usePlaybackSession(requestKey, [], [], fileId, 0, false, "auto"),
      { wrapper, initialProps: { requestKey: "episode-1", fileId: 7 } },
    );
    await waitFor(() => expect(startCount).toBe(1));

    rerender({ requestKey: "episode-2", fileId: 8 });
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    const planId = result.current.plan?.plan_id;
    if (!planId) throw new Error("expected an adopted plan");

    const outcome = await act(async () =>
      Promise.race([
        result.current.invalidatePlan(planId, "video_copy_unsafe", 120),
        new Promise<"blocked">((resolve) => {
          setTimeout(() => resolve("blocked"), 500);
        }),
      ]),
    );

    expect(outcome).toBe(true);
    expect(replanBodies.map(({ operation }) => operation)).toEqual(["failure_recovery"]);
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:2222222222222222"));

    unmount();
  });

  // The scoping must not weaken the guarantee it was built for: an invalidation
  // that arrives while the *current* start is still in flight still waits, so
  // it decides against the plan that response installs rather than no-opping
  // against the one already on screen.
  it("still waits for the start that currently owns the session", async () => {
    let releaseStart: ((response: Response) => void) | undefined;
    const pendingStart = new Promise<Response>((resolve) => {
      releaseStart = resolve;
    });
    let startCount = 0;
    const replanBodies: Array<{ operation: string }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startCount += 1;
        return pendingStart;
      }
      if (url.endsWith("/playback/session-1/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as { operation: string });
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            session_id: "session-1",
            plan_id: "plan:3333333333333333",
            plan_attempt_key: "v3:3333333333333333",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(startCount).toBe(1));

    // The verdict names a plan this client has not read the response for yet.
    let settled = false;
    const invalidation = result.current
      .invalidatePlan("plan:0123456789abcdef", "video_copy_unsafe", 120)
      .then((adopted) => {
        settled = true;
        return adopted;
      });
    await act(async () => {
      await Promise.resolve();
    });
    expect(settled).toBe(false);
    expect(replanBodies).toHaveLength(0);

    await act(async () => {
      releaseStart?.(
        jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({ session_id: "session-1" }),
          },
          { status: 201 },
        ),
      );
      await expect(invalidation).resolves.toBe(true);
    });
    expect(replanBodies.map(({ operation }) => operation)).toEqual(["failure_recovery"]);

    unmount();
  });
});

describe("usePlaybackSession first frame", () => {
  it("reports first_frame once per playback attempt, timed from the play tap", async () => {
    const startBodies: Array<{ playback_attempt_id: string }> = [];
    const routeEvents: Array<{
      event: string;
      playback_attempt_id: string;
      diagnostics: Record<string, string>;
    }> = [];
    let releaseSwitchStart: (() => void) | undefined;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startBodies.push(JSON.parse(String(init?.body)) as { playback_attempt_id: string });
        const sessionId = `session-${startBodies.length}`;
        if (startBodies.length === 2) {
          await new Promise<void>((resolve) => {
            releaseSwitchStart = resolve;
          });
        }
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: sessionId,
            playback_plan: fixturePlanV3({ session_id: sessionId }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            session_id: "session-1",
            plan_id: "plan:fedcba9876543210",
            plan_attempt_key: "v3:fedcba9876543210",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) {
        routeEvents.push(JSON.parse(String(init?.body)) as (typeof routeEvents)[number]);
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const firstFrames = () => routeEvents.filter((event) => event.event === "first_frame");
    // The clock is pinned only around the synchronous calls that read it, so
    // React's scheduler keeps a real clock everywhere else.
    const at = (now: number, run: () => void) => {
      const clock = vi.spyOn(performance, "now").mockReturnValue(now);
      try {
        act(run);
      } finally {
        clock.mockRestore();
      }
    };

    const tap = performance.now();
    markPlaybackIntent("request-1", tap);
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    at(tap + 2_450, () => {
      result.current.reportFirstFrame();
      result.current.reportFirstFrame();
    });
    await waitFor(() => expect(firstFrames()).toHaveLength(1));
    expect(firstFrames()[0]).toMatchObject({
      playback_attempt_id: startBodies[0]?.playback_attempt_id,
      diagnostics: { first_frame_ms: "2450" },
    });

    // A replan keeps the attempt, so its first frame is not the attempt's.
    act(() => result.current.changeQuality("720p", 30));
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:fedcba9876543210"));
    at(tap + 9_000, () => result.current.reportFirstFrame());

    // A version switch starts a new attempt, timed from the switch. Until its
    // plan is adopted, the frame on screen belongs to the previous attempt.
    at(tap + 10_000, () => result.current.switchVersion(99, 30));
    await waitFor(() => expect(startBodies).toHaveLength(2));
    at(tap + 10_100, () => result.current.reportFirstFrame());
    act(() => releaseSwitchStart?.());
    await waitFor(() => expect(result.current.sessionId).toBe("session-2"));
    at(tap + 10_800, () => result.current.reportFirstFrame());

    await waitFor(() => expect(firstFrames()).toHaveLength(2));
    expect(firstFrames()[1]).toMatchObject({
      playback_attempt_id: startBodies[1]?.playback_attempt_id,
      diagnostics: { first_frame_ms: "800" },
    });

    unmount();
  });

  it("reports first_frame without a duration when no play tap started the attempt", async () => {
    const routeEvents: Array<{ event: string; diagnostics: Record<string, string> }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
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
        routeEvents.push(JSON.parse(String(init?.body)) as (typeof routeEvents)[number]);
        return new Response(null, { status: 202 });
      }
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    // A tap for another request, then a deep link to this one: the stale mark
    // must not time this attempt.
    markPlaybackIntent("request-other");
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-deep-link", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    act(() => result.current.reportFirstFrame());

    await waitFor(() =>
      expect(routeEvents.filter((event) => event.event === "first_frame")).toHaveLength(1),
    );
    expect(routeEvents.find((event) => event.event === "first_frame")?.diagnostics).toEqual({});

    unmount();
  });
});

describe("usePlaybackSession mid-stream reconnect", () => {
  type Body = Record<string, unknown>;

  function playable(sessionId: string, plan = fixturePlanV3({ session_id: sessionId })) {
    return {
      protocol_version: 3,
      server_features: ["playback_plan_v3"],
      outcome: "playable",
      session_id: sessionId,
      playback_plan: plan,
    };
  }

  /**
   * Serves the first start, then hands every replan (and any later start) to
   * the test. A reply that throws stands in for a server that cannot be
   * reached: `fetch` rejects with a TypeError.
   */
  function reconnectHarness(options: {
    replan: () => Response | Promise<Response>;
    restart?: (body: Body) => Response | Promise<Response>;
    initialPlan?: ReturnType<typeof fixturePlanV3>;
  }) {
    const startBodies: Body[] = [];
    const replanBodies: Body[] = [];
    const stopped: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          const body = JSON.parse(String(init?.body)) as Body;
          startBodies.push(body);
          if (startBodies.length > 1 && options.restart) return options.restart(body);
          return jsonResponse(playable("session-1", options.initialPlan), { status: 201 });
        }
        if (url.endsWith("/replan")) {
          replanBodies.push(JSON.parse(String(init?.body)) as Body);
          return options.replan();
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") {
          stopped.push(url);
          return jsonResponse({ outcome: "stopped" });
        }
        throw new Error(`Unexpected request: ${url}`);
      }),
    );
    return { startBodies, replanBodies, stopped };
  }

  const unreachable = (): Response => {
    throw new TypeError("Failed to fetch");
  };

  async function startPlaying() {
    const rendered = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(rendered.result.current.planRevision).toBe(1));
    // The stream played: a network failure from here on is a lost connection.
    act(() => rendered.result.current.updatePlaybackState(100, true));
    // RTL's waitFor polls on real timers; from here the test drives time.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    return rendered;
  }

  async function advance(ms: number) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  }

  afterEach(() => {
    vi.useRealTimers();
  });

  it("waits for the server and resumes the same route at the saved position", async () => {
    let serverUp = false;
    const harness = reconnectHarness({
      replan: () =>
        serverUp
          ? jsonResponse(
              playable(
                "session-1",
                fixturePlanV3({
                  plan_id: "plan:1111111111111111",
                  timeline: { ...fixturePlanV3().timeline, player_start_seconds: 120 },
                }),
              ),
            )
          : unreachable(),
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(120, true));
    expect(result.current.connectionStatus).toBe("reconnecting");
    // The player pauses while the server is away; the adopted plan must not
    // inherit that pause.
    act(() => result.current.updatePlaybackState(120, false));

    await advance(RECONNECT_BASE_DELAY_MS);
    expect(harness.replanBodies).toHaveLength(1);
    expect(result.current.connectionStatus).toBe("reconnecting");
    expect(result.current.error).toBeNull();
    expect(result.current.planRevision).toBe(1);

    serverUp = true;
    await advance(reconnectDelayMs(1));
    expect(harness.replanBodies).toHaveLength(2);
    expect(result.current.connectionStatus).toBe("connected");
    expect(result.current.planRevision).toBe(2);
    expect(result.current.plan?.plan_id).toBe("plan:1111111111111111");
    expect(result.current.initialPosition).toBe(120);
    expect(result.current.shouldAutoPlay).toBe(true);
    expect(result.current.sessionId).toBe("session-1");

    // The connection failed, not the route: the request keeps it eligible.
    const body = harness.replanBodies[1];
    expect(body?.operation).toBe("track_change");
    expect(body?.position_seconds).toBe(120);
    expect(body?.attempted_plan_keys).toEqual([]);
    expect(body?.failure).toBeUndefined();
    expect(harness.startBodies).toHaveLength(1);
    expect(harness.stopped).toEqual([]);

    // Nothing else is scheduled once playback is back.
    await advance(RECONNECT_MAX_DELAY_MS * 2);
    expect(harness.replanBodies).toHaveLength(2);

    unmount();
  });

  it("starts a new session at the saved position when the old one did not survive a restart", async () => {
    const harness = reconnectHarness({
      replan: () =>
        jsonResponse(
          { error: "playback_session_not_found", message: "Playback session not found" },
          { status: 404 },
        ),
      restart: () => jsonResponse(playable("session-2"), { status: 201 }),
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(240, false));
    await advance(RECONNECT_BASE_DELAY_MS);

    expect(harness.replanBodies).toHaveLength(1);
    expect(harness.startBodies).toHaveLength(2);
    const restart = harness.startBodies[1];
    expect(restart?.start_position).toBe(240);
    expect(restart?.file_id).toBe(7);
    expect(restart?.audio_track_index).toBe(0);
    expect(restart?.playback_attempt_id).not.toBe(harness.startBodies[0]?.playback_attempt_id);
    expect(result.current.sessionId).toBe("session-2");
    expect(result.current.connectionStatus).toBe("connected");
    // The viewer had paused before the drop, so playback stays paused.
    expect(result.current.shouldAutoPlay).toBe(false);
    expect(harness.stopped).toEqual(["/api/v2/playback/session-1"]);

    unmount();
  });

  it("starts a new session when the server's installation changed", async () => {
    let restarts = 0;
    const harness = reconnectHarness({
      // A replan always carries the old session's installation, so the
      // server refuses it every time.
      replan: () =>
        jsonResponse(
          { error: "installation_changed", message: "Playback installation changed" },
          { status: 409 },
        ),
      // The first start still used cached capabilities; the start helper
      // drops them on this refusal, so the next start succeeds.
      restart: () => {
        restarts += 1;
        return restarts === 1
          ? jsonResponse(
              { error: "installation_changed", message: "Playback installation changed" },
              { status: 409 },
            )
          : jsonResponse(playable("session-2"), { status: 201 });
      },
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(150, true));
    await advance(RECONNECT_BASE_DELAY_MS);
    expect(harness.replanBodies).toHaveLength(1);
    expect(harness.startBodies).toHaveLength(2);
    expect(result.current.connectionStatus).toBe("reconnecting");

    await advance(reconnectDelayMs(1));
    expect(harness.startBodies).toHaveLength(3);
    expect(harness.startBodies[2]?.start_position).toBe(150);
    expect(result.current.sessionId).toBe("session-2");
    expect(result.current.connectionStatus).toBe("connected");

    unmount();
  });

  it("restarts with subtitles off when the viewer had them off", async () => {
    const harness = reconnectHarness({
      // A sidecar track is on offer, but the lost plan had none selected.
      initialPlan: fixturePlanV3({
        subtitle: { mode: "off", inventory: [fixtureSubtitleInventoryItemV3()] },
      }),
      replan: () => jsonResponse({ error: "playback_session_not_found" }, { status: 404 }),
      restart: () => jsonResponse(playable("session-2"), { status: 201 }),
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(90, true));
    await advance(RECONNECT_BASE_DELAY_MS);

    expect(result.current.sessionId).toBe("session-2");
    const restart = harness.startBodies[1];
    // A start that names no subtitle track plays without one; the protocol
    // has no other spelling of "off" and rejects a negative index.
    expect(restart).not.toHaveProperty("subtitle_track_index");
    expect(restart).not.toHaveProperty("subtitle_track_id");
    expect(restart?.audio_track_index).toBe(0);

    unmount();
  });

  it("restarts with the subtitle the viewer had on", async () => {
    const harness = reconnectHarness({
      initialPlan: fixturePlanV3({
        selected_tracks: {
          audio: { id: "file:7:audio:1", index: 1 },
          subtitle: { id: "file:7:subtitle:0", index: 0 },
        },
        subtitle: { mode: "render", inventory: [fixtureSubtitleInventoryItemV3()] },
      }),
      replan: () => jsonResponse({ error: "playback_session_not_found" }, { status: 404 }),
      restart: () => jsonResponse(playable("session-2"), { status: 201 }),
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(90, true));
    await advance(RECONNECT_BASE_DELAY_MS);

    expect(result.current.sessionId).toBe("session-2");
    expect(harness.startBodies[1]?.subtitle_track_index).toBe(0);
    expect(harness.startBodies[1]?.audio_track_index).toBe(1);

    unmount();
  });

  it("retries a refused burn-in subtitle restart without subtitles, like a normal start", async () => {
    const harness = reconnectHarness({
      initialPlan: fixturePlanV3({
        selected_tracks: {
          audio: { id: "file:7:audio:1", index: 1 },
          subtitle: { id: "file:7:subtitle:3", index: 3 },
        },
        subtitle: {
          mode: "burn_in",
          inventory: [
            fixtureSubtitleInventoryItemV3({
              combined_index: 3,
              codec: "hdmv_pgs_subtitle",
              delivery: "burn_in_only",
              url: undefined,
            }),
          ],
        },
      }),
      replan: () => jsonResponse({ error: "playback_session_not_found" }, { status: 404 }),
      restart: (body) =>
        body.subtitle_track_index === 3
          ? jsonResponse(
              {
                protocol_version: 3,
                server_features: ["playback_plan_v3"],
                outcome: "terminal",
                session_id: "session-refused",
                terminal: {
                  reason: "subtitle_burn_in_source_unsupported",
                  message: "The selected subtitle can't be burned into this source.",
                  retryable: false,
                },
              },
              { status: 201 },
            )
          : jsonResponse(playable("session-2"), { status: 201 }),
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(500, true));
    await advance(RECONNECT_BASE_DELAY_MS);

    expect(harness.startBodies).toHaveLength(3);
    const [, refused, fallback] = harness.startBodies;
    expect(refused?.subtitle_track_index).toBe(3);
    expect(fallback).not.toHaveProperty("subtitle_track_index");
    expect(fallback?.start_position).toBe(500);
    expect(fallback?.audio_track_index).toBe(1);
    expect(fallback?.playback_attempt_id).not.toBe(refused?.playback_attempt_id);

    expect(result.current.connectionStatus).toBe("connected");
    expect(result.current.sessionId).toBe("session-2");
    expect(result.current.shouldAutoPlay).toBe(true);
    // The same notice a refused bitmap subtitle at the initial start raises.
    expect(result.current.initialSubtitleErrorTitle).toBe("That subtitle track can't be used");
    expect(result.current.initialSubtitleError).toBe(
      "The selected subtitle can't be burned into this source.",
    );
    expect(harness.stopped).toEqual(["/api/v2/playback/session-1"]);

    unmount();
  });

  it("backs off, gives up with a retry, and recovers when the viewer tries again", async () => {
    let serverUp = false;
    const harness = reconnectHarness({
      replan: () => (serverUp ? jsonResponse(playable("session-1")) : unreachable()),
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(300, true));
    // Reporting again while a cycle runs does not start a second one.
    act(() => result.current.recoverConnection(310, true));

    let elapsed = 0;
    for (let attempt = 0; attempt < RECONNECT_MAX_ATTEMPTS; attempt += 1) {
      await advance(reconnectDelayMs(attempt) - 1);
      expect(harness.replanBodies).toHaveLength(attempt);
      await advance(1);
      expect(harness.replanBodies).toHaveLength(attempt + 1);
      elapsed += reconnectDelayMs(attempt);
    }
    expect(elapsed).toBeLessThanOrEqual(150_000);
    expect(harness.replanBodies.every((body) => body.position_seconds === 300)).toBe(true);

    expect(result.current.connectionStatus).toBe("lost");
    expect(result.current.connectionErrorTitle).toBe("Connection lost");
    expect(result.current.connectionError).toContain("try again");
    expect(result.current.connectionError).not.toContain("start playback");
    // The stream that played is kept so the viewer can retry from it.
    expect(result.current.plan).not.toBeNull();
    expect(result.current.error).toBeNull();

    await advance(RECONNECT_MAX_DELAY_MS * 4);
    expect(harness.replanBodies).toHaveLength(RECONNECT_MAX_ATTEMPTS);

    serverUp = true;
    act(() => result.current.retryConnection());
    expect(result.current.connectionStatus).toBe("reconnecting");
    await advance(0);
    expect(harness.replanBodies).toHaveLength(RECONNECT_MAX_ATTEMPTS + 1);
    expect(harness.replanBodies.at(-1)?.position_seconds).toBe(300);
    expect(result.current.connectionStatus).toBe("connected");
    expect(result.current.planRevision).toBe(2);

    unmount();
  });

  it("shows the server's refusal when the restarted session is refused", async () => {
    reconnectHarness({
      replan: () => jsonResponse({ error: "not_found" }, { status: 404 }),
      restart: () =>
        jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "terminal",
            terminal: { reason: "source_unavailable", retryable: false },
          },
          { status: 201 },
        ),
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(60, true));
    await advance(RECONNECT_BASE_DELAY_MS);

    expect(result.current.connectionStatus).toBe("lost");
    expect(result.current.connectionErrorTitle).toBe("This video is no longer available");
    expect(result.current.plan).not.toBeNull();

    unmount();
  });

  it("stops retrying when the viewer leaves", async () => {
    const harness = reconnectHarness({ replan: unreachable });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(30, true));
    await advance(RECONNECT_BASE_DELAY_MS);
    expect(harness.replanBodies).toHaveLength(1);

    unmount();
    await advance(RECONNECT_MAX_DELAY_MS * 4);
    expect(harness.replanBodies).toHaveLength(1);
  });

  it("reconnects when recovering a broken stream cannot reach the server", async () => {
    let serverUp = false;
    const harness = reconnectHarness({
      replan: () => (serverUp ? jsonResponse(playable("session-1")) : unreachable()),
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverFromFailure({ classification: "decoder_error" }, 75));
    await advance(0);
    expect(harness.replanBodies[0]?.operation).toBe("failure_recovery");
    expect(result.current.connectionStatus).toBe("reconnecting");
    expect(result.current.error).toBeNull();

    // Transport failures while reconnecting do not start route recovery.
    act(() => result.current.recoverFromFailure({ classification: "decoder_error" }, 75));
    await advance(0);
    expect(harness.replanBodies).toHaveLength(1);

    serverUp = true;
    await advance(RECONNECT_BASE_DELAY_MS);
    expect(harness.replanBodies[1]?.operation).toBe("track_change");
    expect(harness.replanBodies[1]?.position_seconds).toBe(75);
    expect(result.current.connectionStatus).toBe("connected");

    unmount();
  });

  it("keeps one budget when the reconnected transport fails before its first frame", async () => {
    let serverUp = true;
    const harness = reconnectHarness({
      replan: () => (serverUp ? jsonResponse(playable("session-1")) : unreachable()),
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(40, true));
    await advance(RECONNECT_BASE_DELAY_MS);
    expect(result.current.connectionStatus).toBe("connected");
    expect(harness.replanBodies).toHaveLength(1);

    // The new transport fails on the network before its first frame, so the
    // player reports a route failure; the server is gone again.
    serverUp = false;
    act(() => result.current.recoverFromFailure({ classification: "decoder_error" }, 40));
    await advance(0);
    expect(harness.replanBodies[1]?.operation).toBe("failure_recovery");
    expect(result.current.connectionStatus).toBe("reconnecting");

    // The cycle continues where the last one stopped instead of starting
    // over, so a server that keeps dropping new streams still runs out.
    await advance(reconnectDelayMs(1) - 1);
    expect(harness.replanBodies).toHaveLength(2);
    await advance(1);
    expect(harness.replanBodies).toHaveLength(3);
    for (let attempt = 2; attempt < RECONNECT_MAX_ATTEMPTS; attempt += 1) {
      await advance(reconnectDelayMs(attempt));
    }
    expect(result.current.connectionStatus).toBe("lost");
    expect(harness.replanBodies).toHaveLength(RECONNECT_MAX_ATTEMPTS + 1);

    unmount();
  });

  it("keeps the error for a recovery that fails before the stream ever played", async () => {
    const harness = reconnectHarness({ replan: unreachable });
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.planRevision).toBe(1));

    act(() => result.current.recoverFromFailure({ classification: "startup_timeout" }, 0));
    await waitFor(() => expect(result.current.error).toBe("Failed to fetch"));
    expect(result.current.connectionStatus).toBe("connected");
    expect(harness.replanBodies).toHaveLength(1);

    unmount();
  });

  it("does not reconnect a session that never started", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          return jsonResponse({ error: "internal_error" }, { status: 500 });
        }
        throw new Error(`Unexpected request: ${url}`);
      }),
    );
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.error).toContain("could not start playback"));

    act(() => result.current.recoverConnection(10, true));
    expect(result.current.connectionStatus).toBe("connected");

    unmount();
  });
});
