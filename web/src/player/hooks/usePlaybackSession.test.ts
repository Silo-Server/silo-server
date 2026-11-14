import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";

import { forgetBandwidthEstimate, recordObservedBandwidthLimitKbps } from "../bandwidth-probe";
import { PlayerConfigProvider, type PlayerConfig } from "../context/PlayerConfigContext";
import { markPlaybackIntent } from "../first-frame";
import {
  buildReplanRequestV3,
  buildStartRequestV3,
  routeEventPlanIdentityV3,
  VIDEO_CLIENT_FEATURES_V3,
} from "../playback-session-wire-v3";
import { PLAN_INVALIDATED_DEFAULT_AUDIO_RECONCILIATION } from "../realtime-protocol";
import type { PlayerAudioTrack, PlayerFileVersion, PlayerSubtitleInfo } from "../types";
import type { SubtitleInventoryItemV3 } from "../protocol-v3";
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
  forgetBandwidthEstimate();
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
  // `default_audio_reconcile_response_v1` is part of the video surface's promise
  // — the server gates the audio reconciliation withdrawal on it, so a start
  // that omits it is sent no withdrawal at all and plays the wrong audio until
  // its next start. `plan_invalidated_v1` stays alongside it for genuine route
  // failures.
  it("advertises only the surface's own features", () => {
    expect(
      buildStartRequestV3({ ...startBase, extraClientFeatures: VIDEO_CLIENT_FEATURES_V3 })
        .client_features,
    ).toEqual([
      "playback_plan_v3",
      "plan_invalidated_v1",
      "marker_thumbnails_v1",
      "default_audio_reconcile_response_v1",
      "source_committed_event_v1",
      "inventory_updated_event_v1",
      "deferred_track_inventory_v1",
    ]);
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

  it("maps a carried audio track id onto carried_audio_track_id", () => {
    expect(
      buildStartRequestV3({ ...startBase, carriedAudioTrackID: "file:7:audio:0" }),
    ).toMatchObject({ carried_audio_track_id: "file:7:audio:0" });
  });

  it("omits carried_audio_track_id when no audio track is carried", () => {
    expect(buildStartRequestV3(startBase)).not.toHaveProperty("carried_audio_track_id");
  });

  it("binds an offered candidate on the start request", () => {
    expect(
      buildStartRequestV3({
        ...startBase,
        offeredVirtualURI: "virtual://movie/x?result=offered",
      }),
    ).toMatchObject({ offered_virtual_uri: "virtual://movie/x?result=offered" });
  });

  it("emits file_selection when the file was explicitly chosen", () => {
    expect(buildStartRequestV3({ ...startBase, fileSelection: "explicit" })).toMatchObject({
      file_selection: "explicit",
    });
  });

  it("omits file_selection when the file choice is server-owned", () => {
    expect(buildStartRequestV3(startBase)).not.toHaveProperty("file_selection");
  });

  it("includes the resolved subtitle track in the initial request", () => {
    expect(buildStartRequestV3({ ...startBase, subtitleTrackIndex: 0 })).toMatchObject({
      subtitle_track_index: 0,
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

  // The server ignores `metered`; a metered link only stops a measurement.
  it("sends no metered flag", () => {
    expect(buildStartRequestV3(startBase)).not.toHaveProperty("metered");
  });

  it("sends the quality preference verbatim for the server to normalize", () => {
    expect(buildStartRequestV3({ ...startBase, qualityPreference: "original" })).toMatchObject({
      quality_preference: "original",
    });
  });

  it("emits force_relink when forceRelink is true", () => {
    expect(buildStartRequestV3({ ...startBase, forceRelink: true })).toMatchObject({
      force_relink: true,
    });
  });

  it("omits force_relink when forceRelink is absent", () => {
    expect(buildStartRequestV3(startBase)).not.toHaveProperty("force_relink");
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
  // promise the server gates the invalidation command on — including the
  // reconciliation capability.
  it("re-advertises the same features a start negotiated", () => {
    expect(
      buildReplanRequestV3({
        ...replanBase,
        operation: "failure_recovery",
        extraClientFeatures: VIDEO_CLIENT_FEATURES_V3,
      }).client_features,
    ).toEqual([
      "playback_plan_v3",
      "plan_invalidated_v1",
      "marker_thumbnails_v1",
      "default_audio_reconcile_response_v1",
      "source_committed_event_v1",
      "inventory_updated_event_v1",
      "deferred_track_inventory_v1",
    ]);
  });

  // The correlation field asserts "this replan is the answer to that withdrawal",
  // so it travels verbatim and only when the caller has one to name.
  it("carries the answered invalidation reason when the replan has one", () => {
    const body = buildReplanRequestV3({
      ...replanBase,
      operation: "track_change",
      answersPlanInvalidation: "default_audio_reconciliation",
    });

    expect(body.answers_plan_invalidation).toBe("default_audio_reconciliation");
  });

  it("omits the answered invalidation reason from an ordinary replan", () => {
    expect(buildReplanRequestV3({ ...replanBase, operation: "quality_change" })).not.toHaveProperty(
      "answers_plan_invalidation",
    );
    expect(
      buildReplanRequestV3({ ...replanBase, operation: "failure_recovery" }),
    ).not.toHaveProperty("answers_plan_invalidation");
  });

  it("re-arms the server's auto fallback when the viewer selects Auto", () => {
    expect(
      buildReplanRequestV3({ ...replanBase, operation: "failure_recovery", autoFallback: true }),
    ).toMatchObject({ auto_fallback: true });
  });

  it("omits auto_fallback when the viewer's intent is unknown", () => {
    expect(
      buildReplanRequestV3({ ...replanBase, operation: "failure_recovery" }),
    ).not.toHaveProperty("auto_fallback");
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

  it("sends a measured estimate when there is one, and no metered flag", () => {
    const body = buildReplanRequestV3({ ...replanBase, operation: "quality_change" });
    expect(body).not.toHaveProperty("bandwidth_estimate_kbps");
    expect(body).not.toHaveProperty("metered");
    expect(
      buildReplanRequestV3({
        ...replanBase,
        operation: "quality_change",
        bandwidthEstimateKbps: 4_200,
      }),
    ).toMatchObject({ bandwidth_estimate_kbps: 4_200 });
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

describe("usePlaybackSession version selection", () => {
  function stubStartBodies() {
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        startBodies.push(body);
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              requested_media_file_id: (body.file_id as number) ?? 7,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    return startBodies;
  }

  it("tells the server to start the viewer's pick and leaves Auto disarmed", async () => {
    const startBodies = stubStartBodies();

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
          undefined,
          undefined,
          true,
        ),
      { wrapper },
    );

    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(startBodies[0]).toMatchObject({ file_id: 7, file_selection: "explicit" });
    // The version menu reads this: an explicit pick must not show Auto checked.
    expect(result.current.autoFallback).toBe(false);

    unmount();
  });

  it("keeps Auto checked and the selection server-owned for an Auto start", async () => {
    const startBodies = stubStartBodies();

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );

    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(startBodies[0]).toMatchObject({ file_id: 7, file_selection: "auto" });
    expect(result.current.autoFallback).toBe(true);

    unmount();
  });

  // An explicit pick takes away the server's licence to substitute, so a
  // refusal has to reach the viewer as a refusal: the error card is what
  // points them at the version list.
  it("surfaces a refused explicit start instead of substituting", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "terminal",
          session_id: "session-1",
          terminal: { reason: "no_alternate_version", message: "", retryable: true },
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
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
          undefined,
          undefined,
          true,
        ),
      { wrapper },
    );

    await waitFor(() => expect(result.current.error).toBeTruthy());
    expect(result.current.error).toContain("Try another version");
    // No silent second attempt: the refusal is the answer.
    expect(startBodies).toHaveLength(1);
    expect(result.current.errorReason).toBe("no_alternate_version");

    unmount();
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
    // Only the browser's rounded downlink hint is available, which is a guess:
    // no request may carry it as an estimate.
    Object.defineProperty(navigator, "connection", {
      configurable: true,
      value: { downlink: 4.5 },
    });
    onTestFinished(() => {
      delete (navigator as Navigator & { connection?: unknown }).connection;
    });

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "original"),
      { wrapper },
    );

    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.qualityPreference).toBe("original");

    act(() => result.current.changeQuality("720p", 120));
    await waitFor(() => {
      expect(result.current.replanning).toBe(false);
      expect(result.current.qualityPreference).toBe("original");
      expect(result.current.error).toBeTruthy();
    });

    act(() => {
      // Deliberately not awaited: these tests drive the replan manually so the
      // promise must not block `act` on a queued/in-flight replan.
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(replanCount).toBe(2));

    const replanBodies = fetchMock.mock.calls
      .filter(([url]) => String(url).endsWith("/playback/session-1/replan"))
      .map(([, init]) => JSON.parse(String(init?.body)) as { quality_preference: string });
    expect(replanBodies.map((body) => body.quality_preference)).toEqual(["720p", "original"]);

    const startBody = JSON.parse(
      String(
        fetchMock.mock.calls.find(([url]) => String(url).endsWith("/playback/start"))?.[1]?.body,
      ),
    ) as Record<string, unknown>;
    expect(startBody.quality_preference).toBe("original");
    for (const body of [startBody, ...replanBodies]) {
      expect(body).not.toHaveProperty("bandwidth_estimate_kbps");
      expect(body).not.toHaveProperty("metered");
    }

    unmount();
  });

  it("replans Auto with the lower estimate a stall step-down recorded", async () => {
    const plan = fixturePlanV3();
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
    const bodiesTo = (suffix: string) =>
      fetchMock.mock.calls
        .filter(([url]) => String(url).endsWith(suffix))
        .map(([, init]) => JSON.parse(String(init?.body)) as Record<string, unknown>);

    // The viewer's saved maximum is 8 Mbps.
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto", 8_000),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    // Nothing was measured yet, so Auto starts without an estimate.
    expect(bodiesTo("/playback/start")[0]).not.toHaveProperty("bandwidth_estimate_kbps");
    expect(bodiesTo("/playback/start")[0]?.bandwidth_cap_kbps).toBe(8_000);

    // What VideoPlayer does when Auto keeps stalling: record the lower
    // estimate, then replan Auto.
    recordObservedBandwidthLimitKbps(4_200);
    act(() => result.current.changeQuality("auto", 120));
    await waitFor(() => expect(bodiesTo("/playback/session-1/replan")).toHaveLength(1));
    expect(bodiesTo("/playback/session-1/replan")[0]).toMatchObject({
      operation: "quality_change",
      quality_preference: "auto",
      bandwidth_estimate_kbps: 4_200,
      bandwidth_cap_kbps: 8_000,
    });
    await waitFor(() => expect(result.current.replanning).toBe(false));

    // The cap stays the viewer's own saved maximum on a fixed quality too.
    act(() => result.current.changeQuality("720p", 130));
    await waitFor(() => expect(bodiesTo("/playback/session-1/replan")).toHaveLength(2));
    expect(bodiesTo("/playback/session-1/replan")[1]).toMatchObject({
      quality_preference: "720p",
      bandwidth_cap_kbps: 8_000,
    });

    unmount();
  });

  it("sets replanningQuality only for quality/output replans and resets it on adoption", async () => {
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
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: `plan:replan-${replanCount}`,
            plan_attempt_key: `v3:replan-${replanCount}`,
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.replanningQuality).toBe(false);

    // A quality change lights the quality flag while in flight.
    act(() => result.current.changeQuality("720p", 120));
    expect(result.current.replanning).toBe(true);
    expect(result.current.replanningQuality).toBe(true);
    await waitFor(() => expect(result.current.replanningQuality).toBe(false));

    // A track change does not.
    act(() => result.current.switchAudioTrack(1, 120));
    expect(result.current.replanning).toBe(true);
    expect(result.current.replanningQuality).toBe(false);
    await waitFor(() => expect(result.current.replanning).toBe(false));

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

    act(() => {
      // Deliberately not awaited: these tests drive the replan manually so the
      // promise must not block `act` on a queued/in-flight replan.
      void result.current.refreshSubtitles(120);
    });
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

    // The quality intent was queued behind the output refresh, and the audio
    // pick is held until that lane frees: it must be re-issued, not dropped.
    await waitFor(() => expect(replanBodies).toHaveLength(3));
    expect(replanBodies[1]).toMatchObject({
      operation: "quality_change",
      quality_preference: "1080p",
    });
    expect(replanBodies[2]).toMatchObject({ operation: "track_change" });
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

describe("usePlaybackSession replay revision", () => {
  it("bumps only when a new session starts at the beginning", async () => {
    let sessionCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { start_position?: number };
        sessionCount += 1;
        const position = body.start_position ?? 0;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: `session-${sessionCount}`,
            playback_plan: fixturePlanV3({
              session_id: `session-${sessionCount}`,
              timeline: { ...fixturePlanV3().timeline, player_start_seconds: position },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, rerender, unmount } = renderHook(
      ({
        requestKey,
        initialPosition,
        forceInitialPosition,
      }: {
        requestKey: string;
        initialPosition: number;
        forceInitialPosition: boolean;
      }) =>
        usePlaybackSession(requestKey, [], [], 7, initialPosition, forceInitialPosition, "auto"),
      {
        wrapper,
        initialProps: {
          requestKey: "episode-1",
          initialPosition: 120,
          forceInitialPosition: false,
        },
      },
    );
    await waitFor(() => expect(result.current.sessionId).toBe("session-1"));
    expect(result.current.replayRevision).toBe(0);

    // A resume at a non-zero position is a continuation, not a replay.
    rerender({ requestKey: "episode-2", initialPosition: 240, forceInitialPosition: false });
    await waitFor(() => expect(result.current.sessionId).toBe("session-2"));
    expect(result.current.replayRevision).toBe(0);

    // A new session at the beginning is a replay.
    rerender({ requestKey: "episode-3", initialPosition: 0, forceInitialPosition: true });
    await waitFor(() => expect(result.current.sessionId).toBe("session-3"));
    expect(result.current.replayRevision).toBe(1);

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

  it("sets pendingSwitchFileId optimistically and clears it when the new plan lands", async () => {
    const startBodies: Array<{ file_id: number }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: `session-${startBodies.length}`,
            playback_plan: fixturePlanV3({
              session_id: `session-${startBodies.length}`,
              plan_id: `plan:switch-${startBodies.length}`,
              requested_media_file_id: body.file_id,
              effective_media_file_id: body.file_id,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.pendingSwitchFileId).toBeNull();

    act(() => result.current.switchVersion(99, 0));
    // Set synchronously, before the replacement plan lands.
    expect(result.current.pendingSwitchFileId).toBe(99);
    expect(result.current.replacing).toBe(true);

    await waitFor(() => expect(result.current.mediaFileId).toBe(99));
    expect(result.current.pendingSwitchFileId).toBeNull();
    expect(result.current.replacing).toBe(false);
    expect(startBodies.map((body) => body.file_id)).toEqual([7, 99]);

    unmount();
  });

  it("carries the current audio track identity into a version-switch start", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        startBodies.push(body);
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: `session-${startBodies.length}`,
            playback_plan: fixturePlanV3({
              session_id: `session-${startBodies.length}`,
              plan_id: `plan:switch-${startBodies.length}`,
              requested_media_file_id: (body.file_id as number) ?? 7,
              effective_media_file_id: (body.file_id as number) ?? 7,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto", null, undefined, 2),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    // The initial start sends the mount-time audio ordinal.
    expect(startBodies[0]).toMatchObject({ file_id: 7, audio_track_index: 2 });

    act(() => result.current.switchVersion(99, 120));
    await waitFor(() => expect(startBodies).toHaveLength(2));
    const replacement = startBodies[1]!;
    expect(replacement.file_id).toBe(99);
    // The replacement start carries the current plan's file-bound audio identity
    // (remapped by family server-side) and drops the mount-time ordinal, which
    // is meaningless for the new file, in favor of the carried identity.
    expect(replacement.carried_audio_track_id).toBe("file:7:audio:0");
    expect(replacement).not.toHaveProperty("audio_track_index");

    unmount();
  });

  it("marks a version-switch start as an explicit file selection", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        startBodies.push(body);
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: `session-${startBodies.length}`,
            playback_plan: fixturePlanV3({
              session_id: `session-${startBodies.length}`,
              plan_id: `plan:switch-${startBodies.length}`,
              requested_media_file_id: (body.file_id as number) ?? 7,
              effective_media_file_id: (body.file_id as number) ?? 7,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    // The auto-selected initial start declares the server-owned choice.
    expect(startBodies[0]).toMatchObject({ file_id: 7, file_selection: "auto" });

    act(() => result.current.switchVersion(99, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));
    // A version switch is always an explicit user action.
    expect(startBodies[1]).toMatchObject({ file_id: 99, file_selection: "explicit" });

    unmount();
  });

  it("bumps transportRevision when a version switch collapses to the playing effective file", async () => {
    // A collapsed virtual row: the server resolves the newly requested catalog
    // row (99) to the same concrete candidate (7) the outgoing plan already
    // plays, so effective_media_file_id and stream.url are unchanged. The
    // requested identity still changed, which means the mounted element holds
    // the old session's stream; the transport revision must bump so the player
    // tears it down and re-attaches.
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        startBodies.push(body);
        const requested = (body.file_id as number) ?? 7;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: `session-${startBodies.length}`,
            playback_plan: fixturePlanV3({
              session_id: `session-${startBodies.length}`,
              plan_id: `plan:switch-${startBodies.length}`,
              requested_media_file_id: requested,
              // The collapsed row resolves to the already-playing candidate.
              effective_media_file_id: 7,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.transportRevision).toBe(1);

    act(() => result.current.switchVersion(99, 0));
    await waitFor(() => expect(result.current.plan?.requested_media_file_id).toBe(99));

    expect(result.current.planRevision).toBe(2);
    expect(result.current.mediaFileId).toBe(7);
    expect(result.current.transportRevision).toBe(2);

    unmount();
  });

  it("coalesces a rapid second version click to the latest target", async () => {
    const startBodies: Array<{ file_id: number; start_position?: number }> = [];
    let releaseFirstSwitch: ((response: Response) => void) | undefined;
    const firstSwitchResponse = new Promise<Response>((resolve) => {
      releaseFirstSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number; start_position?: number };
        startBodies.push(body);
        if (startBodies.length === 2) {
          // Hold the first switch open so the second click lands while it is
          // still in flight.
          return firstSwitchResponse;
        }
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: `session-${startBodies.length}`,
            playback_plan: fixturePlanV3({
              session_id: `session-${startBodies.length}`,
              plan_id: `plan:switch-${startBodies.length}`,
              requested_media_file_id: body.file_id,
              effective_media_file_id: body.file_id,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.switchVersion(99, 15));
    await waitFor(() => expect(startBodies).toHaveLength(2));
    // The second click lands while the first switch is still in flight.
    act(() => result.current.switchVersion(123, 347));
    expect(startBodies).toHaveLength(2);
    expect(result.current.pendingSwitchFileId).toBe(123);

    await act(async () => {
      releaseFirstSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:switch-2",
            requested_media_file_id: 99,
            effective_media_file_id: 99,
          }),
        }),
      );
      await firstSwitchResponse;
    });

    // The coalesced switch to the latest target runs immediately after.
    await waitFor(() => expect(startBodies).toHaveLength(3));
    expect(startBodies.map((body) => body.file_id)).toEqual([7, 99, 123]);
    // The first switch seeks to where its click was; the chained one must use
    // the live position at second-click time, not the first click's closure
    // position (15).
    expect(startBodies[1]?.start_position).toBe(15);
    expect(startBodies[2]?.start_position).toBe(347);
    await waitFor(() => expect(result.current.mediaFileId).toBe(123));
    expect(result.current.pendingSwitchFileId).toBeNull();

    unmount();
  });

  it("refuses replans issued while a version switch is in flight", async () => {
    const startBodies: Array<{ file_id: number }> = [];
    const replanBodies: Array<{ operation: string }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) {
          // Hold the switch open so replans land while it is rebuilding the
          // session.
          return switchResponse;
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
      if (url.endsWith("/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as { operation: string });
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:switch-2",
            plan_attempt_key: "v3:switch-2",
            requested_media_file_id: 99,
            effective_media_file_id: 99,
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
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
    expect(result.current.replacing).toBe(true);

    // A stale render, the realtime channel and the output-capability effect
    // can all try to replan while the switch is rebuilding the session. Each
    // would be built from the outgoing plan but stamped with the sequence the
    // switch just took, so its response could overwrite the replacement plan.
    // All must be refused.
    act(() => {
      result.current.switchAudioTrack(2, 120);
      result.current.changeQuality("1080p", 130);
      void result.current.reanchorSeek(140);
    });
    expect(replanBodies).toEqual([]);

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:switch-2",
            plan_attempt_key: "v3:switch-2",
            requested_media_file_id: 99,
            effective_media_file_id: 99,
            stream: { ...fixturePlanV3().stream, url: "/stream/session-2/master.m3u8" },
            source: { ...fixturePlanV3().source, media_file_id: 99 },
          }),
        }),
      );
      await switchResponse;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(99));
    expect(result.current.plan?.plan_id).toBe("plan:switch-2");
    // The refused seek replays against the settled replacement plan, and the
    // held audio pick is re-resolved against it instead of being discarded.
    // The quality intent is not a track pick and stays dropped.
    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(replanBodies[0]).toMatchObject({ operation: "seek_reanchor", position_seconds: 140 });
    expect(replanBodies[1]).toMatchObject({ operation: "track_change" });

    // Once the switch settles the fence lifts and replans flow again.
    act(() => {
      void result.current.reanchorSeek(150);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(3));

    unmount();
  });

  it("drops a mid-switch seek when its switch fails", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
    let startCount = 0;
    let releaseSwitch: ((response: Response) => void) | null = null;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startCount += 1;
        if (startCount === 1) {
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
        // Hold the version switch open so the seek lands while it is
        // demonstrably pending, then fail it on release.
        return new Promise<Response>((resolve) => {
          releaseSwitch = resolve;
        });
      }
      if (url.endsWith("/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3(),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void (async () => {
        await result.current.switchVersion(99, 0);
      })();
    });
    // The second start is in flight — the switch is demonstrably pending.
    await waitFor(() => expect(startCount).toBe(2));
    expect(result.current.replacing).toBe(true);
    // The seek lands while switchingRef owns the session, so it is
    // remembered rather than issued.
    act(() => {
      void result.current.reanchorSeek(140);
    });
    expect(replanBodies.filter((b) => b.operation === "seek_reanchor")).toHaveLength(0);
    // Fail the switch: the remembered seek must be discarded with it.
    await act(async () => {
      releaseSwitch!(
        jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "terminal",
            session_id: null,
            terminal: { reason: "source_unavailable", message: "gone", retryable: false },
          },
          { status: 200 },
        ),
      );
    });
    await waitFor(() => expect(result.current.error).not.toBeNull());
    // The failed switch retires its session and surfaces an error; the
    // remembered seek must not replan against whatever lands next.
    expect(replanBodies.filter((b) => b.operation === "seek_reanchor")).toHaveLength(0);
    unmount();
  });

  it("ignores a stale declared push in the same tick as a verified one", async () => {
    const verifiedTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const declaredTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "declared",
              audio_tracks: declaredTracks,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.audioInventoryProvisional).toBe(true);

    // Verified and declared pushes arriving in the same tick — before React
    // renders — must still leave the verified list in place.
    act(() => {
      result.current.applyCommittedSource(
        { effectiveMediaFileId: 7, effectiveVirtualUri: null, inventoryStatus: "verified" },
        verifiedTracks,
      );
      result.current.applyCommittedSource(
        { effectiveMediaFileId: 7, effectiveVirtualUri: null, inventoryStatus: "declared" },
        declaredTracks,
      );
    });

    expect(result.current.planAudioTracks).toEqual(verifiedTracks);
    expect(result.current.audioInventoryProvisional).toBe(false);
    unmount();
  });

  it("keeps declared inventory usable after a same-file candidate rotation", async () => {
    const candidateA = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const candidateB = [
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: true },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "verified",
              inventory_provenance: "verified",
              audio_tracks: candidateA,
              effective_virtual_uri: "virtual://movie/x?result=A",
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.audioInventoryProvisional).toBe(false);

    // The serve layer rotates to candidate B under the same file id. Verified
    // evidence for A must not suppress B's declared inventory.
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 7,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "declared",
        },
        candidateB,
      ),
    );
    expect(result.current.planAudioTracks).toEqual(candidateB);
    expect(result.current.audioInventoryProvisional).toBe(true);
    unmount();
  });

  it("stamps a verified subtitle anchor with the payload identity across rotation", async () => {
    const subtitleA: SubtitleInventoryItemV3 = {
      track_id: "file:7:subtitle:0",
      combined_index: 0,
      source: "embedded",
      codec: "subrip",
      language: "eng",
      forced: false,
      default: false,
      hearing_impaired: false,
      delivery: "sidecar",
      url: "/stream/session-1/subtitles/0.vtt?file_id=7",
    };
    const subtitleB = {
      ...subtitleA,
      track_id: "file:7:subtitle:9",
      combined_index: 9,
      language: "spa",
    };
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "declared",
              effective_virtual_uri: "virtual://movie/x?result=A",
              subtitle: { mode: "off", inventory: [] },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    // Rotation to B plus verified B inventory (audio + subtitle) in one
    // tick, then a stale declared B push in the same tick: the subtitle
    // anchor must carry B's URI so the stale push is suppressed, not applied.
    // A serve-layer rotation first arrives as a source commit, then its
    // verified inventory follows on the same socket.
    act(() => {
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 7,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "declared",
        },
        [],
      );
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:b-verified",
        inventory_status: "verified",
        effective_media_file_id: 7,
        effective_virtual_uri: "virtual://movie/x?result=B",
        audio_tracks: [
          { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: true },
        ],
        subtitle_inventory: [subtitleB],
      });
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:b-declared-stale",
        inventory_status: "declared",
        effective_media_file_id: 7,
        effective_virtual_uri: "virtual://movie/x?result=B",
        audio_tracks: [],
        subtitle_inventory: [subtitleA],
      });
    });

    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.subtitleUrls).toHaveLength(1);
    expect(result.current.subtitleUrls[0]?.language).toBe("spa");
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(false);
    unmount();
  });

  it("populates the track menus from an offered release without moving the transport", async () => {
    const offeredAudio = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const offeredSubtitle: SubtitleInventoryItemV3 = {
      track_id: "file:7:subtitle:4",
      combined_index: 4,
      source: "embedded",
      codec: "subrip",
      language: "eng",
      forced: false,
      default: false,
      hearing_impaired: false,
      delivery: "sidecar",
      url: "/stream/session-1/subtitles/4.vtt?file_id=7",
    };
    let replanCalls = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "declared",
              effective_virtual_uri: "virtual://movie/x?result=8376",
              audio_tracks: [],
              subtitle: { mode: "off", inventory: [] },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/replan")) {
        replanCalls += 1;
        throw new Error("an offered-release fold must not replan");
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    const transportBefore = result.current.transportRevision;
    const planRevisionBefore = result.current.planRevision;
    const sessionBefore = result.current.sessionId;
    expect(result.current.planAudioTracks).toEqual([]);

    // The pinned candidate vanished: the server offers the row's current
    // listing as display data keyed on the row, not on the playing candidate.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:offered-7398",
        inventory_status: "verified",
        effective_media_file_id: 7,
        offered_virtual_uri: "virtual://movie/x?result=7398",
        audio_tracks: offeredAudio,
        subtitle_inventory: [offeredSubtitle],
      }),
    );

    // The menus show what the row offers now...
    expect(result.current.planAudioTracks).toEqual(offeredAudio);
    expect(result.current.subtitleUrls).toHaveLength(1);
    expect(result.current.subtitleUrls[0]?.language).toBe("eng");
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    // ...while the transport, session and revisions never move.
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=8376");
    expect(result.current.mediaFileId).toBe(7);
    expect(result.current.transportRevision).toBe(transportBefore);
    expect(result.current.planRevision).toBe(planRevisionBefore);
    expect(result.current.sessionId).toBe(sessionBefore);
    expect(replanCalls).toBe(0);
    unmount();
  });

  it("starts the offered release with the picked audio identity", async () => {
    const offeredAudio = [
      {
        codec: "eac3",
        channels: 6,
        layout: "5.1",
        language: "eng",
        default: true,
        track_id: "file:7:audio:0",
      },
    ];
    const startBodies: Array<Record<string, unknown>> = [];
    let replanCalls = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        startBodies.push(body);
        const offeredStart = startBodies.length > 1;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: offeredStart ? "session-2" : "session-1",
            playback_plan: fixturePlanV3({
              session_id: offeredStart ? "session-2" : "session-1",
              inventory_status: "declared",
              effective_virtual_uri: offeredStart
                ? "virtual://movie/x?result=7398"
                : "virtual://movie/x?result=8376",
              audio_tracks: offeredStart ? offeredAudio : [],
              selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
              subtitle: { mode: "off", inventory: [] },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/replan")) {
        replanCalls += 1;
        throw new Error("an offered-release track pick must not send a generic recovery");
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:offered-7398",
        inventory_status: "verified",
        effective_media_file_id: 7,
        offered_virtual_uri: "virtual://movie/x?result=7398",
        audio_tracks: offeredAudio,
        subtitle_inventory: [],
      }),
    );
    act(() => result.current.switchAudioTrack(0, 42));

    await waitFor(() => expect(startBodies).toHaveLength(2));
    // The pick belongs to a release the transport is not playing. The start
    // names the row, asks for a fresh link, carries the position, and carries
    // the offered track identity — a generic recovery names none of those.
    expect(startBodies[1]).toMatchObject({
      file_id: 7,
      start_position: 42,
      file_selection: "auto",
      offered_virtual_uri: "virtual://movie/x?result=7398",
      force_relink: true,
      carried_audio_track_id: "file:7:audio:0",
    });
    expect(replanCalls).toBe(0);
    await waitFor(() =>
      expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=7398"),
    );
    unmount();
  });

  it("restores live inventory when a previously folded revision retires an offer", async () => {
    const liveAudio = [
      {
        codec: "aac",
        channels: 2,
        layout: "stereo",
        language: "eng",
        default: true,
        track_id: "file:7:audio:0",
      },
      {
        codec: "eac3",
        channels: 6,
        layout: "5.1",
        language: "fra",
        default: false,
        track_id: "file:7:audio:1",
      },
    ];
    const offeredAudio = [
      {
        codec: "eac3",
        channels: 6,
        layout: "5.1",
        language: "deu",
        default: true,
        track_id: "file:7:audio:0",
      },
    ];
    let startCalls = 0;
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          startCalls += 1;
          return jsonResponse(
            {
              protocol_version: 3,
              server_features: ["playback_plan_v3"],
              outcome: "playable",
              session_id: "session-1",
              playback_plan: fixturePlanV3({
                effective_virtual_uri: "virtual://movie/x?result=A",
                audio_tracks: liveAudio,
                selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
                inventory_status: "declared",
              }),
            },
            { status: 201 },
          );
        }
        if (url.endsWith("/playback/session-1/replan")) {
          replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              plan_id: "plan:live-recovered",
              plan_attempt_key: "v3:live-recovered",
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: liveAudio,
              selected_tracks: { audio: { id: "file:7:audio:1", index: 1 } },
            }),
          });
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    // Fold the live inventory first, then replace it with an offer. The later
    // packet repeats the already-folded live revision: retirement must still
    // restore the live tracks instead of clearing only the offer marker and
    // returning at the revision gate.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:live-A",
        inventory_status: "verified",
        effective_media_file_id: 7,
        effective_virtual_uri: "virtual://movie/x?result=A",
        audio_tracks: liveAudio,
        subtitle_inventory: [],
      }),
    );
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:offered-C",
        inventory_status: "verified",
        effective_media_file_id: 7,
        offered_virtual_uri: "virtual://movie/x?result=C",
        audio_tracks: offeredAudio,
        subtitle_inventory: [],
      }),
    );
    expect(result.current.planAudioTracks).toEqual(offeredAudio);

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:live-A",
        inventory_status: "verified",
        effective_media_file_id: 7,
        effective_virtual_uri: "virtual://movie/x?result=A",
        audio_tracks: liveAudio,
        subtitle_inventory: [],
      }),
    );
    expect(result.current.planAudioTracks).toEqual(liveAudio);
    act(() => result.current.switchAudioTrack(1, 10));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    expect(replanBodies[0]).toMatchObject({
      operation: "track_change",
      selected_tracks: { audio: { id: "file:7:audio:1", index: 1 } },
    });
    expect(startCalls).toBe(1);
    unmount();
  });

  it("carries an offered subtitle identity and expresses subtitles off on restart", async () => {
    const offeredAudio = [
      {
        codec: "eac3",
        channels: 6,
        layout: "5.1",
        language: "eng",
        default: true,
        track_id: "file:7:audio:0",
      },
    ];
    const offeredSubtitle = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:4",
      combined_index: 4,
      language: "eng",
      label: "English",
      url: "/stream/session-1/subtitles/4.vtt?file_id=7",
    });
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        startBodies.push(body);
        const session = `session-${startBodies.length}`;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: session,
            playback_plan: fixturePlanV3({
              session_id: session,
              inventory_status: "declared",
              effective_virtual_uri:
                startBodies.length === 1
                  ? "virtual://movie/x?result=8376"
                  : "virtual://movie/x?result=7398",
              audio_tracks: startBodies.length === 1 ? [] : offeredAudio,
              selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
              subtitle: { mode: "off", inventory: [] },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:offered-sub-1",
        inventory_status: "verified",
        effective_media_file_id: 7,
        offered_virtual_uri: "virtual://movie/x?result=7398",
        audio_tracks: offeredAudio,
        subtitle_inventory: [offeredSubtitle],
      }),
    );
    act(() => result.current.changeSubtitleTrack(4, 30));
    await waitFor(() => expect(startBodies).toHaveLength(2));
    expect(startBodies[1]).toMatchObject({
      file_id: 7,
      start_position: 30,
      file_selection: "auto",
      offered_virtual_uri: "virtual://movie/x?result=7398",
      force_relink: true,
      carried_audio_track_id: "file:7:audio:0",
      subtitle_track_index: 4,
      subtitle_track_id: "file:7:subtitle:4",
    });

    await waitFor(() => expect(result.current.sessionId).toBe("session-2"));
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-2",
        inventory_revision: "inv:offered-sub-2",
        inventory_status: "verified",
        effective_media_file_id: 7,
        offered_virtual_uri: "virtual://movie/x?result=9999",
        audio_tracks: offeredAudio,
        subtitle_inventory: [offeredSubtitle],
      }),
    );
    act(() => result.current.changeSubtitleTrack(null, 35));
    await waitFor(() => expect(startBodies).toHaveLength(3));
    expect(startBodies[2]).toMatchObject({
      file_id: 7,
      start_position: 35,
      offered_virtual_uri: "virtual://movie/x?result=9999",
      carried_audio_track_id: "file:7:audio:0",
    });
    expect(startBodies[2]).not.toHaveProperty("subtitle_track_index");
    expect(startBodies[2]).not.toHaveProperty("subtitle_track_id");
    unmount();
  });

  it("keeps the offered candidate through the bitmap-subtitle fallback", async () => {
    const offeredAudio = [
      {
        codec: "eac3",
        channels: 6,
        layout: "5.1",
        language: "eng",
        default: true,
        track_id: "file:7:audio:0",
      },
    ];
    const offeredBitmap = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:4",
      combined_index: 4,
      codec: "pgs",
      delivery: "burn_in_only",
      url: undefined,
    });
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        startBodies.push(body);
        if (startBodies.length === 1) {
          return jsonResponse(
            {
              protocol_version: 3,
              server_features: ["playback_plan_v3"],
              outcome: "playable",
              session_id: "session-1",
              playback_plan: fixturePlanV3({
                effective_virtual_uri: "virtual://movie/x?result=A",
                audio_tracks: offeredAudio,
                subtitle: { mode: "off", inventory: [] },
              }),
            },
            { status: 201 },
          );
        }
        if (startBodies.length === 2) {
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "adaptation_unavailable",
            terminal: {
              reason: "subtitle_conversion_unsupported",
              message: "The selected subtitle cannot be burned in.",
              retryable: false,
            },
          });
        }
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            effective_virtual_uri: "virtual://movie/x?result=7398",
            audio_tracks: offeredAudio,
            selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
            subtitle: { mode: "off", inventory: [] },
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:offered-bitmap",
        inventory_status: "verified",
        effective_media_file_id: 7,
        offered_virtual_uri: "virtual://movie/x?result=7398",
        audio_tracks: offeredAudio,
        subtitle_inventory: [offeredBitmap],
      }),
    );
    act(() => result.current.changeSubtitleTrack(4, 30));
    await waitFor(() => expect(startBodies).toHaveLength(3));
    expect(startBodies[1]).toMatchObject({
      file_id: 7,
      offered_virtual_uri: "virtual://movie/x?result=7398",
      subtitle_track_index: 4,
      subtitle_track_id: "file:7:subtitle:4",
    });
    // The subtitle-free retry still belongs to the offered candidate.
    expect(startBodies[2]).toMatchObject({
      file_id: 7,
      offered_virtual_uri: "virtual://movie/x?result=7398",
    });
    expect(startBodies[2]).not.toHaveProperty("subtitle_track_index");
    expect(startBodies[2]).not.toHaveProperty("subtitle_track_id");
    unmount();
  });

  it("does not run the bitmap fallback for an unavailable offered candidate", async () => {
    const offeredAudio = [
      {
        codec: "eac3",
        channels: 6,
        layout: "5.1",
        language: "eng",
        default: true,
        track_id: "file:7:audio:0",
      },
    ];
    const offeredBitmap = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:4",
      combined_index: 4,
      codec: "pgs",
      delivery: "burn_in_only",
      url: undefined,
    });
    let starts = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        starts += 1;
        if (starts === 1) {
          return jsonResponse(
            {
              protocol_version: 3,
              server_features: ["playback_plan_v3"],
              outcome: "playable",
              session_id: "session-1",
              playback_plan: fixturePlanV3({
                effective_virtual_uri: "virtual://movie/x?result=A",
                audio_tracks: offeredAudio,
                subtitle: { mode: "off", inventory: [] },
              }),
            },
            { status: 201 },
          );
        }
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "adaptation_unavailable",
          terminal: {
            reason: "offered_release_unavailable",
            message: "The offered release is no longer available.",
            retryable: true,
          },
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:offered-bitmap-unavailable",
        inventory_status: "verified",
        effective_media_file_id: 7,
        offered_virtual_uri: "virtual://movie/x?result=7398",
        audio_tracks: offeredAudio,
        subtitle_inventory: [offeredBitmap],
      }),
    );
    act(() => result.current.changeSubtitleTrack(4, 30));
    await waitFor(() => expect(starts).toBe(2));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(starts).toBe(2);
    unmount();
  });

  it("folds a mid-switch seek into a chained switch without a competing replan", async () => {
    const startBodies: Array<{ file_id: number; start_position?: number }> = [];
    const replanBodies: Array<Record<string, unknown>> = [];
    const releases: Array<(response: Response) => void> = [];
    const switchGate = (resolve: (response: Response) => void) => {
      releases.push(resolve);
    };
    let switchCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number; start_position?: number };
        startBodies.push(body);
        if (startBodies.length >= 2) {
          switchCount += 1;
          const gate = new Promise<Response>(switchGate);
          if (switchCount === 1) {
            // Hold the first switch open: queue a chained switch and seeks.
            return gate;
          }
          return gate;
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
      if (url.endsWith("/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:switch-99",
            plan_attempt_key: "v3:switch-99",
            requested_media_file_id: 99,
            effective_media_file_id: 99,
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    // First switch to 98 stays pending; queue a chained switch to 99 while a
    // seek lands mid-flight.
    act(() => result.current.switchVersion(98, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));
    act(() => {
      result.current.switchVersion(99, 300);
      void result.current.reanchorSeek(140);
      void result.current.reanchorSeek(150);
    });

    // Release the first switch: a fresh latest-wins target replaces the old
    // queued one, so the chained switch must start at the remembered 150.
    await act(async () => {
      releases[0]!(
        jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-2",
            playback_plan: fixturePlanV3({
              session_id: "session-2",
              plan_id: "plan:switch-98",
              plan_attempt_key: "v3:switch-98",
              requested_media_file_id: 98,
              effective_media_file_id: 98,
            }),
          },
          { status: 201 },
        ),
      );
    });
    await waitFor(() => expect(startBodies).toHaveLength(3));
    // The chained switch carries the latest remembered seek, not the older
    // queued switch position.
    expect(startBodies[2]).toMatchObject({ file_id: 99, start_position: 150 });
    // Settle the chained switch, then prove no competing replan raced it:
    // the fold replaces the replan, it does not precede one.
    await act(async () => {
      releases[1]!(
        jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-3",
            playback_plan: fixturePlanV3({
              session_id: "session-3",
              plan_id: "plan:switch-99",
              plan_attempt_key: "v3:switch-99",
              requested_media_file_id: 99,
              effective_media_file_id: 99,
            }),
          },
          { status: 201 },
        ),
      );
    });
    await waitFor(() => expect(result.current.mediaFileId).toBe(99));
    // The chained switch settled with replacing cleared; any racing replay
    // would have issued a replan by now.
    await waitFor(() => expect(result.current.replacing).toBe(false));
    expect(replanBodies).toHaveLength(0);
    unmount();
  });

  it("ignores a committed source rotation while a version switch is replacing", async () => {
    const planAudioTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) {
          // Hold the switch open so the rotation lands while it rebuilds.
          return switchResponse;
        }
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: planAudioTracks,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
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
    expect(result.current.replacing).toBe(true);

    // The outgoing transport rotates to another release while the switch is
    // still in flight. Its identity and inventory must not be written under the
    // pending switch.
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "declared",
        },
        [{ codec: "eac3", channels: 6, layout: "5.1", language: "deu", default: true }],
      ),
    );

    expect(result.current.mediaFileId).toBe(7);
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=A");
    expect(result.current.planAudioTracks).toEqual(planAudioTracks);

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:switch-2",
            plan_attempt_key: "v3:switch-2",
            requested_media_file_id: 99,
            effective_media_file_id: 99,
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    // The switch's replacement plan is the authority once it lands.
    await waitFor(() => expect(result.current.mediaFileId).toBe(99));
    expect(result.current.effectiveVirtualUri).toBeNull();
    expect(result.current.planAudioTracks).toEqual([]);

    unmount();
  });

  it("seeks a chained switch from the live playhead after a rotation", async () => {
    const startBodies: Array<{ file_id: number; start_position?: number }> = [];
    let releaseFirstSwitch: ((response: Response) => void) | undefined;
    const firstSwitchResponse = new Promise<Response>((resolve) => {
      releaseFirstSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number; start_position?: number };
        startBodies.push(body);
        if (startBodies.length === 2) return firstSwitchResponse;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: `session-${startBodies.length}`,
            playback_plan: fixturePlanV3({
              session_id: `session-${startBodies.length}`,
              plan_id: `plan:switch-${startBodies.length}`,
              requested_media_file_id: body.file_id,
              effective_media_file_id: body.file_id,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.switchVersion(99, 15));
    await waitFor(() => expect(startBodies).toHaveLength(2));
    // A second click queues a chained switch with the live position at click
    // time.
    act(() => result.current.switchVersion(123, 347));
    expect(startBodies).toHaveLength(2);

    // A rotation arrives while the first switch is in flight: the queued
    // position was captured against the outgoing timeline and must be dropped.
    act(() =>
      result.current.applyCommittedSource(
        { effectiveMediaFileId: 8, effectiveVirtualUri: null, inventoryStatus: "declared" },
        [],
      ),
    );

    await act(async () => {
      releaseFirstSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:switch-2",
            requested_media_file_id: 99,
            effective_media_file_id: 99,
            // The server starts the replacement stream at the live playhead.
            timeline: { ...fixturePlanV3().timeline, source_start_seconds: 500 },
          }),
        }),
      );
      await firstSwitchResponse;
    });

    await waitFor(() => expect(startBodies).toHaveLength(3));
    expect(startBodies.map((body) => body.file_id)).toEqual([7, 99, 123]);
    // The stale captured position (347) was discarded; the chained switch uses
    // the live playhead seeded by the replacement plan.
    expect(startBodies[2]?.start_position).toBe(500);

    unmount();
  });

  it("carries a mid-session Auto re-arm onto the next failure recovery", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
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
      if (url.endsWith("/playback/session-1/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            session_id: "session-1",
            plan_id: "plan:auto-replan-1",
            plan_attempt_key: "v3:autoreplan0001",
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    // Started on an explicit pick, so the session begins with Auto disarmed.
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
          undefined,
          undefined,
          true,
        ),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.autoFallback).toBe(false);

    // Re-arming Auto while a plan is live has no start request to carry the
    // intent, so the next replan must state it or a dead-source recovery stays
    // pinned to the explicit pick while the menu shows Auto.
    act(() => result.current.selectAutoVersion());
    expect(result.current.autoFallback).toBe(true);

    act(() => result.current.recoverFromFailure({ classification: "decoder_failure" }, 120));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    expect(replanBodies[0]).toMatchObject({
      operation: "failure_recovery",
      auto_fallback: true,
    });

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

    act(() => {
      // Deliberately not awaited: these tests drive the replan manually so the
      // promise must not block `act` on a queued/in-flight replan.
      void result.current.refreshSubtitles(120);
    });
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

  it("rejects deferred inventory for the outgoing candidate after a replan adopts another", async () => {
    const outgoingURI = "virtual://movie/x?result=outgoing";
    const incomingURI = "virtual://movie/x?result=incoming";
    const outgoingAudio = [
      { index: 0, language: "eng", codec: "aac", channels: 2, layout: "stereo", default: true },
    ];
    const incomingAudio = [
      {
        index: 0,
        language: "spa",
        codec: "eac3",
        channels: 6,
        layout: "5.1",
        default: true,
        track_id: "file:7:audio:0",
      },
      {
        index: 1,
        language: "eng",
        codec: "aac",
        channels: 2,
        layout: "stereo",
        default: false,
        track_id: "file:7:audio:1",
      },
    ];
    let releaseReplan!: (response: Response) => void;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          return jsonResponse(
            {
              protocol_version: 3,
              server_features: ["playback_plan_v3"],
              outcome: "playable",
              session_id: "session-1",
              playback_plan: fixturePlanV3({
                effective_virtual_uri: outgoingURI,
                audio_tracks: outgoingAudio,
                inventory_status: "declared",
              }),
            },
            { status: 201 },
          );
        }
        if (url.endsWith("/playback/session-1/replan")) {
          replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          return heldReplan;
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.effectiveVirtualUri).toBe(outgoingURI);

    // Hold the replacement plan open, then let the outgoing candidate's
    // verified inventory arrive while the client is still waiting for it.
    act(() => void result.current.refreshSubtitles(120));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:outgoing-verified",
        inventory_status: "verified",
        effective_media_file_id: 7,
        effective_virtual_uri: outgoingURI,
        audio_tracks: outgoingAudio,
        subtitle_inventory: [],
      }),
    );

    await act(async () => {
      releaseReplan(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:incoming-replacement",
            plan_attempt_key: "v3:incoming-replacement",
            effective_virtual_uri: incomingURI,
            audio_tracks: incomingAudio,
            selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
            inventory_status: "declared",
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:incoming-replacement"));
    expect(result.current.effectiveVirtualUri).toBe(incomingURI);
    expect(result.current.planAudioTracks).toEqual(incomingAudio);

    // The next pick must target the winning release, not the deferred
    // outgoing inventory that was queued before the replacement landed.
    act(() => result.current.switchAudioTrack(1, 45));
    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(replanBodies[1]).toMatchObject({
      operation: "track_change",
      selected_tracks: {
        audio: { id: "file:7:audio:1", index: 1 },
      },
    });

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

    act(() => {
      // Deliberately not awaited: these tests drive the replan manually so the
      // promise must not block `act` on a queued/in-flight replan.
      void result.current.refreshSubtitles(120);
    });
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

  it("re-issues an audio pick made during a seek reanchor against the new ordinals", async () => {
    const initialPlan = fixturePlanV3({
      audio_tracks: [
        {
          codec: "eac3",
          channels: 6,
          layout: "5.1",
          language: "eng",
          default: true,
          track_id: "file:7:audio:0",
          selection_index: 0,
        },
        {
          codec: "ac3",
          channels: 2,
          layout: "stereo",
          language: "spa",
          default: false,
          track_id: "file:7:audio:1",
          selection_index: 1,
        },
      ],
    });
    let resolveSeek: ((response: Response) => void) | undefined;
    const seekResponse = new Promise<Response>((resolve) => {
      resolveSeek = resolve;
    });
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
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
          replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          if (replanBodies.length === 1) return seekResponse;
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              plan_id: "plan:after-seek-0001",
              plan_attempt_key: "v3:after-seek-0001",
              audio_tracks: [
                {
                  codec: "ac3",
                  channels: 2,
                  layout: "stereo",
                  language: "spa",
                  default: true,
                  track_id: "file:7:audio:0",
                  selection_index: 0,
                },
                {
                  codec: "eac3",
                  channels: 6,
                  layout: "5.1",
                  language: "eng",
                  default: false,
                  track_id: "file:7:audio:1",
                  selection_index: 1,
                },
              ],
            }),
          });
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.reanchorSeek(400);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    // The viewer picks Spanish while the reanchor is in flight.
    act(() => result.current.switchAudioTrack(1, 400));
    expect(replanBodies).toHaveLength(1);

    await act(async () => {
      resolveSeek?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:after-seek-0001",
            plan_attempt_key: "v3:after-seek-0001",
            audio_tracks: [
              {
                codec: "ac3",
                channels: 2,
                layout: "stereo",
                language: "spa",
                default: true,
                track_id: "file:7:audio:0",
                selection_index: 0,
              },
              {
                codec: "eac3",
                channels: 6,
                layout: "5.1",
                language: "eng",
                default: false,
                track_id: "file:7:audio:1",
                selection_index: 1,
              },
            ],
          }),
        }),
      );
      await seekResponse;
    });

    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(replanBodies[1]).toMatchObject({
      operation: "track_change",
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });

    unmount();
  });

  it("re-issues a queued track change against the replacement plan", async () => {
    const initialPlan = fixturePlanV3();
    const replannedPlan = fixturePlanV3({
      plan_id: "plan:2222222222222222",
      plan_attempt_key: "v3:2222222222222222",
    });
    let resolveFirstReplan: ((response: Response) => void) | undefined;
    const firstReplanResponse = new Promise<Response>((resolve) => {
      resolveFirstReplan = resolve;
    });
    const replanBodies: Array<Record<string, unknown>> = [];
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
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return firstReplanResponse;
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      // In flight, and it will replace the plan the queued audio change is
      // built from.
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    // The pick is re-resolved against the replacement plan's inventory rather
    // than replayed with the outgoing plan's ordinal or silently dropped.
    act(() => result.current.switchAudioTrack(2, 130));
    expect(replanBodies).toHaveLength(1);

    await act(async () => {
      resolveFirstReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: replannedPlan,
        }),
      );
      await firstReplanResponse;
    });

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:2222222222222222"));
    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(replanBodies[1]).toMatchObject({ operation: "track_change" });

    unmount();
  });

  it("re-resolves a queued subtitle pick when the replacement inventory reuses its ordinal", async () => {
    const english = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:0",
      combined_index: 0,
      source: "external",
      codec: "srt",
      language: "eng",
      label: "English",
      url: "/stream/session-1/subtitles/0.vtt?file_id=7",
    });
    const french = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:1",
      combined_index: 1,
      source: "external",
      codec: "srt",
      language: "fra",
      label: "French",
      url: "/stream/session-1/subtitles/1.vtt?file_id=7",
    });
    const initialPlan = fixturePlanV3({
      subtitle: { mode: "off", inventory: [english, french] },
    });
    // The replacement reuses the ordinal-derived ids in the opposite slots:
    // file:7:subtitle:0 now names French, so trusting the captured id would
    // replay the click as the wrong language.
    const replannedPlan = fixturePlanV3({
      plan_id: "plan:subtitle-swap-01",
      plan_attempt_key: "v3:subtitle-swap-01",
      subtitle: {
        mode: "off",
        inventory: [
          { ...french, track_id: "file:7:subtitle:0", combined_index: 0 },
          { ...english, track_id: "file:7:subtitle:1", combined_index: 1 },
        ],
      },
    });
    const replanBodies: Array<Record<string, unknown>> = [];
    let resolveFirstReplan: ((response: Response) => void) | undefined;
    const firstReplanResponse = new Promise<Response>((resolve) => {
      resolveFirstReplan = resolve;
    });
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
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        if (replanBodies.length === 1) return firstReplanResponse;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: replannedPlan,
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.subtitleUrls.map((track) => track.language)).toEqual(["eng", "fra"]);

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    // The viewer clicks English while the replacement plan is in flight.
    act(() => result.current.changeSubtitleTrack(0, 130));
    expect(replanBodies).toHaveLength(1);

    await act(async () => {
      resolveFirstReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: replannedPlan,
        }),
      );
      await firstReplanResponse;
    });

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:subtitle-swap-01"));
    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(replanBodies[1]).toMatchObject({
      operation: "track_change",
      selected_tracks: { subtitle: { id: "file:7:subtitle:1", index: 1 } },
    });

    unmount();
  });

  it("keeps the clicked watch-detail fallback subtitle when the deferred inventory reorders its slots", async () => {
    const english = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:0",
      combined_index: 0,
      source: "external",
      codec: "srt",
      language: "eng",
      label: "English",
      url: "/stream/session-1/subtitles/0.vtt?file_id=7",
    });
    const french = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:1",
      combined_index: 1,
      source: "external",
      codec: "srt",
      language: "fra",
      label: "French",
      url: "/stream/session-1/subtitles/1.vtt?file_id=7",
    });
    // The probe is deferred, so the session publishes no inventory and the
    // menu is showing the watch detail's own rows.
    const initialPlan = fixturePlanV3({
      subtitle: { mode: "off", inventory: [] },
    });
    // The verified inventory arrives with the ordinals swapped: slot 0 is now
    // French, so the ordinal the click was made at names another language.
    const replannedPlan = fixturePlanV3({
      plan_id: "plan:fallback-subtitle-swap-01",
      plan_attempt_key: "v3:fallback-subtitle-swap-01",
      subtitle: {
        mode: "off",
        inventory: [
          { ...french, track_id: "file:7:subtitle:0", combined_index: 0 },
          { ...english, track_id: "file:7:subtitle:1", combined_index: 1 },
        ],
      },
    });
    const replanBodies: Array<Record<string, unknown>> = [];
    let resolveFirstReplan: ((response: Response) => void) | undefined;
    const firstReplanResponse = new Promise<Response>((resolve) => {
      resolveFirstReplan = resolve;
    });
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
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        if (replanBodies.length === 1) return firstReplanResponse;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: replannedPlan,
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.subtitleUrls).toEqual([]);

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    // The viewer clicks the English row the menu is showing. It has no session
    // track to look up, so the click has to carry the displayed row itself.
    const displayedEnglish: PlayerSubtitleInfo = {
      index: 0,
      media_file_id: 7,
      language: "eng",
      codec: "srt",
      label: "English",
      source: "external",
      url: "",
    };
    act(() => result.current.changeSubtitleTrack(0, 130, displayedEnglish));
    expect(replanBodies).toHaveLength(1);

    await act(async () => {
      resolveFirstReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: replannedPlan,
        }),
      );
      await firstReplanResponse;
    });

    await waitFor(() =>
      expect(result.current.plan?.plan_id).toBe("plan:fallback-subtitle-swap-01"),
    );
    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(replanBodies[1]).toMatchObject({
      operation: "track_change",
      selected_tracks: { subtitle: { id: "file:7:subtitle:1", index: 1 } },
    });

    unmount();
  });

  it("surfaces a queued pick the refreshed plan still cannot reconcile after a rotation", async () => {
    const initialPlan = fixturePlanV3({
      audio_tracks: [
        { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
        { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
      ],
    });
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const replanBodies: Array<Record<string, unknown>> = [];
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
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return heldReplan;
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    // Queue a plan-bound audio change (its ordinal was resolved off the plan's
    // menu), then let a serve-layer rotation move the effective file while the
    // replan is still in flight. The rotation observes the same adoption
    // barrier as an inventory push, so it is held rather than folded under the
    // pending replan.
    act(() => result.current.switchAudioTrack(1, 130));
    expect(replanBodies).toHaveLength(1);
    act(() =>
      result.current.applyCommittedSource(
        { effectiveMediaFileId: 8, effectiveVirtualUri: null, inventoryStatus: "declared" },
        [],
      ),
    );
    expect(result.current.mediaFileId).toBe(7);

    // The in-flight replan is refused without replacing the plan, so on settle
    // the held rotation folds (the plan was not replaced). The pick's ordinal
    // belongs to the outgoing file, so it asks for one plan refresh against the
    // rotated source; when that is refused too, it surfaces instead of
    // disappearing with a marker that still points at the old row.
    await act(async () => {
      releaseReplan?.(
        jsonResponse({
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
      await heldReplan;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    await waitFor(() => expect(replanBodies).toHaveLength(2));
    expect(replanBodies[1]).toMatchObject({
      operation: "track_change",
      selected_tracks: { audio: { id: "", index: 1 } },
    });
    expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef");

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
      // Every recovery prepares a fresh server generation even when the plan is
      // transport-identical (the A/V-byte heuristic cannot see regeneration),
      // so the transport revision must bump alongside the plan revision.
      expect(result.current.transportRevision).toBe(attempt + 2);
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

  it("subtitle track_change with a transport-identical plan does not bump transportRevision", async () => {
    const initialPlan = fixturePlanV3();
    const subtitlePlan = fixturePlanV3({
      plan_id: "plan:2222222222222222",
      plan_attempt_key: "v3:2222222222222222",
      subtitle: {
        mode: "render",
        track_id: "file:7:subtitle:0",
        inventory: [],
      },
    });
    const replanBodies: Array<Record<string, unknown>> = [];
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
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: subtitlePlan,
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
    expect(result.current.planRevision).toBe(1);
    expect(result.current.transportRevision).toBe(1);

    act(() => result.current.changeSubtitleTrack(0, 45));
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:2222222222222222"));
    expect(replanBodies[0]?.operation).toBe("track_change");
    expect(result.current.planRevision).toBe(2);
    // The replan only changed the subtitle artifact; the A/V transport bytes
    // are identical, so the player must keep the mounted element.
    expect(result.current.transportRevision).toBe(1);

    unmount();
  });
});

describe("usePlaybackSession held track picks", () => {
  const audioRows = [
    { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
    { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
  ];
  const identifiedAudioRows = [
    { ...audioRows[0]!, track_id: "file:7:audio:0", selection_index: 0 },
    { ...audioRows[1]!, track_id: "file:7:audio:1", selection_index: 1 },
  ];

  function playablePlan(plan: ReturnType<typeof fixturePlanV3>, sessionId = "session-1") {
    return jsonResponse(
      {
        protocol_version: 3,
        server_features: ["playback_plan_v3"],
        outcome: "playable",
        session_id: sessionId,
        playback_plan: plan,
      },
      { status: 201 },
    );
  }

  function refusedReplan() {
    return jsonResponse({
      protocol_version: 3,
      server_features: ["playback_plan_v3"],
      outcome: "adaptation_unavailable",
      session_id: "session-1",
      terminal: {
        reason: "video_conversion_unsupported",
        message: "No executor can transcode this source.",
        retryable: false,
      },
    });
  }

  it("drops a pick held for the previous request when the viewer opens another title", async () => {
    const firstPlan = fixturePlanV3({
      audio_tracks: identifiedAudioRows,
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });
    const secondPlan = fixturePlanV3({
      plan_id: "plan:secondrequest00001",
      plan_attempt_key: "v3:secondrequest00001",
      session_id: "session-2",
      audio_tracks: identifiedAudioRows,
      selected_tracks: { audio: { id: "file:8:audio:0", index: 0 } },
    });
    let releaseRefresh: ((response: Response) => void) | undefined;
    const heldRefresh = new Promise<Response>((resolve) => {
      releaseRefresh = resolve;
    });
    const replanUrls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          const body = JSON.parse(String(init?.body)) as { file_id: number };
          return body.file_id === 8
            ? playablePlan(secondPlan, "session-2")
            : playablePlan(firstPlan);
        }
        if (url.endsWith("/replan")) {
          replanUrls.push(url);
          return heldRefresh;
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, rerender, unmount } = renderHook(
      ({ requestKey, fileId }: { requestKey: string; fileId: number }) =>
        usePlaybackSession(requestKey, [], [], fileId, 0, false, "auto"),
      { wrapper, initialProps: { requestKey: "request-1", fileId: 7 } },
    );
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-1"));

    // A replan owns the lane, so the audio pick is held rather than sent.
    act(() => void result.current.refreshSubtitles(100));
    await waitFor(() => expect(replanUrls).toHaveLength(1));
    act(() => result.current.switchAudioTrack(1, 110));

    // The viewer opens another title before the held replan settles.
    rerender({ requestKey: "request-2", fileId: 8 });
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-2"));

    await act(async () => {
      releaseRefresh?.(refusedReplan());
      await heldRefresh;
    });

    // The pick belonged to request-1's session; replaying it against the new
    // session would switch a track the viewer never picked for this title.
    expect(replanUrls.some((url) => url.includes("session-2"))).toBe(false);
    expect(result.current.trackSelectionNotice).toBeNull();
    expect(result.current.audioTrackIndex).toBe(0);

    unmount();
  });

  it("refreshes a provisional plan once before applying a same-source pick", async () => {
    const declaredPlan = fixturePlanV3({
      inventory_status: "declared",
      inventory_provenance: "declared",
      audio_tracks: identifiedAudioRows,
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });
    const probedFrench = {
      codec: "aac",
      channels: 2,
      layout: "stereo",
      language: "fra",
      default: false,
    };
    const frenchRow = {
      ...probedFrench,
      track_id: "file:7:audio:2",
      selection_index: 2,
    };
    const verifiedPlan = fixturePlanV3({
      plan_id: "plan:probe000000000001",
      plan_attempt_key: "v3:probe000000000001",
      inventory_status: "verified",
      inventory_provenance: "verified",
      audio_tracks: [...identifiedAudioRows, frenchRow],
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });
    const appliedPlan = fixturePlanV3({
      plan_id: "plan:probe000000000002",
      plan_attempt_key: "v3:probe000000000002",
      inventory_status: "verified",
      inventory_provenance: "verified",
      audio_tracks: [...identifiedAudioRows, frenchRow],
      selected_tracks: { audio: { id: "file:7:audio:2", index: 2 } },
    });
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) return playablePlan(declaredPlan);
        if (url.endsWith("/playback/session-1/replan")) {
          replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: replanBodies.length === 1 ? verifiedPlan : appliedPlan,
          });
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe(declaredPlan.plan_id));
    // The probe has published catalog rows the declared plan does not name.
    act(() => result.current.applyAudioInventory([audioRows[0]!, audioRows[1]!, probedFrench], 7));
    expect(result.current.planAudioTracks).toHaveLength(3);

    act(() => result.current.switchAudioTrack(2, 100));

    await waitFor(() => expect(replanBodies).toHaveLength(2));
    // The first replan is the bounded refresh: it carries the current
    // selection, not the clicked row the declared plan cannot name.
    expect(replanBodies[0]).toMatchObject({
      operation: "track_change",
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });
    // The verified inventory landed, so the second replan applies the pick.
    expect(replanBodies[1]).toMatchObject({
      operation: "track_change",
      selected_tracks: { audio: { id: "file:7:audio:2", index: 2 } },
    });
    expect(result.current.trackSelectionNotice).toBeNull();
    expect(result.current.audioTrackIndex).toBe(2);

    unmount();
  });

  it("clears the track notice when the request changes", async () => {
    const firstPlan = fixturePlanV3({
      audio_tracks: identifiedAudioRows,
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });
    const secondPlan = fixturePlanV3({
      plan_id: "plan:secondnotice000001",
      plan_attempt_key: "v3:secondnotice000001",
      session_id: "session-2",
      audio_tracks: identifiedAudioRows,
      selected_tracks: { audio: { id: "file:8:audio:0", index: 0 } },
    });
    let releaseStart: ((response: Response) => void) | undefined;
    const heldStart = new Promise<Response>((resolve) => {
      releaseStart = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          const body = JSON.parse(String(init?.body)) as { file_id: number };
          return body.file_id === 8 ? heldStart : playablePlan(firstPlan);
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, rerender, unmount } = renderHook(
      ({ requestKey, fileId }: { requestKey: string; fileId: number }) =>
        usePlaybackSession(requestKey, [], [], fileId, 0, false, "auto"),
      { wrapper, initialProps: { requestKey: "request-1", fileId: 7 } },
    );
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-1"));

    act(() => result.current.switchAudioTrack(5, 30));
    expect(result.current.trackSelectionNotice?.message).toContain("audio track");

    // The notice belongs to the outgoing request; it must clear immediately
    // rather than waiting for the replacement plan to adopt.
    rerender({ requestKey: "request-2", fileId: 8 });
    await waitFor(() => expect(result.current.trackSelectionNotice).toBeNull());

    await act(async () => {
      releaseStart?.(playablePlan(secondPlan, "session-2"));
      await heldStart;
    });
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-2"));
    expect(result.current.trackSelectionNotice).toBeNull();

    unmount();
  });

  it("clears the track notice when a later track change is adopted", async () => {
    const firstPlan = fixturePlanV3({
      audio_tracks: identifiedAudioRows,
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });
    const adoptedPlan = fixturePlanV3({
      plan_id: "plan:adoptednotice00001",
      plan_attempt_key: "v3:adoptednotice00001",
      audio_tracks: identifiedAudioRows,
      selected_tracks: { audio: { id: "file:7:audio:1", index: 1 } },
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) return playablePlan(firstPlan);
        if (url.endsWith("/playback/session-1/replan")) {
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: adoptedPlan,
          });
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-1"));

    act(() => result.current.switchAudioTrack(5, 30));
    expect(result.current.trackSelectionNotice).not.toBeNull();

    act(() => result.current.switchAudioTrack(1, 40));
    await waitFor(() => expect(result.current.plan?.plan_id).toBe(adoptedPlan.plan_id));
    expect(result.current.trackSelectionNotice).toBeNull();

    unmount();

    // A remount starts from a clean session and does not replay the notice.
    const remounted = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(remounted.result.current.plan?.session_id).toBe("session-1"));
    expect(remounted.result.current.trackSelectionNotice).toBeNull();
    remounted.unmount();
  });

  it("surfaces a queued pick after the one refresh instead of refreshing on a second source move", async () => {
    const probedFrench = {
      codec: "aac",
      channels: 2,
      layout: "stereo",
      language: "fra",
      default: false,
    };
    const initialPlan = fixturePlanV3({
      inventory_status: "verified",
      inventory_provenance: "verified",
      audio_tracks: identifiedAudioRows,
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });
    const replanBodies: Array<Record<string, unknown>> = [];
    const deferreds: Array<{ promise: Promise<Response>; resolve: (response: Response) => void }> =
      [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) return playablePlan(initialPlan);
        if (url.endsWith("/playback/session-1/replan")) {
          replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          let resolve!: (response: Response) => void;
          const promise = new Promise<Response>((done) => {
            resolve = done;
          });
          deferreds.push({ promise, resolve });
          return promise;
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-1"));
    // The probed menu carries a row the verified plan does not name, so the
    // click is held rather than resolvable.
    act(() => result.current.applyAudioInventory([audioRows[0]!, audioRows[1]!, probedFrench], 7));

    act(() => void result.current.refreshSubtitles(100));
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    act(() => result.current.switchAudioTrack(2, 130));
    act(() =>
      result.current.applyCommittedSource(
        { effectiveMediaFileId: 8, effectiveVirtualUri: null, inventoryStatus: "declared" },
        [],
      ),
    );

    // The refusal does not replace the plan, so the rotation folds and the
    // held pick asks for its one refresh.
    await act(async () => {
      deferreds[0]?.resolve(refusedReplan());
      await deferreds[0]?.promise;
    });
    await waitFor(() => expect(replanBodies).toHaveLength(2));
    await waitFor(() => expect(result.current.mediaFileId).toBe(8));

    // A second rotation arrives while that refresh is in flight.
    act(() =>
      result.current.applyCommittedSource(
        { effectiveMediaFileId: 9, effectiveVirtualUri: null, inventoryStatus: "declared" },
        [],
      ),
    );
    await act(async () => {
      deferreds[1]?.resolve(refusedReplan());
      await deferreds[1]?.promise;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(9));
    // The refresh budget is spent: the pick surfaces instead of replanning
    // against another stale ordinal.
    await waitFor(() =>
      expect(result.current.trackSelectionNotice?.message).toContain("audio track"),
    );
    expect(replanBodies).toHaveLength(2);

    unmount();
  });

  it("drops a queued subtitle pick whose inventory has no unambiguous counterpart", async () => {
    const englishFallback: PlayerSubtitleInfo = {
      index: 0,
      media_file_id: 7,
      track_id: "file:7:subtitle:0",
      language: "eng",
      codec: "srt",
      label: "English",
      source: "external",
      url: "",
    };
    const twin = (combinedIndex: number) =>
      fixtureSubtitleInventoryItemV3({
        track_id: `file:7:subtitle:${combinedIndex}`,
        combined_index: combinedIndex,
        source: "external",
        codec: "srt",
        language: "eng",
        label: "English",
        url: `/stream/session-1/subtitles/${combinedIndex}.vtt?file_id=7`,
      });
    const initialPlan = fixturePlanV3({ subtitle: { mode: "off", inventory: [] } });
    const twinPlan = fixturePlanV3({
      plan_id: "plan:subtwins0000000001",
      plan_attempt_key: "v3:subtwins0000000001",
      subtitle: { mode: "off", inventory: [twin(0), twin(1)] },
    });
    const replanBodies: Array<Record<string, unknown>> = [];
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) return playablePlan(initialPlan);
        if (url.endsWith("/playback/session-1/replan")) {
          replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          if (replanBodies.length === 1) return heldReplan;
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: twinPlan,
          });
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-1"));

    act(() => void result.current.refreshSubtitles(100));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    // The click is made against the deferred inventory while the replacement
    // plan is still in flight, so it travels with the clicked fallback row.
    act(() => result.current.changeSubtitleTrack(0, 130, englishFallback));

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: twinPlan,
        }),
      );
      await heldReplan;
    });

    // The replacement inventory names two distinct tracks with the same
    // descriptor, so the captured row cannot be reconciled without guessing.
    await waitFor(() => expect(result.current.plan?.plan_id).toBe(twinPlan.plan_id));
    await waitFor(() =>
      expect(result.current.trackSelectionNotice?.message).toContain("subtitle track"),
    );
    expect(replanBodies).toHaveLength(1);

    unmount();
  });

  it("treats a selection_unavailable subtitle answer as terminal for that pick", async () => {
    const english = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:0",
      combined_index: 0,
      source: "external",
      codec: "srt",
      language: "eng",
      label: "English",
      url: "/stream/session-1/subtitles/0.vtt?file_id=7",
    });
    const french = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:1",
      combined_index: 1,
      source: "external",
      codec: "srt",
      language: "fra",
      label: "French",
      url: "/stream/session-1/subtitles/1.vtt?file_id=7",
    });
    const initialPlan = fixturePlanV3({
      subtitle: { mode: "off", inventory: [english, french] },
    });
    const rejectedPlan = fixturePlanV3({
      plan_id: "plan:subreject000000001",
      plan_attempt_key: "v3:subreject000000001",
      subtitle: { mode: "off", inventory: [english, french] },
      claims: {
        video: { hdr10: false, hdr10_plus: false, hlg: false, dolby_vision: false },
        audio: { codec: "aac", passthrough: false, atmos_preserved: false },
        subtitles: {
          ass_styling_preserved: false,
          bitmap_overlay: false,
          bitmap_sidecar: false,
          reason: "selection_unavailable",
        },
      },
      degradation_warnings: [
        { code: "subtitle_track_unavailable", message: "The selected subtitle is unavailable." },
      ],
    });
    const replanBodies: Array<Record<string, unknown>> = [];
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
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: rejectedPlan,
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-1"));

    act(() => result.current.changeSubtitleTrack(1, 45));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    // The answer is a rejection, not a successful subtitles-off change.
    await waitFor(() =>
      expect(result.current.trackSelectionNotice?.message).toContain("subtitle track"),
    );

    // Replaying the same pick against the same inventory is terminal: the
    // client stops instead of sending the request the server already answered.
    act(() => result.current.changeSubtitleTrack(1, 45));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    expect(replanBodies).toHaveLength(1);

    unmount();
  });

  it("holds a subtitle pick whose ordinal space the plan has not established", async () => {
    const englishFallback: PlayerSubtitleInfo = {
      index: 0,
      media_file_id: 7,
      track_id: "file:7:subtitle:0",
      language: "eng",
      codec: "srt",
      label: "English",
      source: "external",
      url: "",
    };
    const initialPlan = fixturePlanV3({
      subtitle: { mode: "off", inventory: [] },
      tracks_pending: true,
    });
    const replanBodies: Array<Record<string, unknown>> = [];
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
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: initialPlan,
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-1"));

    act(() => result.current.changeSubtitleTrack(0, 45, englishFallback));
    await waitFor(() =>
      expect(result.current.trackSelectionNotice?.message).toContain("subtitle track"),
    );
    // The one permitted refresh replan carries no subtitle identity: the plan
    // published no inventory, so a bare catalog ordinal would address a slot
    // this file may not have.
    for (const body of replanBodies) {
      expect(body).not.toHaveProperty("subtitle_track_index");
      expect(body).not.toHaveProperty("subtitle_track_id");
    }

    unmount();
  });

  it("replays a held pick without a captured row at its exact ordinal", async () => {
    const initialPlan = fixturePlanV3({
      audio_tracks: [audioRows[0]!],
      selected_tracks: { audio: { id: "", index: 0 } },
    });
    const replacementPlan = fixturePlanV3({
      plan_id: "plan:idlessordinal00001",
      plan_attempt_key: "v3:idlessordinal00001",
      audio_tracks: audioRows,
      selected_tracks: { audio: { id: "", index: 0 } },
    });
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) return playablePlan(initialPlan);
        if (url.endsWith("/playback/session-1/replan")) {
          replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: replacementPlan,
          });
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-1"));

    act(() => void result.current.refreshSubtitles(100));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    // The menu has one row, so the click at index 1 carries no captured row.
    act(() => result.current.switchAudioTrack(1, 130));

    await waitFor(() => expect(replanBodies).toHaveLength(2));
    // An id-less plan has only the ordinal channel; the held click's exact
    // slot is the fallback instead of a drop.
    expect(replanBodies[1]).toMatchObject({
      operation: "track_change",
      selected_tracks: { audio: { id: "", index: 1 } },
    });
    expect(result.current.trackSelectionNotice).toBeNull();

    unmount();
  });

  it("sends an empty-plan audio pick without spending the refresh budget", async () => {
    const initialPlan = fixturePlanV3({
      audio_tracks: [],
      inventory_status: "declared",
      inventory_provenance: "declared",
      tracks_pending: true,
      selected_tracks: { audio: { id: "", index: 0 } },
    });
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) return playablePlan(initialPlan);
        if (url.endsWith("/playback/session-1/replan")) {
          replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({ audio_tracks: audioRows }),
          });
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-1"));
    act(() => result.current.applyAudioInventory(audioRows, 7));

    act(() => result.current.switchAudioTrack(1, 90));

    await waitFor(() => expect(replanBodies).toHaveLength(1));
    // The plan publishes no inventory, so the ordinal is the only channel the
    // server has; the pick goes straight out rather than refreshing first.
    expect(replanBodies[0]).toMatchObject({
      operation: "track_change",
      selected_tracks: { audio: { id: "", index: 1 } },
    });
    expect(result.current.trackSelectionNotice).toBeNull();

    unmount();
  });
});

