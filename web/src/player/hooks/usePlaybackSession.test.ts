import { describe, expect, it } from "vitest";

import {
  fixtureClientCapabilitiesV3,
  fixtureClientPlaybackContextV3,
  fixturePlanV3,
} from "../protocol-v3.fixtures";
import {
  buildReplanRequestV3,
  buildStartRequestV3,
  routeEventPlanIdentityV3,
} from "./usePlaybackSession";

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

  it("sends an empty classification when nothing failed", () => {
    const body = buildReplanRequestV3({ ...replanBase, operation: "quality_change" });

    expect(body.failure).toEqual({ classification: "" });
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
