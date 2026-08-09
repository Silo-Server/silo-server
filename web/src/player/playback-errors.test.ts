import { describe, expect, it } from "vitest";

import { describePlanTerminal, describePlaybackTransportError } from "./playback-errors";
import { PlayerFetchError } from "./player-fetch";

describe("describePlanTerminal", () => {
  it("describes disabled video transcoding", () => {
    expect(
      describePlanTerminal({
        reason: "transcoding_disabled",
        message: "The selected server adaptation is disabled for this user.",
        retryable: false,
      }),
    ).toEqual({
      title: "Transcoding is disabled",
      message: "Transcoding is disabled for your user. Ask your server administrator for access.",
    });
  });

  it("describes disabled audio transcoding", () => {
    expect(
      describePlanTerminal({
        reason: "audio_transcoding_disabled",
        message: "The selected server adaptation is disabled for this user.",
        retryable: false,
      }),
    ).toEqual({
      title: "Audio transcoding is disabled",
      message:
        "This item requires audio conversion, but audio transcoding is disabled for your user.",
    });
  });

  it("falls back to the server's own message for reasons it does not name", () => {
    expect(
      describePlanTerminal({
        reason: "some_future_reason",
        message: "A newer server explained this precisely.",
        retryable: false,
      }),
    ).toEqual({
      title: "Playback unavailable",
      message: "A newer server explained this precisely.",
    });
  });

  it("still says something useful when the server sends no message", () => {
    expect(
      describePlanTerminal({ reason: "some_future_reason", message: "", retryable: false }),
    ).toEqual({
      title: "Playback unavailable",
      message: "Silo could not start playback.",
    });
  });
});

describe("describePlaybackTransportError", () => {
  it("renders 426 as an update-required state", () => {
    const error = new PlayerFetchError(426, "Client upgrade required", "client_upgrade_required");

    expect(describePlaybackTransportError(error)).toEqual({
      title: "Update required",
      message:
        "This server speaks a newer playback protocol than this app. Reload the page to pick up the current version.",
    });
  });

  it("ignores non-fetch errors", () => {
    expect(describePlaybackTransportError(new Error("boom"))).toBeNull();
  });

  it("distinguishes an expired playback session from a missing item", () => {
    expect(
      describePlaybackTransportError(
        new PlayerFetchError(404, "Playback session not found", "playback_session_not_found"),
      ),
    ).toEqual({
      title: "Playback session expired",
      message: "This playback session is no longer active. Start it again to keep watching.",
    });
  });

  it("ignores 4xx statuses it has nothing specific to say about", () => {
    expect(
      describePlaybackTransportError(new PlayerFetchError(409, "Conflict", "stale_playback_plan")),
    ).toBeNull();
  });
});