describe("usePlaybackSession dead-session recovery", () => {
  // A plan the viewer has selections on, so the rebuild can be checked to carry
  // them rather than falling back to server defaults.
  const selectedPlan = (sessionId: string, mediaFileId = 7) =>
    fixturePlanV3({
      session_id: sessionId,
      requested_media_file_id: mediaFileId,
      effective_media_file_id: mediaFileId,
      selected_tracks: {
        audio: { id: "file:7:audio:1", index: 1 },
        subtitle: { id: "file:7:subtitle:2", index: 2 },
      },
    });

  it("rebuilds a reaped session once at the live position and selections", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        const sessionId = startBodies.length === 1 ? "session-1" : "session-2";
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: sessionId,
            playback_plan: selectedPlan(sessionId),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) {
        return jsonResponse(
          { error: "playback_session_not_found", message: "Playback session not found" },
          { status: 404 },
        );
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

    // The viewer pauses at 321s, then the server reaps the session under them.
    act(() => {
      result.current.updatePlaybackState(300, true);
      result.current.updatePlaybackState(321, false);
    });

    await act(async () => {
      await result.current.reanchorSeek(321);
    });

    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-2"));
    expect(startBodies).toHaveLength(2);
    const rebuild = startBodies[1]!;
    expect(rebuild.file_id).toBe(7);
    expect(rebuild.start_position).toBe(321);
    expect(rebuild.carried_audio_track_id).toBe("file:7:audio:1");
    expect(rebuild.subtitle_track_index).toBe(2);
    // The dead plan no longer owns the player, and no error dead-ends it.
    expect(result.current.error).toBeNull();
    // Play/pause state survives the rebuild.
    expect(result.current.shouldAutoPlay).toBe(false);

    // One reaped session gets exactly one rebuild: flushing further turns must
    // not produce a third start.
    await act(async () => {
      await Promise.resolve();
    });
    expect(startBodies).toHaveLength(2);

    unmount();
  });

  it("rebuilds a reaped version-switch session with its explicit selection", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        startBodies.push(body);
        const sessionId = `session-${startBodies.length}`;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: sessionId,
            playback_plan: selectedPlan(sessionId, (body.file_id as number) ?? 7),
          },
          { status: 201 },
        );
      }
      if (url.includes("/replan")) {
        return jsonResponse(
          { error: "playback_session_not_found", message: "Playback session not found" },
          { status: 404 },
        );
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
    // The auto-selected initial start declares the server-owned choice.
    expect(startBodies[0]).toMatchObject({ file_id: 7, file_selection: "auto" });

    // A version switch starts explicit; the viewer picked version B.
    act(() => result.current.switchVersion(99, 120));
    await waitFor(() => expect(result.current.plan?.requested_media_file_id).toBe(99));
    expect(startBodies[1]).toMatchObject({ file_id: 99, file_selection: "explicit" });

    // B is reaped under the viewer. The rebuild must carry the switch's
    // explicit selection, not revert to the mount-time auto choice; otherwise
    // the server may silently substitute another edition for B.
    await act(async () => {
      await result.current.reanchorSeek(150);
    });
    await waitFor(() => expect(startBodies).toHaveLength(3));

    const rebuild = startBodies[2]!;
    expect(rebuild.file_id).toBe(99);
    expect(rebuild.file_selection).toBe("explicit");
    expect(result.current.error).toBeNull();

    unmount();
  });

  it("does not rebuild the same dead session twice", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        // The replacement start re-mints the same session id, so the second
        // refusal names the session already rebuilt.
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
      if (url.endsWith("/playback/session-1/replan")) {
        return jsonResponse(
          { error: "playback_session_not_found", message: "Playback session not found" },
          { status: 404 },
        );
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

    await act(async () => {
      await result.current.reanchorSeek(300);
    });
    await waitFor(() => expect(startBodies).toHaveLength(2));

    // A second refusal for the session already rebuilt surfaces its copy
    // instead of starting a third time.
    await act(async () => {
      await result.current.reanchorSeek(600);
    });
    await waitFor(() =>
      expect(result.current.error).toBe(
        "This playback session is no longer active. Start it again to keep watching.",
      ),
    );
    expect(startBodies).toHaveLength(2);

    unmount();
  });
});

describe("usePlaybackSession seek reanchor evidence recovery", () => {
  const SEEK_EVIDENCE_MESSAGE =
    "The frozen playback source could not be reopened; start a new playback attempt.";

  // A plan the viewer has selections on, so the rebuild can be checked to carry
  // them rather than falling back to server defaults.
  const selectedPlan = (sessionId: string) =>
    fixturePlanV3({
      session_id: sessionId,
      stream: {
        url: `/stream/${sessionId}/master.m3u8`,
        protocol: "hls",
        mime_type: "application/vnd.apple.mpegurl",
        headers: {},
        header_refresh: "none",
      },
      requested_media_file_id: 7,
      effective_media_file_id: 7,
      effective_virtual_uri: "virtual://movie/x?result=8376",
      selected_tracks: {
        audio: { id: "file:7:audio:1", index: 1 },
        subtitle: { id: "file:7:subtitle:2", index: 2 },
      },
    });

  const seekEvidenceTerminal = (sessionId: string) => ({
    protocol_version: 3,
    server_features: ["playback_plan_v3"],
    outcome: "adaptation_unavailable",
    session_id: sessionId,
    terminal: {
      reason: "seek_reanchor_evidence_unavailable",
      message: SEEK_EVIDENCE_MESSAGE,
      retryable: true,
    },
  });

  // Records the start bodies and answers every start with the next session in
  // the chain, so a rebuild is visible as a second start.
  function startChainMock(
    startBodies: Array<Record<string, unknown>>,
    replan: (sessionId: string, body: Record<string, unknown>) => unknown,
  ) {
    return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        const sessionId = `session-${startBodies.length}`;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: sessionId,
            playback_plan: selectedPlan(sessionId),
          },
          { status: 201 },
        );
      }
      const replanMatch = /\/playback\/([^/]+)\/replan$/.exec(url);
      if (replanMatch) {
        const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
        return jsonResponse(replan(replanMatch[1]!, body));
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
      throw new Error(`Unexpected request: ${url}`);
    });
  }

  it("restarts once at the seek position with the plan's tracks and offered release", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      startChainMock(startBodies, (sessionId, body) => {
        replanBodies.push(body);
        return seekEvidenceTerminal(sessionId);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.plan?.session_id).toBe("session-1");

    // The row's pinned candidate vanished and the server offers its current
    // listing as display data; a rebuild must keep naming that candidate.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:offered-7398",
        inventory_status: "verified",
        effective_media_file_id: 7,
        offered_virtual_uri: "virtual://movie/x?result=7398",
        audio_tracks: [
          {
            codec: "eac3",
            channels: 6,
            layout: "5.1",
            language: "eng",
            default: true,
            track_id: "file:7:audio:1",
          },
        ],
        subtitle_inventory: [],
      }),
    );

    let accepted = false;
    await act(async () => {
      accepted = await result.current.reanchorSeek(1234);
    });

    // The recovery start is the answer to the seek, not a dead end.
    expect(accepted).toBe(true);
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-2"));

    expect(replanBodies).toHaveLength(1);
    expect(replanBodies[0]).toMatchObject({ operation: "seek_reanchor" });

    expect(startBodies).toHaveLength(2);
    expect(startBodies[1]).toMatchObject({
      file_id: 7,
      file_selection: "auto",
      start_position: 1234,
      carried_audio_track_id: "file:7:audio:1",
      subtitle_track_index: 2,
      offered_virtual_uri: "virtual://movie/x?result=7398",
    });
    // A fresh start re-resolves the source; it is not a relink retry.
    expect(startBodies[1]?.force_relink).toBeUndefined();
    expect(result.current.error).toBeNull();

    unmount();
  });

  it("does not restart for another seek terminal reason", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      startChainMock(startBodies, (sessionId) => ({
        protocol_version: 3,
        server_features: ["playback_plan_v3"],
        outcome: "terminal",
        session_id: sessionId,
        terminal: {
          reason: "session_expired",
          message: "This playback session is no longer active.",
          retryable: false,
        },
      })),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    await act(async () => {
      expect(await result.current.reanchorSeek(1234)).toBe(false);
    });

    await waitFor(() => expect(result.current.error).toBeTruthy());
    expect(result.current.errorReason).toBe("session_expired");
    expect(startBodies).toHaveLength(1);

    unmount();
  });

  it("does not restart for the seek terminal on another operation", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      startChainMock(startBodies, (sessionId) => seekEvidenceTerminal(sessionId)),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    await act(async () => {
      expect(await result.current.refreshSubtitles(100)).toBe(false);
    });

    await waitFor(() =>
      expect(result.current.errorReason).toBe("seek_reanchor_evidence_unavailable"),
    );
    expect(result.current.errorRetryable).toBe(true);
    // Only a failed `seek_reanchor` authorizes the rebuild.
    expect(startBodies).toHaveLength(1);

    unmount();
  });

  it("does not restart a second time when the replacement sees the same terminal", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      startChainMock(startBodies, (sessionId) => seekEvidenceTerminal(sessionId)),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    await act(async () => {
      expect(await result.current.reanchorSeek(300)).toBe(true);
    });
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-2"));
    expect(startBodies).toHaveLength(2);

    // The replacement session's own frozen evidence fails the same way. One
    // original attempt gets exactly one rebuild; this surfaces the retryable
    // terminal instead of starting a third session.
    await act(async () => {
      expect(await result.current.reanchorSeek(600)).toBe(false);
    });

    await waitFor(() => expect(result.current.error).toBe(SEEK_EVIDENCE_MESSAGE));
    expect(result.current.errorReason).toBe("seek_reanchor_evidence_unavailable");
    expect(result.current.errorRetryable).toBe(true);
    expect(startBodies).toHaveLength(2);

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

  // Cold-start default-audio reconciliation is not a route failure: the recipe
  // plays the wrong audio stream, not the wrong bytes. Replanning as
  // `failure_recovery` would fold the healthy route's attempt key into
  // `attempted_plan_keys` and force a worse route (or a failure) onto a session
  // that was playing fine.
  it("replans off a default-audio correction without failure semantics", async () => {
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
        }),
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef"));
    // The viewer is watching, then pauses: the replacement replan must carry the
    // position across the correction and leave the transport paused.
    act(() => {
      result.current.updatePlaybackState(300, true);
      result.current.updatePlaybackState(450, false);
    });

    let outcome: boolean | undefined;
    await act(async () => {
      outcome = await result.current.invalidatePlan(
        "plan:0123456789abcdef",
        PLAN_INVALIDATED_DEFAULT_AUDIO_RECONCILIATION,
        450,
      );
    });

    expect(outcome).toBe(true);
    expect(replanBodies).toHaveLength(1);
    const body = replanBodies[0]!;
    expect(body.operation).toBe("track_change");
    expect(body.position_seconds).toBe(450);
    // The reason goes back on the wire verbatim so the server can match this
    // replan against the decision it is still holding, instead of inferring the
    // pairing from the operation and the absent failure.
    expect(body.answers_plan_invalidation).toBe(PLAN_INVALIDATED_DEFAULT_AUDIO_RECONCILIATION);
    // No failure payload: the server rejects one on a track_change, and the
    // classification would indict a route that never failed.
    expect(body).not.toHaveProperty("failure");
    // Nothing failed, so the route that is playing stays eligible — the loop
    // guard resets rather than excluding the plan it is replacing.
    expect(body.attempted_plan_keys).toEqual([]);
    // The echoed selection is the withdrawn plan's own, unchanged: the client
    // chose no audio and carries none of the correction's identity. The server
    // overwrites it from the decision it persisted for this plan (§6.1.1), so
    // the client must not assert anything about the corrected index here.
    expect(body.selected_tracks).toMatchObject({
      audio: fixturePlanV3().selected_tracks.audio,
    });
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:2222222222222222"));
    // Pause state is untouched: the adopted plan reports the transport's own
    // paused state rather than autoplaying over the viewer's pause.
    expect(result.current.shouldAutoPlay).toBe(false);

    unmount();
  });

  // The deferred-inventory adoption: a cold virtual start commits a plan from
  // declared audio metadata, the probe reorders the inventory so the preferred
  // track lands at the same POSITIONAL index, and the verified revision then
  // arrives. Both menus fill in without a client replan; the server withdraws
  // the stale recipe and the client's replacement replan adopts the corrected
  // plan. The replacement is transport-different even though the positional
  // `selected_tracks.audio.index` is unchanged, because the executable stream
  // ordinal moved — the element must reload, or the already-rebuilt session
  // stream (which now muxes the corrected audio) is never read and playback
  // stays silent until a stop/resume.
  // The in-place adoption invariant: the deferred probe's real lists land
  // while the cold-start plan is still provisional, and a later plan adoption
  // (an output/quality refresh built before the probe persisted) must not
  // regress the menus to the stale declared list. Nothing replans for the
  // adoption, nothing reloads, and what is already playing keeps playing.
  it("keeps the real deferred inventory in place when a later provisional plan is adopted", async () => {
    const declaredAudio = [
      { index: 1, language: "eng", codec: "aac", channels: 2, layout: "stereo", default: true },
      { index: 3, language: "spa", codec: "eac3", channels: 6, layout: "5.1", default: false },
    ];
    // The probe found the preferred spa stream first in the verified order.
    const verifiedAudio = [
      { index: 3, language: "spa", codec: "eac3", channels: 6, layout: "5.1", default: true },
      { index: 1, language: "eng", codec: "aac", channels: 2, layout: "stereo", default: false },
    ];
    const declaredPlan = fixturePlanV3({
      inventory_status: "declared",
      inventory_provenance: "pending",
      tracks_pending: true,
      audio_tracks: declaredAudio,
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });
    // A replan built before the probe persisted: provisional again, carrying
    // the stale declared list rather than the real one.
    const provisionalPlan = fixturePlanV3({
      plan_id: "plan:2222222222222222",
      plan_attempt_key: "v3:2222222222222222",
      inventory_status: "declared",
      inventory_provenance: "pending",
      tracks_pending: true,
      audio_tracks: declaredAudio,
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });
    const verifiedSubtitle = {
      track_id: "file:7:subtitle:0",
      combined_index: 0,
      source: "embedded" as const,
      codec: "subrip",
      language: "eng",
      forced: false,
      default: false,
      hearing_impaired: false,
      delivery: "sidecar" as const,
      url: "/stream/session-1/subtitles/0.vtt?file_id=7",
    };
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          return jsonResponse(
            {
              protocol_version: 3,
              server_features: ["playback_plan_v3"],
              outcome: "playable",
              session_id: "session-1",
              playback_plan: declaredPlan,
            },
            { status: 201 },
          );
        }
        if (url.endsWith("/playback/session-1/replan")) {
          replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: provisionalPlan,
          });
        }
        if (url.endsWith("/playback/route-events")) {
          return new Response(null, { status: 202 });
        }
        if (init?.method === "DELETE") {
          return new Response(null, { status: 204 });
        }
        throw new Error(`Unexpected request: ${url}`);
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef"));
    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.transportRevision).toBe(1);

    // The verified revision lands while the provisional plan is still playing:
    // both menus fill in without a client replan and without a stream reload.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:1",
        inventory_status: "verified",
        effective_media_file_id: 7,
        audio_tracks: verifiedAudio,
        subtitle_inventory: [verifiedSubtitle],
      }),
    );
    expect(replanBodies).toHaveLength(0);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toEqual(verifiedAudio);
    expect(result.current.subtitleUrls).toHaveLength(1);

    // A later provisional plan is adopted (vehicle: an ordinary track change).
    // The real lists must survive it in place; the stream does not reload.
    await act(async () => {
      result.current.switchAudioTrack(1, 450);
    });
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:2222222222222222"));
    expect(replanBodies).toHaveLength(1);
    expect(result.current.planAudioTracks).toEqual(verifiedAudio);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleUrls).toHaveLength(1);
    expect(result.current.transportRevision).toBe(1);

    unmount();
  });

  // The generic path is what a genuine route failure depends on, and it must not
  // have widened to swallow every reason while the audio path was carved out.
  it("keeps failure_recovery semantics for any other invalidation reason", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      invalidationFetchMock(replanBodies, {
        protocol_version: 3,
        server_features: ["playback_plan_v3"],
        outcome: "playable",
        session_id: "session-1",
        playback_plan: fixturePlanV3({
          plan_id: "plan:3333333333333333",
          plan_attempt_key: "v3:3333333333333333",
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
        200,
      );
    });

    expect(outcome).toBe(true);
    expect(replanBodies[0]).toMatchObject({
      operation: "failure_recovery",
      failed_plan_id: "plan:0123456789abcdef",
      position_seconds: 200,
      attempted_plan_keys: ["v3:0123456789abcdef"],
      failure: { classification: "video_copy_unsafe" },
    });
    // A route failure carries its own evidence in `failure`; echoing a reason
    // here would claim the server asked for a non-failure correction it never
    // sent, and the server correlates on it.
    expect(replanBodies[0]).not.toHaveProperty("answers_plan_invalidation");

    unmount();
  });

  // An unrecognized reason is a route verdict this client cannot interpret, so
  // it recovers exactly as before rather than guessing at a non-failure path.
  it("keeps failure_recovery semantics for an unrecognized reason", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      invalidationFetchMock(replanBodies, {
        protocol_version: 3,
        server_features: ["playback_plan_v3"],
        outcome: "playable",
        session_id: "session-1",
        playback_plan: fixturePlanV3({
          plan_id: "plan:4444444444444444",
          plan_attempt_key: "v3:4444444444444444",
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
        "some_reason_from_a_newer_server",
        90,
      );
    });

    expect(outcome).toBe(true);
    expect(replanBodies[0]).toMatchObject({
      operation: "failure_recovery",
      position_seconds: 90,
      attempted_plan_keys: ["v3:0123456789abcdef"],
      failure: { classification: "some_reason_from_a_newer_server" },
    });

    unmount();
  });

  // The correlation field is scoped to the reconciliation path because it is a
  // claim about a pending server decision. A quality change, a subtitle change
  // and an inventory refresh are viewer intents with no withdrawal to answer,
  // and one of them happens to share the reconciliation's `track_change`
  // operation — which is exactly why the field cannot be inferred from the
  // operation alone.
  it("sends no answered-invalidation reason on viewer-initiated replans", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      invalidationFetchMock(replanBodies, {
        protocol_version: 3,
        server_features: ["playback_plan_v3"],
        outcome: "playable",
        session_id: "session-1",
        playback_plan: fixturePlanV3(),
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "original"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef"));

    await act(async () => {
      result.current.changeQuality("720p", 120);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    await act(async () => {
      result.current.changeSubtitleTrack(0, 200);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(2));

    await act(async () => {
      await result.current.refreshSubtitles(300);
    });
    await waitFor(() => expect(replanBodies).toHaveLength(3));

    expect(replanBodies.map((body) => body.operation)).toEqual([
      "quality_change",
      "track_change",
      "track_change",
    ]);
    for (const body of replanBodies) {
      expect(body).not.toHaveProperty("answers_plan_invalidation");
    }

    unmount();
  });

  // The server decides whether to send the reconciliation withdrawal by reading
  // the advertised features, so both the capability and the promise it belongs
  // to have to be on every request that can carry one — start and replan alike,
  // because a replan replaces the negotiated list.
  it("advertises the reconciliation capability on start and replan", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      invalidationFetchMock(replanBodies, {
        protocol_version: 3,
        server_features: ["playback_plan_v3"],
        outcome: "playable",
        session_id: "session-1",
        playback_plan: fixturePlanV3({ plan_id: "plan:2222222222222222" }),
      }),
    );

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef"));

    await act(async () => {
      await result.current.invalidatePlan(
        "plan:0123456789abcdef",
        PLAN_INVALIDATED_DEFAULT_AUDIO_RECONCILIATION,
        450,
      );
    });
    expect(replanBodies).toHaveLength(1);

    const startCall = vi
      .mocked(fetch)
      .mock.calls.find(([url]) => String(url).endsWith("/playback/start"));
    const startBody = JSON.parse(String(startCall?.[1]?.body)) as { client_features: string[] };
    for (const features of [startBody.client_features, replanBodies[0]!.client_features]) {
      expect(features).toContain("default_audio_reconcile_response_v1");
      // Genuine route-failure invalidations still depend on the older token.
      expect(features).toContain("plan_invalidated_v1");
    }

    unmount();
  });

  it("does nothing for a plan the session already moved past", async () => {
    const replanBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal("fetch", invalidationFetchMock(replanBodies, {}));

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef"));

    let outcome: boolean | undefined;
    await act(async () => {
      outcome = await result.current.invalidatePlan("plan:superseded", "video_copy_unsafe", 12);
    });

    // Reported as handled: the invalidated route is already gone, and replanning
    // would evict a plan the server never complained about.
    expect(outcome).toBe(true);
    expect(replanBodies).toHaveLength(0);

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

describe("usePlaybackSession plan audio inventory", () => {
  it("exposes the plan's audio_tracks as planAudioTracks on adoption", async () => {
    const planAudioTracks = [
      { index: 0, codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
      { index: 1, codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({ audio_tracks: planAudioTracks }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.planAudioTracks).toEqual(planAudioTracks);
    unmount();
  });

  it("defaults planAudioTracks to an empty list when the plan publishes none", async () => {
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
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });

  it("fills in a richer probed inventory without bumping the plan or transport revision", async () => {
    const planAudioTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const richer = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", index: 9, default: false },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              audio_tracks: planAudioTracks,
              subtitle: { mode: "off", inventory: [] },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    const planRevision = result.current.planRevision;
    const transportRevision = result.current.transportRevision;

    act(() => result.current.applyAudioInventory(richer));

    expect(result.current.planAudioTracks).toEqual(richer);
    // A menu-data fill-in must not reload the stream.
    expect(result.current.planRevision).toBe(planRevision);
    expect(result.current.transportRevision).toBe(transportRevision);
    // The plan's unversioned delivery path is projected into the v2 namespace
    // (see buildPlayerStreamUrl); a menu-data fill-in must not reload it.
    expect(result.current.streamUrl).toBe("/api/v2/stream/session-1/master.m3u8?token=token");
    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/playback/start")),
    ).toHaveLength(1);
    unmount();
  });

  it("ignores a refreshed inventory no richer than the plan's", async () => {
    const planAudioTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({ audio_tracks: planAudioTracks }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() =>
      result.current.applyAudioInventory([
        { codec: "eac3", channels: 6, layout: "5.1", language: "eng" },
      ]),
    );

    expect(result.current.planAudioTracks).toEqual(planAudioTracks);
    unmount();
  });

  it("replaces the inventory when the poll resolved a different file", async () => {
    const planAudioTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    // The plan names the collapsed row (7); the poll resolved the effective
    // candidate (8), whose probed inventory is poorer but authoritative for the
    // file actually playing.
    const poorerCandidateInventory = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              audio_tracks: planAudioTracks,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.applyAudioInventory(poorerCandidateInventory, 8));

    expect(result.current.planAudioTracks).toEqual(poorerCandidateInventory);
    unmount();
  });

  it("re-keys an unchanged-length candidate when only its URI moved under the same file", async () => {
    const planAudioTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    // The rotation landed on another candidate of the same resolved row: the
    // file id is unchanged and the catalogue list is the same length, so only
    // the URI distinguishes the new source.
    const sameLength = [
      { codec: "ac3", channels: 2, layout: "stereo", language: "spa", default: true },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 8,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: planAudioTracks,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 8, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    const planRevision = result.current.planRevision;
    const transportRevision = result.current.transportRevision;

    act(() => result.current.applyAudioInventory(sameLength, 8, "virtual://movie/x?result=B"));

    expect(result.current.mediaFileId).toBe(8);
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(sameLength);
    expect(result.current.audioInventoryProvisional).toBe(false);
    // Menu data only: the transport must not reload.
    expect(result.current.planRevision).toBe(planRevision);
    expect(result.current.transportRevision).toBe(transportRevision);
    unmount();
  });

  it("re-keys a moved candidate with an empty list without claiming a verified inventory", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 8,
              effective_virtual_uri: "virtual://movie/x?result=A",
              inventory_status: "declared",
              audio_tracks: [
                { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 8, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.audioInventoryProvisional).toBe(true);

    act(() => result.current.applyAudioInventory([], 8, "virtual://movie/x?result=B"));

    // The identity moved and the previous release's tracks are gone, but an
    // empty list is not probe evidence: the menu stays marked.
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual([]);
    expect(result.current.audioInventoryProvisional).toBe(true);
    unmount();
  });

  it("keeps the superset guard when the poll names the plan's own file", async () => {
    const planAudioTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              audio_tracks: planAudioTracks,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() =>
      result.current.applyAudioInventory(
        [{ codec: "eac3", channels: 6, layout: "5.1", language: "eng" }],
        7,
      ),
    );

    expect(result.current.planAudioTracks).toEqual(planAudioTracks);
    unmount();
  });

  it("adopts a committed source's identity and declared inventory without reloading", async () => {
    const planAudioTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: planAudioTracks,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    const planRevision = result.current.planRevision;
    const transportRevision = result.current.transportRevision;

    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "declared",
        },
        [{ codec: "eac3", channels: 6, layout: "5.1", language: "deu", default: true }],
      ),
    );

    expect(result.current.mediaFileId).toBe(8);
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual([
      { codec: "eac3", channels: 6, layout: "5.1", language: "deu", default: true },
    ]);
    // A committed-source push is menu data: the stream must not reload.
    expect(result.current.planRevision).toBe(planRevision);
    expect(result.current.transportRevision).toBe(transportRevision);
    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/playback/start")),
    ).toHaveLength(1);
    unmount();
  });

  it("clears the previous release's inventory when a committed source declares none", async () => {
    const planAudioTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: planAudioTracks,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "declared",
        },
        [],
      ),
    );

    // The previous release's tracks must not survive under the new source.
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });

  it("keeps the audio marker on its matched row when an adopted replan reorders its inventory", async () => {
    const englishTrack = {
      codec: "eac3",
      channels: 6,
      layout: "5.1",
      language: "eng",
      default: true,
      track_id: "file:7:audio:0",
      selection_index: 0,
    };
    const spanishTrack = {
      codec: "ac3",
      channels: 2,
      layout: "stereo",
      language: "spa",
      default: false,
      track_id: "file:7:audio:1",
      selection_index: 1,
    };
    const verifiedAudioTracks = [englishTrack, spanishTrack];
    // The replan was built before the probe persisted, so it carries the older
    // declared order and marks itself provisional. The server mints ids from
    // the file id and published ordinal, so the reordered list re-mints them:
    // Spanish now occupies slot 0 and English slot 1. Keeping each track's old
    // id, as this test used to, does not reproduce what the server sends.
    const reorderedPlanTracks = [
      { ...spanishTrack, track_id: "file:7:audio:0", selection_index: 0 },
      { ...englishTrack, track_id: "file:7:audio:1", selection_index: 1 },
    ];
    const replanBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "verified",
              inventory_provenance: "verified",
              audio_tracks: verifiedAudioTracks,
              selected_tracks: { audio: { id: "file:7:audio:1", index: 1 } },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:reordered000000001",
            plan_attempt_key: "v3:reordered000000001",
            inventory_status: "declared",
            inventory_provenance: "declared",
            tracks_pending: true,
            audio_tracks: reorderedPlanTracks,
            selected_tracks: { audio: { id: "file:7:audio:1", index: 1 } },
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.planAudioTracks).toEqual(verifiedAudioTracks);
    expect(result.current.audioTrackIndex).toBe(1);

    // Pick the English row at displayed index 0. The replan answers with the
    // same track at its own (reordered) ordinal 1.
    act(() => result.current.switchAudioTrack(0, 120));
    await waitFor(() => expect(replanBodies).toHaveLength(1));

    // The verified list survives the provisional replan, and the marker must
    // follow the row naming the committed track (English at displayed 0) rather
    // than the plan ordinal 1, which now names the Spanish row.
    expect(result.current.planAudioTracks).toEqual(verifiedAudioTracks);
    expect(result.current.audioTrackIndex).toBe(0);
    unmount();
  });

  it("carries the plan's audio identity for a pick from a repaired inventory", async () => {
    const planAudioTracks = [
      {
        codec: "eac3",
        channels: 6,
        layout: "5.1",
        language: "eng",
        default: true,
        track_id: "file:7:audio:0",
        selection_index: 0,
      },
      {
        codec: "ac3",
        channels: 2,
        layout: "stereo",
        language: "spa",
        default: false,
        track_id: "file:7:audio:1",
        selection_index: 1,
      },
    ];
    // A probe repair reveals a third track and reorders the existing two.
    const repaired: PlayerAudioTrack[] = [
      { codec: "ac3", channels: 2, layout: "stereo", language: "spa" },
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng" },
      { codec: "aac", channels: 2, layout: "stereo", language: "fra" },
    ];
    const replanBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              audio_tracks: planAudioTracks,
              selected_tracks: { audio: { id: "file:7:audio:1", index: 1 } },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:repaired000000001",
            plan_attempt_key: "v3:repaired000000001",
            audio_tracks: planAudioTracks,
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.applyAudioInventory(repaired, 7));
    expect(result.current.planAudioTracks).toEqual(repaired);
    // The committed selection (spa) followed the applied row that carries it.
    expect(result.current.audioTrackIndex).toBe(0);

    // The menu's English row is at displayed index 1, but the plan holds it at
    // ordinal 0. The identity names the picked language and the plan ordinal it
    // encodes, so the pair is internally consistent for the server.
    act(() => result.current.switchAudioTrack(1, 130));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    expect(replanBodies[0]).toMatchObject({
      operation: "track_change",
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });

    // Picking the already-playing track resolves to the same identity and is a
    // no-op, even though its displayed index (1) no longer matches the plan's
    // selected ordinal (0).
    act(() => result.current.applyAudioInventory(repaired, 7));
    act(() => result.current.switchAudioTrack(1, 131));
    expect(replanBodies).toHaveLength(1);

    unmount();
  });

  it("exposes a declared inventory as provisional on both menus", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "declared",
              inventory_provenance: "declared",
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.subtitleInventoryProvisional).toBe(true);
    unmount();
  });

  it("keeps a verified inventory confident", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "verified",
              inventory_provenance: "verified",
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    unmount();
  });

  it("clears only the audio flag when the poll folds in probed tracks", async () => {
    const planAudioTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const richer = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", index: 9, default: false },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "declared",
              audio_tracks: planAudioTracks,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.audioInventoryProvisional).toBe(true);

    act(() => result.current.applyAudioInventory(richer));

    expect(result.current.planAudioTracks).toEqual(richer);
    expect(result.current.audioInventoryProvisional).toBe(false);
    // The subtitle list resolves through its own replan, not the audio poll.
    expect(result.current.subtitleInventoryProvisional).toBe(true);
    unmount();
  });

  it("folds a verified live inventory update into both menus without a replan", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "declared",
              audio_tracks: [
                { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.subtitleInventoryProvisional).toBe(true);

    const verifiedAudio = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 2, layout: "stereo", language: "spa", default: false },
    ];

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:1",
        inventory_status: "verified",
        effective_media_file_id: 7,
        audio_tracks: verifiedAudio,
        subtitle_inventory: [
          {
            track_id: "file:7:subtitle:0",
            combined_index: 0,
            source: "embedded",
            codec: "subrip",
            language: "eng",
            forced: false,
            default: false,
            hearing_impaired: false,
            delivery: "sidecar",
            url: "/stream/session-1/subtitles/0.vtt?file_id=7",
          },
        ],
      }),
    );

    expect(result.current.planAudioTracks).toEqual(verifiedAudio);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.subtitleUrls).toHaveLength(1);
    expect(result.current.subtitleUrls[0]?.language).toBe("eng");

    // A later verified revision that drops every subtitle clears the menu
    // instead of leaving the lighter declared entry behind.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:2",
        inventory_status: "verified",
        effective_media_file_id: 7,
        audio_tracks: verifiedAudio,
        subtitle_inventory: [],
      }),
    );
    expect(result.current.subtitleUrls).toHaveLength(0);
    expect(result.current.subtitleInventoryProvisional).toBe(false);

    // Stale A/B/A replay: delivering previously-seen inv:1 again is rejected.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:1",
        inventory_status: "verified",
        effective_media_file_id: 7,
        audio_tracks: verifiedAudio,
        subtitle_inventory: [
          {
            track_id: "file:7:subtitle:0",
            combined_index: 0,
            source: "embedded",
            codec: "subrip",
            language: "eng",
            forced: false,
            default: false,
            hearing_impaired: false,
            delivery: "sidecar",
            url: "/stream/session-1/subtitles/0.vtt?file_id=7",
          },
        ],
      }),
    );
    expect(result.current.subtitleUrls).toHaveLength(0);

    // Stale push naming another source identity (file 99) is rejected.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:3",
        inventory_status: "verified",
        effective_media_file_id: 99,
        audio_tracks: verifiedAudio,
        subtitle_inventory: [
          {
            track_id: "file:99:subtitle:0",
            combined_index: 0,
            source: "embedded",
            codec: "subrip",
            language: "spa",
            forced: false,
            default: false,
            hearing_impaired: false,
            delivery: "sidecar",
            url: "/stream/session-1/subtitles/0.vtt?file_id=99",
          },
        ],
      }),
    );
    expect(result.current.subtitleUrls).toHaveLength(0);

    // Menu data only: the fold never hits the transport boundary for a replan.
    expect(
      fetchMock.mock.calls.filter(([input]) => String(input).includes("/replan")),
    ).toHaveLength(0);
    unmount();
  });

  it("handles tracks_pending and inventory_url from deferred start, cleared by verified push", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-deferred-1",
            playback_plan: fixturePlanV3({
              session_id: "session-deferred-1",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-deferred-1/inventory",
              inventory_revision: "inv:provisional",
              inventory_status: "declared",
              inventory_provenance: "pending",
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-deferred-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    // Started with provisional/pending inventory
    expect(result.current.inventoryPending).toBe(true);
    expect(result.current.inventoryUrl).toBe("/api/v2/playback/session-deferred-1/inventory");
    expect(result.current.inventoryRevision).toBe("inv:provisional");
    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.subtitleInventoryProvisional).toBe(true);

    // Live inventory_updated push arrives from probe
    const verifiedAudio = [
      { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
      { codec: "eac3", channels: 6, layout: "5.1", language: "deu", default: false },
    ];
    const verifiedSubtitle = [
      fixtureSubtitleInventoryItemV3({
        track_id: "file:7:subtitle:0",
        language: "deu",
        label: "German",
      }),
    ];

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-deferred-1",
        inventory_revision: "inv:verified-1",
        inventory_status: "verified",
        effective_media_file_id: 7,
        audio_tracks: verifiedAudio,
        subtitle_inventory: verifiedSubtitle,
      }),
    );

    // Pending cleared, tracks updated live
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.inventoryRevision).toBe("inv:verified-1");
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toHaveLength(2);
    expect(result.current.subtitleUrls).toHaveLength(1);
    expect(result.current.subtitleUrls[0]?.language).toBe("deu");

    unmount();
  });

  it("adopts a verified inventory push that moves only the catalog row of the same candidate", async () => {
    // Production capture (#345): a deferred probe's verified inventory was
    // published after the serve layer moved the session onto the owner catalog
    // row of the *same* concrete candidate (`?result=` unchanged). The push
    // therefore named a different effective_media_file_id than the plan the
    // client was still playing. The stale-delivery guard compared file ids
    // only and dropped the revision, so the open session stayed on
    // "Discovering…" until the viewer closed the player and resumed.
    const candidateURI = "virtual://series/show/2/3?result=d3b36e7d105c9493432429f3";
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-row-move",
            playback_plan: fixturePlanV3({
              session_id: "session-row-move",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-row-move/inventory",
              inventory_status: "pending",
              inventory_provenance: "pending",
              effective_media_file_id: 7,
              effective_virtual_uri: candidateURI,
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-row-move", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.inventoryPending).toBe(true);

    const verifiedAudio = [
      { codec: "aac", channels: 2, layout: "stereo", language: "ja", default: true },
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: false },
    ];
    const verifiedSubtitles = Array.from({ length: 12 }, (_, index) =>
      fixtureSubtitleInventoryItemV3({
        track_id: `file:8:subtitle:${index}`,
        combined_index: index,
        language: index === 3 ? "eng" : "fra",
      }),
    );

    let adopted = false;
    act(() => {
      adopted = result.current.applyInventoryUpdate({
        session_id: "session-row-move",
        inventory_revision: "inv:verified-row-8",
        inventory_status: "verified",
        // Same concrete candidate, owner row 8 instead of the plan's row 7.
        effective_media_file_id: 8,
        effective_virtual_uri: candidateURI,
        audio_tracks: verifiedAudio,
        subtitle_inventory: verifiedSubtitles,
      });
    });

    // The delivered revision was for the source on screen, so it must be
    // adopted: no poll, replan, or reopen is needed to finish discovery.
    expect(adopted).toBe(true);
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.mediaFileId).toBe(8);
    expect(result.current.planAudioTracks).toHaveLength(2);
    expect(result.current.subtitleUrls).toHaveLength(12);

    // A genuinely different candidate on another row is still stale traffic.
    act(() => {
      adopted = result.current.applyInventoryUpdate({
        session_id: "session-row-move",
        inventory_revision: "inv:verified-other",
        inventory_status: "verified",
        effective_media_file_id: 9,
        effective_virtual_uri: "virtual://series/show/2/3?result=other",
        audio_tracks: verifiedAudio,
        subtitle_inventory: verifiedSubtitles,
      });
    });
    expect(adopted).toBe(false);
    expect(result.current.mediaFileId).toBe(8);

    // A later declared source commit may name yet another row of the same
    // candidate (the serve layer associating the session with its final owner
    // row). It moves the live row without taking the verified menu back to
    // "Discovering…".
    act(() => {
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 9,
          effectiveVirtualUri: candidateURI,
          inventoryStatus: "declared",
        },
        verifiedAudio,
      );
    });
    expect(result.current.mediaFileId).toBe(9);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toHaveLength(2);
    expect(result.current.subtitleUrls).toHaveLength(12);

    unmount();
  });

  it("polls inventory_url when pending and clears loading state on 200 verified response", async () => {
    let inventoryPolled = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-poll-1",
            playback_plan: fixturePlanV3({
              session_id: "session-poll-1",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-poll-1/inventory",
              inventory_revision: "inv:prov-1",
              inventory_status: "declared",
              inventory_provenance: "pending",
              audio_tracks: [],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/api/v2/playback/session-poll-1/inventory")) {
        inventoryPolled = true;
        const ifNoneMatch = (init?.headers as Record<string, string> | undefined)?.[
          "If-None-Match"
        ];
        expect(ifNoneMatch).toBe("inv:prov-1");
        return jsonResponse({
          session_id: "session-poll-1",
          inventory_revision: "inv:probed-ok",
          inventory_status: "verified",
          effective_media_file_id: 7,
          audio_tracks: [
            { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
          ],
          subtitle_inventory: [
            fixtureSubtitleInventoryItemV3({
              track_id: "file:7:subtitle:0",
              language: "eng",
              label: "English",
            }),
          ],
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-poll-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.inventoryPending).toBe(true);

    let pollResult = false;
    await act(async () => {
      pollResult = await result.current.pollInventory();
    });

    expect(inventoryPolled).toBe(true);
    expect(pollResult).toBe(true);
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.inventoryRevision).toBe("inv:probed-ok");
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toHaveLength(1);
    expect(result.current.subtitleUrls).toHaveLength(1);

    unmount();
  });

  it("rejects a terminal poll response for a mismatched source identity and keeps discovery pending", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-mismatch-1",
            playback_plan: fixturePlanV3({
              session_id: "session-mismatch-1",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-mismatch-1/inventory",
              inventory_revision: "inv:prov-1",
              effective_media_file_id: 7,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/api/v2/playback/session-mismatch-1/inventory")) {
        // Returns verified status, but for file 99 (a different release)
        return jsonResponse({
          session_id: "session-mismatch-1",
          inventory_revision: "inv:mismatched-file",
          inventory_status: "verified",
          effective_media_file_id: 99,
          audio_tracks: [
            { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
          ],
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-mismatch-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.inventoryPending).toBe(true);

    let pollResult = true;
    await act(async () => {
      pollResult = await result.current.pollInventory();
    });

    // Rejected because source identity 99 does not match session file 7
    expect(pollResult).toBe(false);
    expect(result.current.inventoryPending).toBe(true);

    unmount();
  });

  it("does not mark a timed-out poll failed once a refreshed plan supplies the confident list", async () => {
    // Production capture (episode 2/20): the deferred inventory never reached
    // terminal `verified`; every poll returned `declared` for a *different*
    // effective row, so the client dropped it and the attempt budget expired.
    // The probe evidence instead arrived through a track_change replan whose
    // plan carried the full list with no declared marker. Exhausting the poll
    // afterwards must not re-arm the discovery-failure notice above that list.
    const verifiedAudio = [
      { codec: "eac3", channels: 2, layout: "stereo", language: "en", default: true },
      { codec: "eac3", channels: 2, layout: "stereo", language: "ja", default: false },
    ];
    const verifiedSubtitles = Array.from({ length: 10 }, (_, index) =>
      fixtureSubtitleInventoryItemV3({
        track_id: `file:7:subtitle:${index}`,
        combined_index: index,
        language: index === 0 ? "eng" : "fra",
      }),
    );
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-exhaust-confidence",
            playback_plan: fixturePlanV3({
              session_id: "session-exhaust-confidence",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-exhaust-confidence/inventory",
              inventory_revision: "inv:prov-1",
              inventory_status: "declared",
              inventory_provenance: "pending",
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://series/show/2/20?result=b5950e",
              audio_tracks: [
                { codec: "eac3", channels: 2, layout: "stereo", language: "en", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/replan")) {
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-exhaust-confidence",
          playback_plan: fixturePlanV3({
            session_id: "session-exhaust-confidence",
            plan_id: "plan:replan-verified",
            plan_attempt_key: "v3:replan-verified",
            inventory_url: "/api/v2/playback/session-exhaust-confidence/inventory",
            effective_media_file_id: 7,
            effective_virtual_uri: "virtual://series/show/2/20?result=b5950e",
            audio_tracks: verifiedAudio,
            subtitle: { mode: "off", inventory: verifiedSubtitles },
          }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-exhaust-confidence", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.inventoryPending).toBe(true);
    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.subtitleInventoryProvisional).toBe(true);

    await act(async () => {
      await result.current.reanchorSeek(120);
    });

    // The refreshed plan carries the probed list with no declared marker, so
    // both menus are confident even though the deferred poll is still owed.
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toHaveLength(2);
    expect(result.current.subtitleUrls).toHaveLength(10);

    act(() => {
      result.current.markInventoryExhausted();
    });

    // The menus already render confident probe evidence, so the spent budget
    // is moot: it must not re-arm the failure notice. Nothing is owed — the
    // exhausted round scheduled no successor — so the pending flag clears too
    // rather than leaving discovery reading as in flight with no poll running.
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.inventoryFailed).toBe(false);
    // The confident list is untouched.
    expect(result.current.planAudioTracks).toHaveLength(2);
    expect(result.current.subtitleUrls).toHaveLength(10);

    unmount();
  });

  it("keeps a discovery-failure marker for provisional subtitles after an audio-only recovery", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-late-evidence",
            playback_plan: fixturePlanV3({
              session_id: "session-late-evidence",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-late-evidence/inventory",
              inventory_revision: "inv:prov-1",
              inventory_status: "declared",
              inventory_provenance: "pending",
              effective_media_file_id: 7,
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-late-evidence", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    // Both menus start provisional, so the exhausted poll is a failure.
    act(() => {
      result.current.markInventoryExhausted();
    });
    expect(result.current.inventoryFailed).toBe(true);

    // An audio-only catalog fold supplies probe evidence for the audio menu but
    // resolves nothing for subtitles. The audio menu renders its confident list
    // (its failure banner is gated on its own provisional flag), while the
    // shared marker must survive so the still-provisional subtitle menu keeps
    // its terminal failure notice instead of falling back to an indefinite
    // spinner. A later markInventoryExhausted is a no-op (pending is false), so
    // clearing here cannot be recovered.
    act(() => {
      result.current.applyAudioInventory(
        [
          { codec: "eac3", channels: 2, layout: "stereo", language: "en", default: true },
          { codec: "eac3", channels: 2, layout: "stereo", language: "ja", default: false },
        ],
        7,
      );
    });

    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(true);
    expect(result.current.inventoryFailed).toBe(true);
    expect(result.current.planAudioTracks).toHaveLength(2);

    // A later verified full-inventory push resolves the remaining provisional
    // menu and clears the shared marker.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-late-evidence",
        inventory_status: "verified",
        inventory_revision: "inv:verified-after-audio",
        effective_media_file_id: 7,
        audio_tracks: [
          { codec: "eac3", channels: 2, layout: "stereo", language: "en", default: true },
          { codec: "eac3", channels: 2, layout: "stereo", language: "ja", default: false },
        ],
        subtitle_inventory: [
          fixtureSubtitleInventoryItemV3({ track_id: "file:7:subtitle:0", combined_index: 0 }),
        ],
      }),
    );

    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.inventoryFailed).toBe(false);

    unmount();
  });

  it("keeps an audio-only recovery's failure marker across a same-identity follow-up fold", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-late-evidence-repeat",
            playback_plan: fixturePlanV3({
              session_id: "session-late-evidence-repeat",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-late-evidence-repeat/inventory",
              inventory_revision: "inv:prov-1",
              inventory_status: "declared",
              inventory_provenance: "pending",
              effective_media_file_id: 7,
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-late-evidence-repeat", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      result.current.markInventoryExhausted();
    });
    expect(result.current.inventoryFailed).toBe(true);

    // A same-identity audio fold resolves the audio side but must preserve the
    // shared failure while subtitles are still provisional; a second
    // same-identity fold (e.g. a richer catalog read) must not clear it either.
    const audioFold = [
      { codec: "eac3", channels: 2, layout: "stereo", language: "en", default: true },
      { codec: "eac3", channels: 2, layout: "stereo", language: "ja", default: false },
    ];
    act(() => {
      result.current.applyAudioInventory(audioFold, 7);
    });
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(true);
    expect(result.current.inventoryFailed).toBe(true);

    act(() => {
      result.current.applyAudioInventory(
        [
          ...audioFold,
          { codec: "eac3", channels: 6, layout: "5.1", language: "de", default: false },
        ],
        7,
      );
    });
    expect(result.current.subtitleInventoryProvisional).toBe(true);
    expect(result.current.inventoryFailed).toBe(true);

    unmount();
  });

  it("keeps an audio recovery's failure marker when the fold moves to another file", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-late-evidence-rotation",
            playback_plan: fixturePlanV3({
              session_id: "session-late-evidence-rotation",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-late-evidence-rotation/inventory",
              inventory_revision: "inv:prov-1",
              inventory_status: "declared",
              inventory_provenance: "pending",
              effective_media_file_id: 7,
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-late-evidence-rotation", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      result.current.markInventoryExhausted();
    });
    expect(result.current.inventoryFailed).toBe(true);

    // A changed-identity fold carries positive audio evidence for the new
    // source, but the subtitle inventory is still provisional, so the shared
    // failure marker must survive for the subtitle menu.
    act(() => {
      result.current.applyAudioInventory(
        [{ codec: "eac3", channels: 2, layout: "stereo", language: "en", default: true }],
        8,
      );
    });

    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(true);
    expect(result.current.inventoryFailed).toBe(true);

    unmount();
  });

  it("preserves verified inventory across a stale pending replan and re-clears pending on redelivery", async () => {
    // The probe verified these rows against the playing source: English at the
    // first slot, French at the second.
    const verifiedSubtitles = [
      fixtureSubtitleInventoryItemV3({
        track_id: "file:7:subtitle:0",
        combined_index: 0,
        language: "eng",
        label: "English",
      }),
      fixtureSubtitleInventoryItemV3({
        track_id: "file:7:subtitle:1",
        combined_index: 1,
        language: "fra",
        label: "French",
      }),
    ];
    // The replan was built before the probe persisted, so it still publishes
    // the declared order and re-mints the ordinal-derived ids: French now
    // occupies slot 0 and English slot 1.
    const reorderedPlanSubtitles = [
      { ...verifiedSubtitles[1]!, track_id: "file:7:subtitle:0", combined_index: 0 },
      { ...verifiedSubtitles[0]!, track_id: "file:7:subtitle:1", combined_index: 1 },
    ];
    let replanCalled = false;
    const replanBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-stale-replan",
            playback_plan: fixturePlanV3({
              session_id: "session-stale-replan",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-stale-replan/inventory",
              inventory_revision: "inv:prov",
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/test?result=c1",
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/replan")) {
        replanCalled = true;
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        // Server replan returns a plan that still echoes the older provisional/pending inventory
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-stale-replan",
          playback_plan: fixturePlanV3({
            session_id: "session-stale-replan",
            plan_id: "plan:replan-1",
            plan_attempt_key: "v3:replan-1",
            tracks_pending: true,
            inventory_url: "/api/v2/playback/session-stale-replan/inventory",
            effective_media_file_id: 7,
            effective_virtual_uri: "virtual://movie/test?result=c1",
            audio_tracks: [
              { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
            ],
            subtitle: { mode: "render", inventory: reorderedPlanSubtitles },
            selected_tracks: {
              audio: { id: "file:7:audio:0", index: 0 },
              // English, at the ordinal the reordered plan assigned it.
              subtitle: { id: "file:7:subtitle:1", index: 1 },
            },
          }),
        });
      }
      if (url.endsWith("/inventory")) {
        return jsonResponse({
          session_id: "session-stale-replan",
          inventory_revision: "inv:verified-1",
          inventory_status: "verified",
          effective_media_file_id: 7,
          effective_virtual_uri: "virtual://movie/test?result=c1",
          audio_tracks: verifiedAudio,
          subtitle_inventory: verifiedSubtitles,
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-stale-replan", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    // Step 1: probe lands, verified inventory update arrives
    const verifiedAudio = [
      { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
      { codec: "eac3", channels: 6, layout: "5.1", language: "deu", default: false },
    ];
    act(() => {
      result.current.applyInventoryUpdate({
        session_id: "session-stale-replan",
        inventory_revision: "inv:verified-1",
        inventory_status: "verified",
        effective_media_file_id: 7,
        effective_virtual_uri: "virtual://movie/test?result=c1",
        audio_tracks: verifiedAudio,
        subtitle_inventory: verifiedSubtitles,
      });
    });

    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toHaveLength(2);

    // Step 2: a seek/track replan occurs, and server echoes the start attempt's pending plan
    await act(async () => {
      await result.current.reanchorSeek(120);
    });
    expect(replanCalled).toBe(true);

    // Authority anchor preserves verified inventory and prevents downgrade to pending
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toHaveLength(2);
    // The menu keeps the verified rows in their verified order, even though the
    // adopted provisional plan publishes another one.
    expect(result.current.subtitleUrls.map((track) => track.language)).toEqual(["eng", "fra"]);

    // A manual pick from the preserved rows must name the clicked asset, not
    // whichever track inherited its ordinal in the adopted plan. Displayed
    // French still carries `file:7:subtitle:1`, but the plan's French row is
    // `file:7:subtitle:0`; trusting the equal-looking id would switch language.
    act(() => result.current.changeSubtitleTrack(1, 130));
    await waitFor(() => expect(replanBodies).toHaveLength(2));
    const planFrench = result.current.plan?.subtitle.inventory.find(
      (item) => item.language === "fra",
    );
    expect(planFrench).toMatchObject({ track_id: "file:7:subtitle:0", combined_index: 0 });
    expect(replanBodies[1]).toMatchObject({
      operation: "track_change",
      selected_tracks: {
        subtitle: { id: planFrench?.track_id, index: planFrench?.combined_index },
      },
    });

    // Step 3: redelivery of inv:verified-1 terminal update reconciles successfully
    let accepted = false;
    act(() => {
      accepted = result.current.applyInventoryUpdate({
        session_id: "session-stale-replan",
        inventory_revision: "inv:verified-1",
        inventory_status: "verified",
        effective_media_file_id: 7,
        effective_virtual_uri: "virtual://movie/test?result=c1",
        audio_tracks: verifiedAudio,
        subtitle_inventory: verifiedSubtitles,
      });
    });

    expect(accepted).toBe(true);
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.planAudioTracks).toHaveLength(2);

    // Poll from inventory_url to confirm pollInventory() still runs and returns true
    let pollAccepted = false;
    await act(async () => {
      pollAccepted = await result.current.pollInventory();
    });

    expect(pollAccepted).toBe(true);
    expect(result.current.inventoryPending).toBe(false);

    unmount();
  });

  it("preserves verified-empty inventory across a stale replan declaring nonempty tracks", async () => {
    let replanCalled = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-empty-replan",
            playback_plan: fixturePlanV3({
              session_id: "session-empty-replan",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-empty-replan/inventory",
              inventory_revision: "inv:prov-empty",
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/empty?result=e1",
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
                { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/replan")) {
        replanCalled = true;
        // Stale replan echoes original declared nonempty tracks with tracks_pending
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-empty-replan",
          playback_plan: fixturePlanV3({
            session_id: "session-empty-replan",
            plan_id: "plan:empty-replan-1",
            plan_attempt_key: "v3:empty-replan-1",
            tracks_pending: true,
            effective_media_file_id: 7,
            effective_virtual_uri: "virtual://movie/empty?result=e1",
            audio_tracks: [
              { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
            ],
          }),
        });
      }
      if (url.endsWith("/inventory")) {
        return jsonResponse({
          session_id: "session-empty-replan",
          inventory_revision: "inv:verified-empty",
          inventory_status: "verified",
          effective_media_file_id: 7,
          effective_virtual_uri: "virtual://movie/empty?result=e1",
          audio_tracks: [],
          subtitle_inventory: [],
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-empty-replan", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    // Step 1: probe lands, verified empty inventory update arrives
    act(() => {
      result.current.applyInventoryUpdate({
        session_id: "session-empty-replan",
        inventory_revision: "inv:verified-empty",
        inventory_status: "verified",
        effective_media_file_id: 7,
        effective_virtual_uri: "virtual://movie/empty?result=e1",
        audio_tracks: [],
        subtitle_inventory: [],
      });
    });

    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toHaveLength(0);
    expect(result.current.subtitleUrls).toHaveLength(0);

    // Step 2: a replan occurs echoing the start attempt's declared nonempty tracks
    await act(async () => {
      await result.current.reanchorSeek(60);
    });
    expect(replanCalled).toBe(true);

    // Verified empty inventory is preserved: declared tracks NOT restored, pending NOT restored
    expect(result.current.planAudioTracks).toHaveLength(0);
    expect(result.current.subtitleUrls).toHaveLength(0);
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);

    unmount();
  });

  it("sets inventoryFailed when inventory_updated push reports failed status", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-fail-1",
            playback_plan: fixturePlanV3({
              session_id: "session-fail-1",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-fail-1/inventory",
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-fail-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.inventoryPending).toBe(true);
    expect(result.current.inventoryFailed).toBe(false);

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-fail-1",
        inventory_status: "failed",
        inventory_revision: "inv:failed-1",
      }),
    );

    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.inventoryFailed).toBe(true);

    unmount();
  });

  it("markInventoryExhausted transitions pending inventory to failed state", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-exhaust-1",
            playback_plan: fixturePlanV3({
              session_id: "session-exhaust-1",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-exhaust-1/inventory",
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-exhaust-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.inventoryPending).toBe(true);
    expect(result.current.inventoryFailed).toBe(false);

    act(() => {
      result.current.markInventoryExhausted();
    });

    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.inventoryFailed).toBe(true);

    unmount();
  });

  it("marks a legacy declared inventory failed when its poll is exhausted", async () => {
    // An older server declares the inventory without deferring it: no
    // inventory_url and no tracks_pending. The legacy catalog poll owns the
    // retry, so the menus are provisional while `inventoryPending` stays false.
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-legacy-exhaust",
            playback_plan: fixturePlanV3({
              session_id: "session-legacy-exhaust",
              inventory_status: "declared",
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-legacy-exhaust", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.inventoryUrl).toBeNull();
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.inventoryFailed).toBe(false);

    act(() => {
      result.current.markInventoryExhausted();
    });

    // The spent budget is a terminal failure for the still-declared menu, not
    // an indefinite "Discovering…".
    expect(result.current.inventoryFailed).toBe(true);

    unmount();
  });

  it("never labels a verified legacy inventory failed when its poll is exhausted", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-legacy-verified",
            playback_plan: fixturePlanV3({
              session_id: "session-legacy-verified",
              inventory_status: "verified",
              inventory_provenance: "verified",
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-legacy-verified", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.audioInventoryProvisional).toBe(false);

    act(() => {
      result.current.markInventoryExhausted();
    });

    // A confident list is not a failed discovery; the exhausted budget is moot.
    expect(result.current.inventoryFailed).toBe(false);
    expect(result.current.planAudioTracks).toHaveLength(1);

    unmount();
  });

  it("keeps a legacy inventory verified when the exhausted poll fires after the probe lands", async () => {
    // A late-arriving verified list resolves both menus while the legacy poll is
    // still scheduled. The stale exhaustion callback must read that current
    // state and stay a no-op rather than overwrite the confident list.
    const verifiedAudio = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "en", default: true },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-legacy-stale-exhaust",
            playback_plan: fixturePlanV3({
              session_id: "session-legacy-stale-exhaust",
              inventory_status: "declared",
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-legacy-stale-exhaust", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.subtitleInventoryProvisional).toBe(true);

    // The probe lands and both menus turn confident before the budget runs out.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-legacy-stale-exhaust",
        inventory_status: "verified",
        inventory_revision: "inv:legacy-late-verified",
        effective_media_file_id: 7,
        audio_tracks: verifiedAudio,
        subtitle_inventory: [
          fixtureSubtitleInventoryItemV3({ track_id: "file:7:subtitle:0", combined_index: 0 }),
        ],
      }),
    );

    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.inventoryFailed).toBe(false);

    act(() => {
      result.current.markInventoryExhausted();
    });

    // The exhausted budget observed a verified inventory, so it must not
    // resurrect a failure or disturb the confident lists.
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.inventoryFailed).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toEqual(verifiedAudio);

    unmount();
  });

  it("clears a legacy inventory exhaustion failure when a verified list arrives late", async () => {
    // The poll budget runs out first: a still-declared menu is a terminal
    // failure. The probe then lands afterwards, and the failure must clear so it
    // never masks the verified list.
    const verifiedAudio = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "en", default: true },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-legacy-late-verified",
            playback_plan: fixturePlanV3({
              session_id: "session-legacy-late-verified",
              inventory_status: "declared",
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-legacy-late-verified", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      result.current.markInventoryExhausted();
    });
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.inventoryFailed).toBe(true);

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-legacy-late-verified",
        inventory_status: "verified",
        inventory_revision: "inv:legacy-verified-after-exhaust",
        effective_media_file_id: 7,
        audio_tracks: verifiedAudio,
        subtitle_inventory: [
          fixtureSubtitleInventoryItemV3({ track_id: "file:7:subtitle:0", combined_index: 0 }),
        ],
      }),
    );

    // Positive probe evidence clears the exhausted-budget marker; the verified
    // list is the menu the viewer sees.
    expect(result.current.inventoryPending).toBe(false);
    expect(result.current.inventoryFailed).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toEqual(verifiedAudio);

    unmount();
  });

  it("ignores an exhaustion that names a replaced session or file", async () => {
    // A timer armed for one session (or file) can fire after the player has
    // moved on. The expected identity passed with the call must drop it rather
    // than mark the live session's menus failed.
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-guard",
            playback_plan: fixturePlanV3({
              session_id: "session-guard",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-guard/inventory",
              inventory_status: "declared",
              inventory_provenance: "pending",
              effective_media_file_id: 7,
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-guard", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.inventoryPending).toBe(true);
    expect(result.current.inventoryFailed).toBe(false);

    // A stale session id is dropped.
    act(() => {
      result.current.markInventoryExhausted({ sessionId: "session-replaced", mediaFileId: 7 });
    });
    expect(result.current.inventoryFailed).toBe(false);
    expect(result.current.inventoryPending).toBe(true);

    // A stale file id is dropped too.
    act(() => {
      result.current.markInventoryExhausted({ sessionId: "session-guard", mediaFileId: 99 });
    });
    expect(result.current.inventoryFailed).toBe(false);
    expect(result.current.inventoryPending).toBe(true);

    // The live identity marks the exhausted budget failed.
    act(() => {
      result.current.markInventoryExhausted({ sessionId: "session-guard", mediaFileId: 7 });
    });
    expect(result.current.inventoryFailed).toBe(true);
    expect(result.current.inventoryPending).toBe(false);

    unmount();
  });

  it("drops an exhaustion for the old identity when a replacement is queued in the same batch", async () => {
    // A replacement push can be queued before React commits it, so the refs
    // still name the replaced file when the exhausted timer fires. The identity
    // check must run against the state the updater transitions — the queued
    // replacement — not those stale refs, or it would label the replacement's
    // provisional menu failed.
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-batch-exhaust",
            playback_plan: fixturePlanV3({
              session_id: "session-batch-exhaust",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-batch-exhaust/inventory",
              inventory_status: "declared",
              inventory_provenance: "pending",
              effective_media_file_id: 7,
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-batch-exhaust", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.inventoryPending).toBe(true);
    expect(result.current.audioInventoryProvisional).toBe(true);

    act(() => {
      // Queue the replacement file's declared inventory, then exhaust with the
      // replaced file's identity before React commits the replacement.
      result.current.applyCommittedSource(
        { effectiveMediaFileId: 8, inventoryStatus: "declared" },
        [{ codec: "eac3", channels: 6, layout: "5.1", language: "en", default: true }],
      );
      result.current.markInventoryExhausted({
        sessionId: "session-batch-exhaust",
        mediaFileId: 7,
      });
    });

    // The stale timer named file 7; the queued replacement is file 8 and stays
    // provisional, neither failed nor pending with no round scheduled.
    expect(result.current.mediaFileId).toBe(8);
    expect(result.current.inventoryFailed).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.inventoryPending).toBe(true);

    unmount();
  });

  it("marks a sub-less file failed while its subtitle inventory is still declared", async () => {
    // A declared plan with no subtitles: probe evidence for the audio menu can
    // land, but the empty subtitle list is still declared metadata. An exhausted
    // budget is then a discovery failure, because an unprobed or failed probe
    // also serves an empty list — only a verified-empty inventory is the
    // confident sub-less state.
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-sub-less",
            playback_plan: fixturePlanV3({
              session_id: "session-sub-less",
              inventory_status: "declared",
              effective_media_file_id: 7,
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
              subtitle: { mode: "off", inventory: [] },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-sub-less", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.subtitleInventoryProvisional).toBe(true);
    expect(result.current.subtitleUrls).toHaveLength(0);

    // The catalog poll supplies probe evidence for the audio menu only.
    act(() => {
      result.current.applyAudioInventory(
        [
          { codec: "eac3", channels: 2, layout: "stereo", language: "en", default: true },
          { codec: "eac3", channels: 2, layout: "stereo", language: "ja", default: false },
        ],
        7,
      );
    });

    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(true);

    act(() => {
      result.current.markInventoryExhausted();
    });

    // The confident audio list does not rescue the still-declared, empty
    // subtitle menu: the exhausted budget is the terminal failure.
    expect(result.current.inventoryFailed).toBe(true);
    expect(result.current.subtitleInventoryProvisional).toBe(true);
    expect(result.current.planAudioTracks).toHaveLength(2);

    unmount();
  });

  it("does not stick the failure badge when a replan is in flight at exhaustion", async () => {
    // The deferred poll budget can expire while a replan is still resolving.
    // The stale failure must not outlive the replan: its confident inventory is
    // adopted and the badge clears over it.
    const verifiedAudio = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "en", default: true },
    ];
    const verifiedSubtitles = [
      fixtureSubtitleInventoryItemV3({ track_id: "file:7:subtitle:0", combined_index: 0 }),
    ];
    let resolveReplan: ((response: Response) => void) | null = null;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3", "deferred_track_inventory_v1"],
            outcome: "playable",
            session_id: "session-replan-exhaust",
            playback_plan: fixturePlanV3({
              session_id: "session-replan-exhaust",
              tracks_pending: true,
              inventory_url: "/api/v2/playback/session-replan-exhaust/inventory",
              inventory_status: "declared",
              inventory_provenance: "pending",
              effective_media_file_id: 7,
              audio_tracks: [
                { codec: "aac", channels: 2, layout: "stereo", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/replan")) {
        return new Promise<Response>((resolve) => {
          resolveReplan = resolve;
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-replan-exhaust", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.subtitleInventoryProvisional).toBe(true);

    let pendingReplan: Promise<boolean> | undefined;
    await act(async () => {
      pendingReplan = result.current.reanchorSeek(120);
    });
    // The replan mock resolves its dynamic import before issuing the fetch, so
    // the pending response resolver is attached a microtask later.
    expect(resolveReplan).not.toBeNull();

    // The budget expires while the replan is still resolving.
    act(() => {
      result.current.markInventoryExhausted();
    });
    expect(result.current.inventoryFailed).toBe(true);

    await act(async () => {
      resolveReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-replan-exhaust",
          playback_plan: fixturePlanV3({
            session_id: "session-replan-exhaust",
            plan_id: "plan:replan-confident",
            plan_attempt_key: "v3:replan-confident",
            inventory_url: "/api/v2/playback/session-replan-exhaust/inventory",
            effective_media_file_id: 7,
            audio_tracks: verifiedAudio,
            subtitle: { mode: "off", inventory: verifiedSubtitles },
          }),
        }),
      );
      await pendingReplan;
    });

    // The replan's result is adopted and the stale failure badge does not stick.
    expect(result.current.inventoryFailed).toBe(false);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.planAudioTracks).toEqual(verifiedAudio);
    expect(result.current.subtitleUrls).toHaveLength(1);

    unmount();
  });

  it("upgrades a declared inventory with a shorter verified list", async () => {
    const declared = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    // The probe found only one real track; the declared second was synthesized.
    const verified = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "declared",
              audio_tracks: declared,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.audioInventoryProvisional).toBe(true);

    act(() => result.current.applyAudioInventory(verified));

    expect(result.current.planAudioTracks).toEqual(verified);
    expect(result.current.audioInventoryProvisional).toBe(false);
    unmount();
  });

  it("ignores a same-source declared commit after a verified plan", async () => {
    const verifiedTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const declaredTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "verified",
              inventory_provenance: "verified",
              audio_tracks: verifiedTracks,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.audioInventoryProvisional).toBe(false);

    // A same-source declared commit is a stale delivery after the probe
    // landed: it must leave the verified list and marker in place instead of
    // flapping the menus disabled/enabled.
    act(() =>
      result.current.applyCommittedSource(
        { effectiveMediaFileId: 7, effectiveVirtualUri: null, inventoryStatus: "declared" },
        declaredTracks,
      ),
    );

    expect(result.current.planAudioTracks).toEqual(verifiedTracks);
    expect(result.current.audioInventoryProvisional).toBe(false);
    unmount();
  });

  it("ignores a same-source declared subtitle inventory after a verified one", async () => {
    const verifiedSubtitles: SubtitleInventoryItemV3[] = [
      {
        track_id: "file:7:subtitle:0",
        combined_index: 0,
        source: "embedded",
        codec: "subrip",
        language: "eng",
        forced: false,
        default: false,
        hearing_impaired: false,
        delivery: "sidecar",
        url: "/stream/session-1/subtitles/0.vtt?file_id=7",
      },
    ];
    const declaredSubtitles: SubtitleInventoryItemV3[] = [
      ...verifiedSubtitles,
      {
        track_id: "file:7:subtitle:1",
        combined_index: 1,
        source: "embedded",
        codec: "subrip",
        language: "spa",
        forced: false,
        default: false,
        hearing_impaired: false,
        delivery: "sidecar",
        url: "/stream/session-1/subtitles/1.vtt?file_id=7",
      },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "verified",
              inventory_provenance: "verified",
              subtitle: { mode: "off", inventory: verifiedSubtitles },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.subtitleInventoryProvisional).toBe(false);

    // A stale same-source declared delivery must not flap the subtitle menu
    // back to provisional or overwrite the verified list.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:declared-after-verified",
        inventory_status: "declared",
        effective_media_file_id: 7,
        audio_tracks: [],
        subtitle_inventory: declaredSubtitles,
      }),
    );

    expect(result.current.subtitleUrls).toHaveLength(1);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    unmount();
  });

  it("adopts a shorter verified committed source over a longer declared inventory", async () => {
    const declared = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
      { codec: "ac3", channels: 6, layout: "5.1", language: "spa", default: false },
    ];
    const verified = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "declared",
              audio_tracks: declared,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.audioInventoryProvisional).toBe(true);

    act(() =>
      result.current.applyCommittedSource(
        { effectiveMediaFileId: 7, effectiveVirtualUri: null, inventoryStatus: "verified" },
        verified,
      ),
    );

    expect(result.current.planAudioTracks).toEqual(verified);
    expect(result.current.audioInventoryProvisional).toBe(false);
    unmount();
  });

  // The cold plan for a virgin virtual release carries zero tracks (fast
  // discovery timed out, the declared fallback is empty too) and commits before
  // the background probe materializes the candidate's own catalog row. The
  // verified push that follows is bound to the same candidate URI but names that
  // later row, so its `effective_media_file_id` differs from the plan's. It is
  // the server's own inventory for the live session, not another source: folding
  // it in place mounts both track menus (the audio button and the subtitle list)
  // without a replan, a row/file switch, or an element reload. Red before the
  // guard learned to admit a verified revision for the exact live candidate.
  it("mounts both menus from a verified push when the cold plan is empty", async () => {
    const candidateUri = "virtual://movie/x?result=A";
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "declared",
              inventory_provenance: "pending",
              effective_media_file_id: 7,
              effective_virtual_uri: candidateUri,
              audio_tracks: [],
              subtitle: { mode: "off", inventory: [] },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.planAudioTracks).toEqual([]);
    expect(result.current.subtitleUrls).toEqual([]);
    expect(result.current.audioInventoryProvisional).toBe(true);
    expect(result.current.subtitleInventoryProvisional).toBe(true);
    const planRevision = result.current.planRevision;
    const transportRevision = result.current.transportRevision;
    const streamUrl = result.current.streamUrl;

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:1",
        inventory_status: "verified",
        // The candidate's own catalog row, materialized after the cold plan
        // committed; the URI still names the live candidate.
        effective_media_file_id: 8,
        effective_virtual_uri: candidateUri,
        audio_tracks: [
          { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
          { codec: "ac3", channels: 2, layout: "stereo", language: "spa", default: false },
        ],
        subtitle_inventory: [
          {
            track_id: "file:8:subtitle:0",
            combined_index: 0,
            source: "embedded",
            codec: "subrip",
            language: "eng",
            forced: false,
            default: false,
            hearing_impaired: false,
            delivery: "sidecar",
            url: "/stream/session-1/subtitles/0.vtt?file_id=8",
          },
        ],
      }),
    );

    // Both menus are created from the verified push; the badge clears.
    expect(result.current.planAudioTracks).toHaveLength(2);
    expect(result.current.subtitleUrls).toHaveLength(1);
    expect(result.current.subtitleUrls[0]?.language).toBe("eng");
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    // Menu data only: the same session keeps its stream, transport and plan.
    expect(result.current.sessionId).toBe("session-1");
    expect(result.current.planRevision).toBe(planRevision);
    expect(result.current.transportRevision).toBe(transportRevision);
    expect(result.current.streamUrl).toBe(streamUrl);
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes("/replan"))).toHaveLength(0);
    unmount();
  });

  // The same cold-empty plan, but the verified push arrives while the
  // default-audio reconciliation replan is in flight. The flush must still mount
  // the menus from it: the push names the live candidate's URI, so its differing
  // catalog row is the resolved identity, not a sibling. Red before the flush
  // admitted a verified revision for the exact live candidate.
  it("mounts both menus from a verified push deferred by a replan", async () => {
    const candidateUri = "virtual://movie/x?result=A";
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              inventory_status: "declared",
              inventory_provenance: "pending",
              effective_media_file_id: 7,
              effective_virtual_uri: candidateUri,
              audio_tracks: [],
              subtitle: { mode: "off", inventory: [] },
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    const transportRevision = result.current.transportRevision;

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:deferred-empty",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: candidateUri,
        audio_tracks: [
          { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
          { codec: "ac3", channels: 2, layout: "stereo", language: "spa", default: false },
        ],
        subtitle_inventory: [
          {
            track_id: "file:8:subtitle:0",
            combined_index: 0,
            source: "embedded",
            codec: "subrip",
            language: "eng",
            forced: false,
            default: false,
            hearing_impaired: false,
            delivery: "sidecar",
            url: "/stream/session-1/subtitles/0.vtt?file_id=8",
          },
        ],
      }),
    );

    // The reconciliation replan settles on the same candidate with the stale
    // (empty) declared list; the deferred verified push then mounts the menus.
    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:deferred-empty",
            plan_attempt_key: "v3:deferred-empty",
            inventory_status: "declared",
            inventory_provenance: "pending",
            effective_media_file_id: 7,
            effective_virtual_uri: candidateUri,
            audio_tracks: [],
            subtitle: { mode: "off", inventory: [] },
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:deferred-empty"));
    expect(result.current.planAudioTracks).toHaveLength(2);
    expect(result.current.subtitleUrls).toHaveLength(1);
    expect(result.current.audioInventoryProvisional).toBe(false);
    expect(result.current.subtitleInventoryProvisional).toBe(false);
    expect(result.current.transportRevision).toBe(transportRevision);
    expect(result.current.sessionId).toBe("session-1");
    unmount();
  });
});

describe("usePlaybackSession effective virtual source", () => {
  it("exposes the plan's effective_virtual_uri on adoption", async () => {
    const effectiveVirtualUri = "/media/Movies/Example (2024)/Example.1080p.mkv";
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 100,
              effective_virtual_uri: effectiveVirtualUri,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    // The collapsed id still names the requested frame; the path names the
    // concrete candidate the menus should adopt. It is menu data only.
    expect(result.current.mediaFileId).toBe(100);
    expect(result.current.effectiveVirtualUri).toBe(effectiveVirtualUri);
    unmount();
  });

  it("defaults effectiveVirtualUri to null when the plan omits it", async () => {
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
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(result.current.effectiveVirtualUri).toBeNull();
    unmount();
  });
});

describe("usePlaybackSession retryable terminals", () => {
  it("retries the same file with force_relink and coalesces a double-tap", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    let startCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startCount += 1;
        startBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        if (startCount === 1) {
          return jsonResponse(
            {
              protocol_version: 3,
              server_features: ["playback_plan_v3"],
              outcome: "terminal",
              terminal: {
                reason: "virtual_source_unavailable",
                message: "The virtual source could not be resolved for playback.",
                retryable: true,
              },
            },
            { status: 201 },
          );
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
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );

    await waitFor(() => expect(result.current.error).toBeTruthy());
    expect(result.current.errorReason).toBe("virtual_source_unavailable");
    expect(result.current.errorRetryable).toBe(true);
    expect(result.current.retrying).toBe(false);

    // A second tap while the retry is in flight must not issue a second start.
    act(() => {
      result.current.retryStart();
      result.current.retryStart();
    });

    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(startBodies).toHaveLength(2);
    expect(startBodies[0]?.file_id).toBe(7);
    expect(startBodies[1]).toMatchObject({ file_id: 7, force_relink: true });
    expect(startBodies[1]?.playback_attempt_id).not.toBe(startBodies[0]?.playback_attempt_id);
    expect(result.current.retrying).toBe(false);
    expect(result.current.error).toBeNull();
    unmount();
  });

  it("keeps a retryable terminal off the screen and retryable after a failed retry", async () => {
    let startCount = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startCount += 1;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "terminal",
            terminal: {
              reason: "virtual_source_unavailable",
              message:
                startCount === 1
                  ? "The virtual source could not be resolved for playback."
                  : "The virtual source could not be refreshed for playback.",
              retryable: true,
            },
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );

    await waitFor(() => expect(result.current.error).toBeTruthy());

    act(() => result.current.retryStart());

    await waitFor(() =>
      expect(result.current.error).toBe("The virtual source could not be refreshed for playback."),
    );
    expect(result.current.errorReason).toBe("virtual_source_unavailable");
    expect(result.current.errorRetryable).toBe(true);
    expect(result.current.plan).toBeNull();
    expect(result.current.retrying).toBe(false);
    expect(startCount).toBe(2);
    unmount();
  });
});

describe("usePlaybackSession deferred identity pushes", () => {
  it("re-keys the live identity when a poll resolves another effective source", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              audio_tracks: [
                { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    const transportRevision = result.current.transportRevision;

    // A poll resolved the effective virtual candidate while the plan still
    // names the collapsed row. The inventory and the version identity must
    // move together.
    act(() =>
      result.current.applyAudioInventory(
        [{ codec: "ac3", channels: 2, layout: "stereo", language: "spa", default: true }],
        8,
        "virtual://movie/x?result=B",
      ),
    );

    expect(result.current.mediaFileId).toBe(8);
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual([
      { codec: "ac3", channels: 2, layout: "stereo", language: "spa", default: true },
    ]);
    expect(result.current.audioInventoryProvisional).toBe(false);
    // Menu data only: the transport must not reload.
    expect(result.current.transportRevision).toBe(transportRevision);
    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/playback/start")),
    ).toHaveLength(1);
    unmount();
  });

  it("preserves the live virtual URI when an ordinary version poll moves by id alone", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: [
                { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
              ],
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() =>
      result.current.applyAudioInventory(
        [{ codec: "ac3", channels: 6, layout: "5.1", language: "fra", default: true }],
        8,
      ),
    );

    // The id moved but no candidate was named, so the rotation's effective URI
    // must not be cleared under the new file.
    expect(result.current.mediaFileId).toBe(8);
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=A");
    unmount();
  });

  it("applies a source commit deferred by a version switch once it settles", async () => {
    const planAudioTracks = [
      { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
    ];
    const rotatedTracks = [
      { codec: "ac3", channels: 2, layout: "stereo", language: "deu", default: true },
    ];
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) {
          // Hold the switch open so the push lands while it rebuilds.
          return switchResponse;
        }
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: planAudioTracks,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    // Switch to the very file the rotation will name. The v2 wire omits the
    // candidate URI, so only the deferred push can re-key the version menu.
    act(() => result.current.switchVersion(8, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));
    expect(result.current.replacing).toBe(true);

    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "verified",
        },
        rotatedTracks,
      ),
    );
    // Under the pending switch the menus stay on the outgoing identity.
    expect(result.current.mediaFileId).toBe(7);
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=A");

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:switch-2",
            plan_attempt_key: "v3:switch-2",
            requested_media_file_id: 8,
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    // The replacement plan names file 8, so the deferred commit is the
    // carrier that re-keys the candidate URI and its declared inventory.
    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedTracks);
    unmount();
  });

  it("drops a source commit deferred by a switch when it names another file", async () => {
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) return switchResponse;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.switchVersion(8, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));
    expect(result.current.replacing).toBe(true);

    // A stale rotation names file 9, which the switch never lands on. It must
    // not be replayed against the replacement plan.
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 9,
          effectiveVirtualUri: "virtual://movie/x?result=C",
          inventoryStatus: "verified",
        },
        [{ codec: "ac3", channels: 2, layout: "stereo", language: "ita", default: true }],
      ),
    );

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:switch-2",
            plan_attempt_key: "v3:switch-2",
            requested_media_file_id: 8,
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    expect(result.current.effectiveVirtualUri).toBeNull();
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });
});

describe("usePlaybackSession deferred push authority", () => {
  const outgoingAudio = [
    { codec: "eac3", channels: 6, layout: "5.1", language: "eng", default: true },
  ];
  const rotatedAudio = [
    { codec: "ac3", channels: 2, layout: "stereo", language: "deu", default: true },
  ];
  const collisionAudio = [
    { codec: "ac3", channels: 2, layout: "stereo", language: "fra", default: true },
  ];
  // Two entries so a fold that lands is observable even when the incumbent menu
  // is non-empty and no identity move is involved.
  const richerCollisionAudio = [
    { codec: "ac3", channels: 2, layout: "stereo", language: "fra", default: true },
    { codec: "ac3", channels: 2, layout: "stereo", language: "ita", default: false },
  ];

  it("rejects an outgoing source commit even when it matches the outgoing rendered file", async () => {
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) return switchResponse;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.switchVersion(8, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));

    // The push names the OUTGOING source (file 7, candidate A). The rendered
    // state still says file 7 at a settle path, so matching it there would let
    // this stale carrier survive; it must be rejected against the settled plan.
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 7,
          effectiveVirtualUri: "virtual://movie/x?result=A",
          inventoryStatus: "verified",
        },
        rotatedAudio,
      ),
    );

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:switch-2",
            plan_attempt_key: "v3:switch-2",
            requested_media_file_id: 8,
            effective_media_file_id: 8,
            effective_virtual_uri: "virtual://movie/x?result=B",
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    // The outgoing release's tracks must not be folded under the settled plan.
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });

  it("rejects a same-file candidate collision the settled plan cannot vouch for", async () => {
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 8,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 8, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    // Candidate C rides the same collapsed file id (8) as the settled plan's B.
    // File equality alone would accept it; the plan names B, so C is stale. It
    // arrives as an inventory revision, the path that defers under a replan.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:collision",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=C",
        audio_tracks: collisionAudio,
      }),
    );

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:collision0001",
            plan_attempt_key: "v3:collision0001",
            effective_media_file_id: 8,
            effective_virtual_uri: "virtual://movie/x?result=B",
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() =>
      expect(result.current.plan?.effective_virtual_uri).toBe("virtual://movie/x?result=B"),
    );
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });

  it("replays an older inventory before a newer source commit in arrival order", async () => {
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) return switchResponse;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.switchVersion(8, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));

    // Older inventory revision for candidate C arrives first, then the newer
    // source commit for candidate B. The settled plan names file 8 with no
    // candidate URI, so both pass the identity gate; only arrival order decides
    // which one wins, and the newer source must.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:older",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=C",
        audio_tracks: collisionAudio,
      }),
    );
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "verified",
        },
        rotatedAudio,
      ),
    );

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:switch-order",
            plan_attempt_key: "v3:switch-order",
            requested_media_file_id: 8,
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    // The newer source commit wins; the older inventory must not overwrite it.
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("folds an inventory revision deferred by a replan once the replacement plan adopts it", async () => {
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    // The push names the replacement plan's incoming identity, which is only
    // knowable once the plan settles; it must be held, not dropped.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:deferred",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=B",
        audio_tracks: rotatedAudio,
      }),
    );

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:deferred00001",
            plan_attempt_key: "v3:deferred00001",
            effective_media_file_id: 8,
            effective_virtual_uri: "virtual://movie/x?result=B",
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("clears a deferred push when a new request retires the session", async () => {
    const startBodies: Array<{ file_id: number }> = [];
    const switchResponse = new Promise<Response>(() => {
      // Hold the pending switch open forever; the new request retires it before
      // its replacement ever lands.
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) return switchResponse;
        if (startBodies.length === 3) {
          return jsonResponse(
            {
              protocol_version: 3,
              server_features: ["playback_plan_v3"],
              outcome: "playable",
              session_id: "session-3",
              playback_plan: fixturePlanV3({
                session_id: "session-3",
                plan_id: "plan:episode-two",
                plan_attempt_key: "v3:episode-two",
                effective_media_file_id: 9,
                effective_virtual_uri: "virtual://movie/x?result=D",
                audio_tracks: [],
              }),
            },
            { status: 201 },
          );
        }
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, rerender, unmount } = renderHook(
      ({ requestKey, fileId }: { requestKey: string; fileId: number }) =>
        usePlaybackSession(requestKey, [], [], fileId, 0, false, "auto"),
      { wrapper, initialProps: { requestKey: "episode-1", fileId: 7 } },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.switchVersion(8, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));

    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "verified",
        },
        rotatedAudio,
      ),
    );

    // A new request retires the pending switch; its deferred push must not leak
    // into the replacement session.
    rerender({ requestKey: "episode-2", fileId: 9 });

    await waitFor(() => expect(result.current.mediaFileId).toBe(9));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=D");
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });

  it("drops a same-file sibling inventory after the live source already rotated", async () => {
    // The plan owns file 8 without naming a candidate (v2 wire). A poll moves
    // the live source to candidate A. During a replacement that also settles on
    // file 8 without a candidate, a C inventory is queued. C is a same-file
    // sibling of the live outgoing A: the URI-less plan cannot vouch for it and
    // the concrete live URI contradicts it, so it is a genuine collision and is
    // dropped. (A same-file push that agrees with the live source is admitted;
    // see the URI-less acceptance regression below.)
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) return switchResponse;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 8,
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 8, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    // A poll resolves the live source to candidate A under the same file.
    act(() => result.current.applyAudioInventory(collisionAudio, 8, "virtual://movie/x?result=A"));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=A");

    act(() => result.current.switchVersion(9, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));

    // Candidate C on the same file is deferred behind the pending replacement.
    // It contradicts the live outgoing A, so the URI-less winner cannot vouch
    // for it and it must not be folded.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:sibling-c",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=C",
        audio_tracks: outgoingAudio,
      }),
    );
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=A");

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:live-a-2",
            plan_attempt_key: "v3:live-a-2",
            requested_media_file_id: 9,
            // The replacement falls back onto the same file 8, URI-less.
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    await waitFor(() => expect(result.current.sessionId).toBe("session-2"));
    // Candidate C is a same-file sibling of the live outgoing A, so the
    // URI-less winner cannot vouch for it and it is dropped; the plan's own
    // (empty, candidate-less) identity is what stands.
    expect(result.current.effectiveVirtualUri).toBeNull();
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });

  it("admits a same-file inventory push against a URI-less settled plan", async () => {
    // The v2 start wire omits the candidate URI, so the settled replacement plan
    // names only file 8. The verified inventory push names that same file with
    // the candidate the plan leaves out; it is the only carrier of the live
    // identity and must be folded rather than refused for the plan's silence.
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) return switchResponse;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.switchVersion(8, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));

    // A verified inventory for candidate B on the replacement's file 8 is
    // deferred behind the pending switch.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:uri-less",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=B",
        audio_tracks: rotatedAudio,
      }),
    );

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:uri-less-2",
            plan_attempt_key: "v3:uri-less-2",
            requested_media_file_id: 8,
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    // The push names the plan's own file, so it is folded; the plan itself
    // carries no candidate URI.
    expect(result.current.plan?.effective_virtual_uri).toBeUndefined();
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("holds an older inventory behind a newer source commit during a replan", async () => {
    // A replanning session defers an inventory for candidate C, then a newer
    // source commit for candidate B arrives while the same replan is still in
    // flight. Both must observe the adoption barrier and flush in arrival
    // order against the URI-less winning plan, so B wins.
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    // Older inventory for C, then the newer source commit for B. The replan
    // response settles on file 8 with no candidate URI.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:older",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=C",
        audio_tracks: collisionAudio,
      }),
    );
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "verified",
        },
        rotatedAudio,
      ),
    );
    // Both are held while the replan owns the session.
    expect(result.current.mediaFileId).toBe(7);

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:order000000001",
            plan_attempt_key: "v3:order000000001",
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    // The URI-less plan cannot distinguish B from C, so only the newest
    // rotation (B) may be the carrier; the older inventory (C) must not win.
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("folds a source commit deferred by a replan when the plan keeps the file", async () => {
    // Exercises the source-commit path (not the inventory path): a rotation to
    // a new candidate on the same file lands inside a replan, and the settled
    // URI-less plan still names that file, so the commit is admitted.
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    // A rotation moves the transport to candidate B on file 8 while the replan
    // is in flight; the response settles on the same file with no candidate.
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "verified",
        },
        rotatedAudio,
      ),
    );
    expect(result.current.mediaFileId).toBe(7);

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:commit-replan01",
            plan_attempt_key: "v3:commit-replan01",
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    // The winning plan names file 8; the deferred commit is the carrier that
    // re-keys the candidate to B and its inventory.
    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("keeps a deferred push whose identity the winning plan retains", async () => {
    // The replacement retains the effective candidate the session was already
    // on. The winner explicitly names it, so the deferred probe update for that
    // candidate is kept rather than rejected as an outgoing carrier.
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) return switchResponse;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.switchVersion(8, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));

    // A probe update names the very candidate the replacement will retain.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:retained",
        inventory_status: "verified",
        effective_media_file_id: 7,
        effective_virtual_uri: "virtual://movie/x?result=A",
        audio_tracks: rotatedAudio,
      }),
    );

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:retained-2",
            plan_attempt_key: "v3:retained-2",
            requested_media_file_id: 8,
            // The replacement falls back onto the same candidate A, file 7.
            effective_media_file_id: 7,
            effective_virtual_uri: "virtual://movie/x?result=A",
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(7));
    // The winner names candidate A, so the corroborating verified update is
    // applied rather than discarded as the outgoing source.
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=A");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("preserves a legitimate rotation when a URI-ful original plan is refused", async () => {
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const replanBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) {
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return heldReplan;
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    // The transport rotates to file 8 candidate B while the replan is in
    // flight. The plan being replanned names file 7 candidate A, so when the
    // replan is refused the original URI-ful plan is still the live plan and
    // must not veto the rotation the transport already committed to.
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "verified",
        },
        rotatedAudio,
      ),
    );
    expect(result.current.mediaFileId).toBe(7);

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
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
      await heldReplan;
    });

    // The plan was refused, not replaced, so it still names file 7 candidate A.
    // The rotation to B must survive: the refusal is not evidence that the
    // transport stayed on A.
    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    expect(result.current.plan?.plan_id).toBe("plan:0123456789abcdef");
    expect(replanBodies).toHaveLength(1);
    unmount();
  });

  it("uses the live rotation as the outgoing baseline before the next render", async () => {
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    // No render between the poll rotation and the deferred push: the menus move
    // to file 8 candidate B, then a source commit for the same file's other
    // candidate C is held behind the replan. The next transition must read the
    // rotation, not the pre-rotation rendered state; otherwise the URI-less
    // winner would misread C as a cross-file rotation off file 7 and admit it.
    act(() => {
      result.current.applyAudioInventory(rotatedAudio, 8, "virtual://movie/x?result=B");
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=C",
          inventoryStatus: "verified",
        },
        collisionAudio,
      );
    });

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:no-render001",
            plan_attempt_key: "v3:no-render001",
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    // The winner is URI-less on file 8 and the live rotation is already B, so C
    // shares the live file and cannot be proven. It must not be folded.
    expect(result.current.effectiveVirtualUri).toBeNull();
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });

  it("refolds a refused inventory revision once a later rotation matches it", async () => {
    // A revision refused by a flush is not the same statement as a revision that
    // was applied: the refusal is about the settled plan at that moment, and the
    // live source can still move onto the candidate the revision names later in
    // the same generation. Recording the refusal would swallow that redelivery
    // and leave the track menus stale for the rest of the session.
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 8,
              effective_virtual_uri: "virtual://movie/x?result=B",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 8, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    // A revision for candidate C is held behind the replan. The settled
    // replacement names candidate B, so C is a same-file collision and is
    // refused rather than folded.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:redelivered",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=C",
        audio_tracks: collisionAudio,
      }),
    );

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:redeliver0001",
            plan_attempt_key: "v3:redeliver0001",
            effective_media_file_id: 8,
            effective_virtual_uri: "virtual://movie/x?result=B",
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:redeliver0001"));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual([]);

    // A rotation moves the live source onto C with an empty inventory. The
    // revision the flush refused is still unrecorded, so the redelivery now
    // matches the live identity and must fold.
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=C",
          inventoryStatus: "verified",
        },
        [],
      ),
    );
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=C");
    expect(result.current.planAudioTracks).toEqual([]);

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:redelivered",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=C",
        audio_tracks: collisionAudio,
      }),
    );
    expect(result.current.planAudioTracks).toEqual(collisionAudio);

    // Now that the revision has genuinely been applied, its duplicate is
    // suppressed: a redelivery with a different inventory must not overwrite.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:redelivered",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=C",
        audio_tracks: rotatedAudio,
      }),
    );
    expect(result.current.planAudioTracks).toEqual(collisionAudio);
    unmount();
  });

  it("admits a deferred URI-only inventory push against a URI-less settled plan", async () => {
    // A partial push may name only the candidate URI and no file id. It is
    // deferred behind a replan whose replacement plan is URI-less (v2 wire) and
    // the live source is URI-less too, so the URI is the only key the push
    // offers. It must be folded rather than dropped for the missing file.
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 8,
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 8, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:uri-only",
        inventory_status: "verified",
        effective_virtual_uri: "virtual://movie/x?result=B",
        audio_tracks: rotatedAudio,
      }),
    );

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:uri-only0001",
            plan_attempt_key: "v3:uri-only0001",
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:uri-only0001"));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("drops a file-only inventory under a concrete live candidate on the same file", async () => {
    // The plan is URI-less on file 8 and a poll has resolved the live source to
    // candidate A. A later file-only inventory revision names file 8 but no
    // candidate, so it cannot be tied to A; folding it would overwrite the
    // poll's proven inventory with an unproven one.
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 8,
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 8, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.applyAudioInventory(rotatedAudio, 8, "virtual://movie/x?result=A"));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=A");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:file-only",
        inventory_status: "verified",
        effective_media_file_id: 8,
        audio_tracks: richerCollisionAudio,
      }),
    );

    // The concrete live candidate A stands; the file-only revision is refused
    // even though its richer list would otherwise replace the menu.
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=A");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("drops a file-only inventory against a URI-ful settled plan via the direct path", async () => {
    // The settled plan names candidate B by URI. A file-only inventory revision
    // (the shape a refused-then-redelivered revision takes) names the plan's file
    // but no candidate, so the direct path must not fold it over B's inventory.
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 8,
              effective_virtual_uri: "virtual://movie/x?result=B",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 8, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:file-only-uri-ful",
        inventory_status: "verified",
        effective_media_file_id: 8,
        audio_tracks: richerCollisionAudio,
      }),
    );

    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(outgoingAudio);
    unmount();
  });

  it("refuses a file-only inventory deferred before a poll folded a same-file candidate", async () => {
    // A FILE-ONLY inventory revision is queued behind a replan. Its captured
    // outgoing identity is URI-less, so the arrival-time baseline alone would
    // admit it. While it waits, a poll (which bypasses the adoption barrier)
    // folds candidate B on the same file. The flush must judge the queued push
    // against the candidate the menus now carry, not only the arrival capture:
    // otherwise the unproven file-only revision overwrites B's proven inventory.
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 8,
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 8, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    // The file-only inventory is held; its captured outgoing identity is
    // URI-less, and the richer list would replace a one-entry menu.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:file-only-deferred",
        inventory_status: "verified",
        effective_media_file_id: 8,
        audio_tracks: richerCollisionAudio,
      }),
    );
    // The poll folds candidate B under the same file while the push is queued.
    act(() => result.current.applyAudioInventory(rotatedAudio, 8, "virtual://movie/x?result=B"));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
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
      await heldReplan;
    });

    // The plan was refused, so it still stands URI-less on file 8; the live source
    // is the folded B. The file-only revision must not overwrite B's inventory.
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("refuses a file-only inventory deferred while a concrete candidate is live", async () => {
    // A poll moves the live source to candidate B on file 8, then a switch starts
    // and a file-only inventory revision for file 8 is deferred. The replacement
    // settles URI-less on file 8, so file equality alone would admit it; the
    // concrete arrival-time live candidate B must refuse it instead.
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) return switchResponse;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    // The poll folds candidate B on file 8 before the switch barrier opens.
    act(() => result.current.applyAudioInventory(rotatedAudio, 8, "virtual://movie/x?result=B"));
    act(() => result.current.switchVersion(9, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:file-only-live",
        inventory_status: "verified",
        effective_media_file_id: 8,
        audio_tracks: richerCollisionAudio,
      }),
    );

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:file-only-live2",
            plan_attempt_key: "v3:file-only-live2",
            requested_media_file_id: 9,
            // The replacement falls back onto file 8, URI-less.
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    await waitFor(() => expect(result.current.sessionId).toBe("session-2"));
    // The replacement plan is URI-less on file 8, and the file-only revision is
    // refused against the concrete candidate that was live when it arrived, so
    // its richer track list is not folded and the plan's own (empty) identity
    // and inventory stand.
    expect(result.current.effectiveVirtualUri).toBeNull();
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });

  it("keeps the newer of two same-file inventories replayed in one flush", async () => {
    // Two concrete inventory revisions for the same file are queued behind a
    // replan: C first, then B. The settled plan is URI-less on file 8, so both
    // pass the identity gate and arrival order must decide — B, sent later, wins.
    // If the flush judged each entry against the menu identity an earlier entry
    // had already moved, C would veto B and invert arrival order.
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:order-c",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=C",
        audio_tracks: collisionAudio,
      }),
    );
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:order-b",
        inventory_status: "verified",
        effective_media_file_id: 8,
        effective_virtual_uri: "virtual://movie/x?result=B",
        audio_tracks: rotatedAudio,
      }),
    );

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:order-b0000001",
            plan_attempt_key: "v3:order-b0000001",
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    // The later inventory (B) wins; the earlier one (C) must not veto it.
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("admits a queued push naming the replaced plan's explicitly winning candidate", async () => {
    // The session was on (file 7, candidate A) and the viewer explicitly picked
    // a row the server resolved to the same file's candidate B. A probe push for
    // B is queued behind the switch. The outgoing candidate A must not veto the
    // plan the server just selected: a replaced URI-ful winner is decided by
    // exact identity before any ambiguity guard.
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) return switchResponse;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
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

    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 7,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "verified",
        },
        rotatedAudio,
      ),
    );
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=A");

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:retain-b000001",
            plan_attempt_key: "v3:retain-b000001",
            requested_media_file_id: 99,
            effective_media_file_id: 7,
            effective_virtual_uri: "virtual://movie/x?result=B",
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    await waitFor(() =>
      expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B"),
    );
    // The winner names candidate B outright, so its corroborating inventory is
    // folded rather than vetoed by the outgoing candidate A.
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("keeps the newer same-file source commit over an earlier deferred source commit", async () => {
    // The transport commits to candidate C, then to candidate B, on the same
    // file while a replan is in flight. Both are source commits. The settled
    // winner is URI-less, so arrival order decides: the queue's own earlier
    // commit is not an external authority the newer one must answer to.
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=C",
          inventoryStatus: "verified",
        },
        collisionAudio,
      ),
    );
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=B",
          inventoryStatus: "verified",
        },
        rotatedAudio,
      ),
    );

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:source-order01",
            plan_attempt_key: "v3:source-order01",
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() => expect(result.current.mediaFileId).toBe(8));
    // The newer commit (B) must remain the final identity and inventory; the
    // earlier same-file commit (C) is not an authority that can veto it.
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("does not fold a URI-only inventory for the outgoing file onto a replaced file", async () => {
    // The plan owns file 7 with no candidate URI. A URI-only inventory push for
    // its candidate is queued during a switch, and the replacement settles on a
    // different file 8, also URI-less. The push proves no relation to file 8, so
    // it must not inherit the replacement's file id.
    const startBodies: Array<{ file_id: number }> = [];
    let releaseSwitch: ((response: Response) => void) | undefined;
    const switchResponse = new Promise<Response>((resolve) => {
      releaseSwitch = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        const body = JSON.parse(String(init?.body)) as { file_id: number };
        startBodies.push(body);
        if (startBodies.length === 2) return switchResponse;
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.switchVersion(8, 0));
    await waitFor(() => expect(startBodies).toHaveLength(2));

    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:uri-only-outgoing",
        inventory_status: "verified",
        effective_virtual_uri: "virtual://movie/x?result=C",
        audio_tracks: rotatedAudio,
      }),
    );

    await act(async () => {
      releaseSwitch?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-2",
          playback_plan: fixturePlanV3({
            session_id: "session-2",
            plan_id: "plan:cross-file-01",
            plan_attempt_key: "v3:cross-file-01",
            requested_media_file_id: 8,
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await switchResponse;
    });

    await waitFor(() => expect(result.current.sessionId).toBe("session-2"));
    // No proven relation to the settled file, so the push is refused and a
    // current-source refresh must re-establish the candidate instead.
    expect(result.current.effectiveVirtualUri).toBeNull();
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });

  it("does not let a source commit overwrite a poll folded after it was queued", async () => {
    // The review's ordering: a URI-bearing source commit queued while a replan
    // owns the session names file 8 candidate C. Before the replan settles, a
    // catalog poll (which bypasses the adoption barrier) folds the newer candidate
    // B on the same file. The replacement plan then lands URI-less on file 8,
    // overwriting the menu mirror with the plan's own candidate-less identity, so
    // `appliedIdentity` alone no longer names B. The commit's arrival outgoing is
    // cross-file (file 7 candidate A), so an arrival-order-only judgement against
    // the queue would admit C and install it; the poll candidate remembered since
    // the fold must still veto the stale commit.
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    // The transport's older commit for candidate C is held behind the replan.
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=C",
          inventoryStatus: "verified",
        },
        collisionAudio,
      ),
    );
    // A poll then folds the newer candidate B on the same file; the menus move to
    // B while the commit still waits.
    act(() => result.current.applyAudioInventory(rotatedAudio, 8, "virtual://movie/x?result=B"));
    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:poll-stale01",
            plan_attempt_key: "v3:poll-stale01",
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    // The replacement plan is URI-less on file 8 and cannot vouch for C, and the
    // poll's B was folded after C was queued. The stale commit must not install C
    // over the newer poll; the plan's own candidate-less identity stands.
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:poll-stale01"));
    expect(result.current.effectiveVirtualUri).toBeNull();
    expect(result.current.planAudioTracks).toEqual([]);
    unmount();
  });

  it("keeps a poll's candidate over a source commit queued after it", async () => {
    // The mirror of the previous ordering: the poll folds candidate B first, so
    // that candidate is the commit's arrival outgoing baseline. A later sibling
    // commit for candidate C on the same file is a genuine same-file collision and
    // is refused, whether or not it is treated as newer than an applied poll.
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 7,
              effective_virtual_uri: "virtual://movie/x?result=A",
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    act(() => result.current.applyAudioInventory(rotatedAudio, 8, "virtual://movie/x?result=B"));
    // The commit for C arrives after the poll; its captured outgoing is B.
    act(() =>
      result.current.applyCommittedSource(
        {
          effectiveMediaFileId: 8,
          effectiveVirtualUri: "virtual://movie/x?result=C",
          inventoryStatus: "verified",
        },
        collisionAudio,
      ),
    );

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
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
      await heldReplan;
    });

    expect(result.current.effectiveVirtualUri).toBe("virtual://movie/x?result=B");
    expect(result.current.planAudioTracks).toEqual(rotatedAudio);
    unmount();
  });

  it("refuses a deferred push with neither URI nor file against a URI-less winner", async () => {
    // A malformed or partial push may name no source at all. It cannot be tied to
    // the settled adoption, so it must not fold its audio list over a URI-less
    // winner that the wire already names by file alone.
    let releaseReplan: ((response: Response) => void) | undefined;
    const heldReplan = new Promise<Response>((resolve) => {
      releaseReplan = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        return jsonResponse(
          {
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              effective_media_file_id: 8,
              audio_tracks: outgoingAudio,
            }),
          },
          { status: 201 },
        );
      }
      if (url.endsWith("/playback/session-1/replan")) return heldReplan;
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 8, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      void result.current.refreshSubtitles(120);
    });
    await waitFor(() => expect(result.current.replanning).toBe(true));

    // No file id and no URI: an identity-less push that cannot be tied to any
    // adoption. Its richer list must not replace the URI-less winner's menu.
    act(() =>
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:bogus-identity",
        inventory_status: "verified",
        audio_tracks: richerCollisionAudio,
      }),
    );

    await act(async () => {
      releaseReplan?.(
        jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: "session-1",
          playback_plan: fixturePlanV3({
            plan_id: "plan:bogus-identity",
            plan_attempt_key: "v3:bogus-identity",
            effective_media_file_id: 8,
            audio_tracks: [],
          }),
        }),
      );
      await heldReplan;
    });

    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:bogus-identity"));
    expect(result.current.effectiveVirtualUri).toBeNull();
    expect(result.current.planAudioTracks).toEqual([]);
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

  it("enters the lost state when the server says the session is gone and nothing played", async () => {
    const harness = reconnectHarness({ replan: () => unreachable() });
    const rendered = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(rendered.result.current.planRevision).toBe(1));

    // A session-scoped call (ticket mint, progress) answered 404 before the
    // first frame: there is no played stream for the reconnect to reposition,
    // so the player goes straight to the terminal state.
    act(() => rendered.result.current.reportSessionLost());

    expect(rendered.result.current.connectionStatus).toBe("lost");
    expect(rendered.result.current.connectionErrorTitle).toBe("Connection lost");
    expect(harness.replanBodies).toHaveLength(0);
    expect(harness.startBodies).toHaveLength(1);

    rendered.unmount();
  });

  it("retries a pre-first-frame loss from the position the start asked for", async () => {
    const harness = reconnectHarness({
      replan: () =>
        jsonResponse(
          { error: "playback_session_not_found", message: "Playback session not found" },
          { status: 404 },
        ),
      restart: () => jsonResponse(playable("session-2"), { status: 201 }),
    });
    const rendered = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 240, true, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(rendered.result.current.planRevision).toBe(1));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });

    // The session is answered gone before the first frame: no stream played, so
    // no reconnect cycle recorded where the viewer asked to be.
    act(() => rendered.result.current.reportSessionLost());
    expect(rendered.result.current.connectionStatus).toBe("lost");

    // Retry must rebuild at the start's own position, not zero.
    act(() => rendered.result.current.retryConnection());
    await advance(0);

    const restart = harness.startBodies[1];
    expect(restart?.start_position).toBe(240);
    expect(rendered.result.current.sessionId).toBe("session-2");
    expect(rendered.result.current.connectionStatus).toBe("connected");

    rendered.unmount();
  });

  it("retries a pre-first-frame loss from the server-resolved resume position", async () => {
    // The caller asked for automatic resume (position zero); the server
    // resolved where it actually starts.
    const resumedPlan = fixturePlanV3({ session_id: "session-1" });
    resumedPlan.timeline = {
      ...resumedPlan.timeline,
      source_start_seconds: 275,
      player_start_seconds: 5,
      timeline_offset_seconds: 270,
    };
    const harness = reconnectHarness({
      initialPlan: resumedPlan,
      replan: () =>
        jsonResponse(
          { error: "playback_session_not_found", message: "Playback session not found" },
          { status: 404 },
        ),
      restart: () => jsonResponse(playable("session-2"), { status: 201 }),
    });
    const rendered = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(rendered.result.current.planRevision).toBe(1));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });

    // The session is answered gone before the first frame, so no reconnect
    // cycle recorded the server-resolved resume.
    act(() => rendered.result.current.reportSessionLost());
    expect(rendered.result.current.connectionStatus).toBe("lost");

    // Retry must rebuild at the adopted plan's position, not the request zero.
    act(() => rendered.result.current.retryConnection());
    await advance(0);

    const restart = harness.startBodies[1];
    expect(restart?.start_position).toBe(275);
    expect(rendered.result.current.sessionId).toBe("session-2");
    expect(rendered.result.current.connectionStatus).toBe("connected");
    expect(rendered.result.current.shouldAutoPlay).toBe(true);

    rendered.unmount();
  });

  it("keeps the adopted-plan discriminator across a replacement in the same request", async () => {
    // The first plan resolved a server position; a version switch then replaces
    // it while the request stays the same. The adopted-plan flag is not reset
    // by that replacement, so a pre-first-frame loss still retries at the
    // replaced plan's position instead of the request's own.
    const firstPlan = fixturePlanV3({ session_id: "session-1" });
    firstPlan.timeline = {
      ...firstPlan.timeline,
      source_start_seconds: 275,
      player_start_seconds: 5,
      timeline_offset_seconds: 270,
    };
    const switchedPlan = fixturePlanV3({
      session_id: "session-1",
      plan_id: "plan:switched00000000001",
      requested_media_file_id: 99,
      effective_media_file_id: 99,
    });
    switchedPlan.timeline = {
      ...switchedPlan.timeline,
      source_start_seconds: 120,
      player_start_seconds: 120,
    };
    let restarts = 0;
    const harness = reconnectHarness({
      initialPlan: firstPlan,
      replan: () =>
        jsonResponse(
          { error: "playback_session_not_found", message: "Playback session not found" },
          { status: 404 },
        ),
      restart: () => {
        restarts += 1;
        return restarts === 1
          ? jsonResponse(playable("session-1", switchedPlan), { status: 201 })
          : jsonResponse(playable("session-2"), { status: 201 });
      },
    });
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.planRevision).toBe(1));

    act(() => result.current.switchVersion(99, 120));
    await waitFor(() => expect(result.current.plan?.plan_id).toBe("plan:switched00000000001"));
    expect(result.current.initialPosition).toBe(120);

    // The replacement's session is answered gone before its first frame.
    act(() => result.current.reportSessionLost());
    expect(result.current.connectionStatus).toBe("lost");

    act(() => result.current.retryConnection());
    await waitFor(() => expect(harness.startBodies).toHaveLength(3));

    // Retry rebuilds at the replaced plan's position, proving the replacement
    // did not clear the adopted-plan flag and fall back to the request zero.
    expect(harness.startBodies[2]?.start_position).toBe(120);
    expect(result.current.sessionId).toBe("session-2");
    expect(result.current.connectionStatus).toBe("connected");

    unmount();
  });

  it("keeps a paused adopted plan's resume intent through a pre-first-frame loss", async () => {
    // The player was paused when the connection dropped, so the replacement the
    // reconnect adopts is paused too, and the server resolved it to zero. A
    // pre-first-frame loss must carry both through Retry: the pause stays, and
    // the adopted zero supersedes the previous plan's position.
    const firstPlan = fixturePlanV3({ session_id: "session-1" });
    firstPlan.timeline = {
      ...firstPlan.timeline,
      source_start_seconds: 275,
      player_start_seconds: 5,
      timeline_offset_seconds: 270,
    };
    const pausedPlan = fixturePlanV3({
      session_id: "session-1",
      plan_id: "plan:paused000000000001",
    });
    let replans = 0;
    const harness = reconnectHarness({
      initialPlan: firstPlan,
      replan: () => {
        replans += 1;
        return replans === 1
          ? jsonResponse(playable("session-1", pausedPlan))
          : jsonResponse(
              { error: "playback_session_not_found", message: "Playback session not found" },
              { status: 404 },
            );
      },
      restart: () => jsonResponse(playable("session-2"), { status: 201 }),
    });
    const rendered = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(rendered.result.current.planRevision).toBe(1));
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });

    // The viewer paused, then the connection dropped: the reconnect's first
    // attempt adopts a replacement plan, paused and resolved to zero.
    act(() => rendered.result.current.recoverConnection(275, false));
    await advance(RECONNECT_BASE_DELAY_MS);

    expect(rendered.result.current.plan?.plan_id).toBe("plan:paused000000000001");
    expect(rendered.result.current.shouldAutoPlay).toBe(false);
    expect(rendered.result.current.initialPosition).toBe(0);

    // The replacement's session is answered gone before its first frame.
    act(() => rendered.result.current.reportSessionLost());
    expect(rendered.result.current.connectionStatus).toBe("lost");

    // Retry keeps the pause and the adopted zero, not the previous plan's 275.
    act(() => rendered.result.current.retryConnection());
    await advance(0);

    const restart = harness.startBodies[1];
    expect(restart?.start_position).toBe(0);
    expect(rendered.result.current.shouldAutoPlay).toBe(false);
    expect(rendered.result.current.sessionId).toBe("session-2");

    rendered.unmount();
  });

  it("runs the reconnect's pending attempt immediately when a gone session is reported", async () => {
    const harness = reconnectHarness({ replan: () => unreachable() });
    const { result, unmount } = await startPlaying();

    // A network drop starts the reconnect and arms its first backoff.
    act(() => result.current.recoverConnection(100, true));
    expect(result.current.connectionStatus).toBe("reconnecting");
    expect(harness.replanBodies).toHaveLength(0);

    // A session-scoped call answers that the session is gone while the backoff
    // is still armed. The reconnect replaces a dead session with a fresh start,
    // so its next attempt must run now rather than wait out the delay.
    act(() => result.current.expediteReconnect());
    await advance(0);

    expect(harness.replanBodies).toHaveLength(1);
    expect(result.current.connectionStatus).toBe("reconnecting");

    unmount();
  });

  it("ends instead of starting a new session when an administrator ended the session", async () => {
    const harness = reconnectHarness({
      replan: () =>
        jsonResponse(
          {
            type: "https://siloserver.org/docs/api/v2/problems/playback_session_ended",
            title: "Playback session ended",
            status: 410,
            detail: "An administrator ended this playback session",
          },
          { status: 410 },
        ),
      restart: () => jsonResponse(playable("session-2"), { status: 201 }),
    });
    const { result, unmount } = await startPlaying();

    act(() => result.current.recoverConnection(240, true));
    await advance(RECONNECT_BASE_DELAY_MS);

    expect(harness.replanBodies).toHaveLength(1);
    expect(harness.startBodies).toHaveLength(1);
    expect(result.current.connectionStatus).toBe("ended");
    expect(result.current.sessionId).toBe("session-1");

    // Nothing retries an ended session.
    await advance(RECONNECT_MAX_DELAY_MS * 2);
    expect(harness.replanBodies).toHaveLength(1);
    expect(harness.startBodies).toHaveLength(1);

    unmount();
  });

  it("ends when a recovery replan learns an administrator ended the session", async () => {
    const harness = reconnectHarness({
      replan: () =>
        jsonResponse(
          {
            type: "https://siloserver.org/docs/api/v2/problems/playback_session_ended",
            title: "Playback session ended",
            status: 410,
            detail: "An administrator ended this playback session",
          },
          { status: 410 },
        ),
    });
    const { result, unmount } = await startPlaying();

    await act(async () => {
      result.current.changeQuality("720p", 100);
    });

    expect(harness.replanBodies).toHaveLength(1);
    expect(harness.startBodies).toHaveLength(1);
    expect(result.current.connectionStatus).toBe("ended");
    expect(result.current.error).toBeNull();

    unmount();
  });

  it("does not reconnect a session a reanchor found ended before that state renders", async () => {
    const harness = reconnectHarness({
      replan: () =>
        jsonResponse(
          {
            type: "https://siloserver.org/docs/api/v2/problems/playback_session_ended",
            title: "Playback session ended",
            status: 410,
            detail: "An administrator ended this playback session",
          },
          { status: 410 },
        ),
      restart: () => jsonResponse(playable("session-2"), { status: 201 }),
    });
    const { result, unmount } = await startPlaying();
    const session = result.current;

    // The player's reopen fallback runs as soon as the reanchor settles, from a
    // render that still says connected.
    await act(async () => {
      expect(await session.reanchorSeek(240)).toBe(false);
      session.recoverConnection(240, true);
    });
    await advance(RECONNECT_MAX_DELAY_MS * 2);

    expect(harness.replanBodies).toHaveLength(1);
    expect(harness.startBodies).toHaveLength(1);
    expect(result.current.connectionStatus).toBe("ended");

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

describe("usePlaybackSession initial file selection", () => {
  function version(overrides: Partial<PlayerFileVersion> & { file_id: number }): PlayerFileVersion {
    return {
      resolution: "",
      codec_video: "",
      codec_audio: "",
      hdr: false,
      container: "virtual",
      file_size: 0,
      duration: 0,
      bitrate: 0,
      ...overrides,
    };
  }

  // The season-card route leaves `fileId` unset and its resume hint can point at
  // the collapsed episode row (23_517) rather than the concrete candidate
  // (23_519) that actually owns the tracks.
  const neutralRow = version({
    file_id: 23_517,
    file_path: "virtual://series/tt0434665/17/1",
  });
  const concreteRow = version({
    file_id: 23_519,
    file_path: "virtual://series/tt0434665/17/1?result=23519",
    resolution: "1080p",
    codec_video: "h264",
    codec_audio: "aac",
    subtitle_tracks: [{ index: 0, language: "eng" }],
  });

  function captureStarts() {
    const startBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/playback/start")) {
          startBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
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
        if (init?.method === "DELETE") return jsonResponse({ outcome: "stopped" });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );
    return startBodies;
  }

  it("does not request a neutral virtual row named only by the resume hint", async () => {
    const startBodies = captureStarts();
    const { result, unmount } = renderHook(
      () =>
        usePlaybackSession(
          "request-1",
          [neutralRow, concreteRow],
          [],
          undefined,
          0,
          false,
          "auto",
          null,
          { lastFileId: neutralRow.file_id },
          null,
          undefined,
          undefined,
          false,
          false,
          true,
          "auto",
        ),
      { wrapper },
    );

    await waitFor(() => expect(result.current.planRevision).toBe(1));
    expect(startBodies).toHaveLength(1);
    expect(startBodies[0]?.file_id).toBe(concreteRow.file_id);

    unmount();
  });

  it("keeps an explicit preferred file authoritative even when it is neutral", async () => {
    const startBodies = captureStarts();
    const { result, unmount } = renderHook(
      () =>
        usePlaybackSession(
          "request-1",
          [neutralRow, concreteRow],
          [],
          neutralRow.file_id,
          0,
          false,
          "auto",
          null,
          { lastFileId: concreteRow.file_id },
          null,
          undefined,
          undefined,
          true,
          false,
          true,
          "always",
        ),
      { wrapper },
    );

    await waitFor(() => expect(result.current.planRevision).toBe(1));
    expect(startBodies).toHaveLength(1);
    expect(startBodies[0]?.file_id).toBe(neutralRow.file_id);

    unmount();
  });

  it("switches to a subtitle-bearing candidate in the resumed edition, not another edition", async () => {
    const resumedWithoutSubtitles = version({
      file_id: 23_520,
      file_path: "virtual://series/show/1/1?result=a",
      edition_key: "theatrical",
      resolution: "2160p",
      codec_video: "hevc",
      codec_audio: "eac3",
      subtitle_tracks: [],
    });
    const sameEditionWithSubtitles = version({
      file_id: 23_521,
      file_path: "virtual://series/show/1/1?result=b",
      edition_key: "theatrical",
      resolution: "1080p",
      codec_video: "h264",
      codec_audio: "aac",
      subtitle_tracks: [{ index: 0, language: "eng" }],
    });
    const otherEditionWithSubtitles = version({
      file_id: 23_522,
      file_path: "virtual://series/show/1/1?result=c",
      edition_key: "director",
      resolution: "1080p",
      codec_video: "h264",
      codec_audio: "aac",
      subtitle_tracks: [{ index: 0, language: "eng" }],
    });
    const startBodies = captureStarts();
    const { result, unmount } = renderHook(
      () =>
        usePlaybackSession(
          "request-1",
          [resumedWithoutSubtitles, sameEditionWithSubtitles, otherEditionWithSubtitles],
          [],
          undefined,
          0,
          false,
          "auto",
          null,
          { lastFileId: resumedWithoutSubtitles.file_id, lastEditionKey: "theatrical" },
          null,
          undefined,
          undefined,
          false,
          false,
          true,
          "auto",
        ),
      { wrapper },
    );

    await waitFor(() => expect(result.current.planRevision).toBe(1));
    expect(startBodies[0]?.file_id).toBe(sameEditionWithSubtitles.file_id);

    unmount();
  });
});

describe("usePlaybackSession track change guards", () => {
  const planAudioTracks = [
    {
      codec: "aac",
      channels: 2,
      layout: "stereo",
      language: "eng",
      default: true,
      track_id: "file:7:audio:0",
    },
    {
      codec: "eac3",
      channels: 6,
      layout: "5.1",
      language: "fra",
      default: false,
      track_id: "file:7:audio:1",
    },
  ];

  function guardHarness(plan = fixturePlanV3()) {
    const replanBodies: Array<{
      selected_tracks?: {
        audio?: Record<string, unknown>;
        subtitle?: Record<string, unknown>;
      };
    }> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
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
          replanBodies.push(JSON.parse(String(init?.body)) as (typeof replanBodies)[number]);
          return jsonResponse({
            protocol_version: 3,
            server_features: ["playback_plan_v3"],
            outcome: "playable",
            session_id: "session-1",
            playback_plan: fixturePlanV3({
              plan_id: "plan:2222222222222222",
              plan_attempt_key: "v3:2222222222222222",
              audio_tracks: plan.audio_tracks,
              subtitle: plan.subtitle,
            }),
          });
        }
        if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
        if (init?.method === "DELETE") return new Response(null, { status: 204 });
        throw new Error(`Unexpected request: ${url}`);
      }),
    );
    return replanBodies;
  }

  it("ignores an audio ordinal the plan does not publish", async () => {
    const plan = fixturePlanV3({
      audio_tracks: planAudioTracks,
      selected_tracks: { audio: { id: "file:7:audio:0", index: 0 } },
    });
    const replanBodies = guardHarness(plan);
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe(plan.plan_id));

    act(() => result.current.switchAudioTrack(5, 30));
    expect(replanBodies).toHaveLength(0);
    expect(result.current.replanning).toBe(false);
    expect(result.current.trackSelectionNotice?.message).toContain("audio track");

    // A published ordinal still changes the track.
    act(() => result.current.switchAudioTrack(1, 31));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    expect(replanBodies[0]?.selected_tracks?.audio).toMatchObject({
      id: "file:7:audio:1",
      index: 1,
    });

    unmount();
  });

  it("ignores a subtitle ordinal the plan does not publish", async () => {
    const published = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:0",
      combined_index: 0,
      language: "eng",
    });
    const plan = fixturePlanV3({
      subtitle: { mode: "render", track_id: published.track_id, inventory: [published] },
    });
    const replanBodies = guardHarness(plan);
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe(plan.plan_id));

    act(() => result.current.changeSubtitleTrack(4, 30));
    expect(replanBodies).toHaveLength(0);
    expect(result.current.replanning).toBe(false);
    expect(result.current.trackSelectionNotice?.message).toContain("subtitle track");

    // The published ordinal is sent with its identity, not a bare index.
    act(() => result.current.changeSubtitleTrack(0, 31));
    await waitFor(() => expect(replanBodies).toHaveLength(1));
    expect(replanBodies[0]?.selected_tracks?.subtitle).toMatchObject({
      id: "file:7:subtitle:0",
      index: 0,
    });

    unmount();
  });

  it("reconciles a stale subtitle slot by language instead of sending its ordinal", async () => {
    const outgoing = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:4",
      combined_index: 4,
      language: "eng",
      label: "English",
    });
    const incoming = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:7",
      combined_index: 7,
      language: "eng",
      label: "English",
    });
    const plan = fixturePlanV3({
      subtitle: { mode: "render", track_id: outgoing.track_id, inventory: [outgoing] },
    });
    const replanBodies = guardHarness(plan);
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe(plan.plan_id));

    // The verified push replaces the plan's inventory in the same tick the
    // viewer's click still names the outgoing menu slot (4). The pick is the
    // same English track, now at ordinal 7, so that is what the replan asks
    // for; ordinal 4 no longer exists in the plan.
    act(() => {
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:subtitle-moved",
        inventory_status: "verified",
        effective_media_file_id: 7,
        audio_tracks: planAudioTracks,
        subtitle_inventory: [incoming],
      });
      result.current.changeSubtitleTrack(4, 40);
    });

    await waitFor(() => expect(replanBodies).toHaveLength(1));
    expect(replanBodies[0]?.selected_tracks?.subtitle).toMatchObject({
      id: "file:7:subtitle:7",
      index: 7,
    });

    unmount();
  });

  it("reconciles a stale subtitle slot by identity when the plan reorders ordinals", async () => {
    const english = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:0",
      combined_index: 0,
      language: "eng",
      label: "English",
    });
    const french = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:1",
      combined_index: 1,
      language: "fra",
      label: "French",
    });
    const plan = fixturePlanV3({
      subtitle: { mode: "render", track_id: english.track_id, inventory: [english, french] },
    });
    const replanBodies = guardHarness(plan);
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe(plan.plan_id));

    // The verified revision swaps the ordinal order: French is now 0 and
    // English is 1. The click still names the displayed English slot (0), so
    // the plan ordinal would select French. The picked track's identity wins.
    act(() => {
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:subtitle-reordered",
        inventory_status: "verified",
        effective_media_file_id: 7,
        audio_tracks: planAudioTracks,
        subtitle_inventory: [
          { ...french, combined_index: 0 },
          { ...english, combined_index: 1 },
        ],
      });
      result.current.changeSubtitleTrack(0, 40);
    });

    await waitFor(() => expect(replanBodies).toHaveLength(1));
    expect(replanBodies[0]?.selected_tracks?.subtitle).toMatchObject({
      id: "file:7:subtitle:0",
      index: 1,
    });

    unmount();
  });

  it("reconciles a stale subtitle slot without track ids by language", async () => {
    const english = fixtureSubtitleInventoryItemV3({
      track_id: "",
      combined_index: 0,
      language: "eng",
      label: "English",
    });
    const french = fixtureSubtitleInventoryItemV3({
      track_id: "",
      combined_index: 1,
      language: "fra",
      label: "French",
    });
    const plan = fixturePlanV3({
      subtitle: { mode: "render", inventory: [english, french] },
    });
    const replanBodies = guardHarness(plan);
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe(plan.plan_id));

    // The menu publishes no stable ids, so the pick is named only by its slot:
    // slot 0 is English. The verified revision moves French to 0, and only the
    // language reconciliation can still find the track the viewer clicked.
    act(() => {
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:subtitle-reordered-no-ids",
        inventory_status: "verified",
        effective_media_file_id: 7,
        audio_tracks: planAudioTracks,
        subtitle_inventory: [
          { ...french, track_id: "file:7:subtitle:1", combined_index: 0 },
          { ...english, track_id: "file:7:subtitle:0", combined_index: 1 },
        ],
      });
      result.current.changeSubtitleTrack(0, 40);
    });

    await waitFor(() => expect(replanBodies).toHaveLength(1));
    expect(replanBodies[0]?.selected_tracks?.subtitle).toMatchObject({
      id: "file:7:subtitle:0",
      index: 1,
    });

    unmount();
  });

  it("ignores a stale subtitle slot with no counterpart even when the ordinal exists", async () => {
    const japanese = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:2",
      combined_index: 0,
      language: "jpn",
      label: "Japanese",
    });
    const english = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:0",
      combined_index: 1,
      language: "eng",
      label: "English",
    });
    const french = fixtureSubtitleInventoryItemV3({
      track_id: "file:7:subtitle:1",
      combined_index: 2,
      language: "fra",
      label: "French",
    });
    const plan = fixturePlanV3({
      subtitle: { mode: "render", track_id: japanese.track_id, inventory: [japanese, english] },
    });
    const replanBodies = guardHarness(plan);
    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.plan_id).toBe(plan.plan_id));

    // The viewer clicked the displayed Japanese slot (0). The revision no
    // longer carries Japanese, and slot 0 now names English: sending the stale
    // ordinal would silently switch language, so the pick is surfaced.
    act(() => {
      result.current.applyInventoryUpdate({
        session_id: "session-1",
        inventory_revision: "inv:subtitle-dropped",
        inventory_status: "verified",
        effective_media_file_id: 7,
        audio_tracks: planAudioTracks,
        subtitle_inventory: [
          { ...english, combined_index: 0 },
          { ...french, combined_index: 1 },
        ],
      });
      result.current.changeSubtitleTrack(0, 40);
    });

    expect(replanBodies).toHaveLength(0);
    expect(result.current.replanning).toBe(false);
    expect(result.current.trackSelectionNotice?.message).toContain("subtitle track");

    unmount();
  });
});

describe("usePlaybackSession terminal refusals", () => {
  it("retires a broken plan and surfaces a retryable error card on adaptation_unavailable", async () => {
    let starts = 0;
    let replans = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        starts += 1;
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
        replans += 1;
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "adaptation_unavailable",
          terminal: {
            reason: "adaptation_unavailable",
            message: "No validated playback route is available for this source and output route.",
            retryable: false,
          },
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => {
      result.current.recoverFromFailure({ classification: "decoder_error" }, 30);
    });

    // The broken transport stays broken: a refusal that names no playable
    // adaptation leaves nothing to render, so the player must stop showing a
    // spinner over a plan the server cannot serve.
    await waitFor(() => expect(result.current.errorTitle).toBe("No playable version found"));
    expect(result.current.plan).toBeNull();
    expect(result.current.loading).toBe(false);
    expect(result.current.errorReason).toBe("adaptation_unavailable");
    expect(replans).toBe(1);

    // Retry is a fresh start, not a replan against the broken route.
    act(() => result.current.retryStart());
    await waitFor(() => expect(result.current.plan).not.toBeNull());
    expect(starts).toBe(2);

    unmount();
  });

  it("keeps the playing plan when a quality change is refused with adaptation_unavailable", async () => {
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
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "adaptation_unavailable",
          terminal: {
            reason: "adaptation_unavailable",
            message: "No validated playback route is available for this source and output route.",
            retryable: false,
          },
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan).not.toBeNull());

    act(() => result.current.changeQuality("720p", 30));
    await waitFor(() => expect(result.current.errorReason).toBe("adaptation_unavailable"));

    // A refused quality change is not a failed transport: the old stream is
    // still the viewer's, so only the refusal surfaces (the menu owns the copy).
    expect(result.current.plan).not.toBeNull();
    expect(result.current.errorTitle).toBe("No playable version found");

    unmount();
  });

  it("enters the session-gone state and restarts with a fresh start", async () => {
    const startBodies: Array<Record<string, unknown>> = [];
    const replanBodies: Array<Record<string, unknown>> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/playback/start")) {
        startBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        const sessionId = `session-${startBodies.length}`;
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
      const replanMatch = /\/playback\/([^/]+)\/replan$/.exec(url);
      if (replanMatch) {
        replanBodies.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return jsonResponse({
          protocol_version: 3,
          server_features: ["playback_plan_v3"],
          outcome: "playable",
          session_id: replanMatch[1]!,
          playback_plan: fixturePlanV3({ session_id: replanMatch[1]! }),
        });
      }
      if (url.endsWith("/playback/route-events")) return new Response(null, { status: 202 });
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result, unmount } = renderHook(
      () => usePlaybackSession("request-1", [], [], 7, 0, false, "auto"),
      { wrapper },
    );
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-1"));
    act(() => result.current.updatePlaybackState(120, true));

    // A control-ticket mint (or a stream/subtitle fetch) proved the session is
    // gone. No replan may be issued against the dead id.
    act(() => result.current.markSessionGone());
    expect(result.current.connectionStatus).toBe("gone");
    expect(result.current.connectionErrorTitle).toBe("Playback session expired");

    act(() => result.current.restartPlayback());
    await waitFor(() => expect(result.current.plan?.session_id).toBe("session-2"));
    expect(result.current.connectionStatus).toBe("connected");
    expect(replanBodies).toHaveLength(0);
    expect(startBodies).toHaveLength(2);
    // The restart resumes where the viewer was, not at the request's origin.
    expect(startBodies[1]).toMatchObject({ start_position: 120 });

    unmount();
  });
});
