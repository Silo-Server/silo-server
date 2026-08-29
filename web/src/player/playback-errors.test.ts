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

  it("keeps the server's explanation of why a subtitle cannot be used", () => {
    expect(
      describePlanTerminal({
        reason: "subtitle_conversion_unsupported",
        message:
          "The selected subtitle must be burned into the video, but 4K transcoding is disabled.",
        retryable: false,
      }),
    ).toEqual({
      title: "That subtitle track can't be used",
      message:
        "The selected subtitle must be burned into the video, but 4K transcoding is disabled.",
    });
  });

  it("falls back to a generic subtitle sentence when the server sends no message", () => {
    expect(
      describePlanTerminal({
        reason: "subtitle_codec_unsupported",
        message: "  ",
        retryable: false,
      }),
    ).toEqual({
      title: "That subtitle track can't be used",
      message:
        "Silo couldn't prepare the selected subtitles for this device. Try a different track.",
    });
  });

  it("keeps the server's reason for refusing every version of an item", () => {
    expect(
      describePlanTerminal({
        reason: "no_alternate_version",
        message: "A lower-resolution source is required because 4K transcoding is disabled.",
        retryable: false,
      }),
    ).toEqual({
      title: "No playable version found",
      message: "A lower-resolution source is required because 4K transcoding is disabled.",
    });
  });

  it("falls back to a generic sentence when no alternate version carries no message", () => {
    expect(
      describePlanTerminal({ reason: "no_alternate_version", message: "   ", retryable: false }),
    ).toEqual({
      title: "No playable version found",
      message:
        "Silo couldn't find a way to play this file on this device. Try another version if one is available.",
    });
  });

  it("keeps the generic sentence for an exhausted adaptation search", () => {
    expect(
      describePlanTerminal({
        reason: "adaptation_exhausted",
        message: "All compatible playback recipes have already failed for this output route.",
        retryable: false,
      }),
    ).toEqual({
      title: "No playable version found",
      message:
        "Silo couldn't find a way to play this file on this device. Try another version if one is available.",
    });
  });

  it("keeps the server's explanation of a failed conversion start", () => {
    expect(
      describePlanTerminal({
        reason: "transcode_start_failed",
        message: "Failed to start the playback transport.",
        retryable: true,
      }),
    ).toEqual({
      title: "Playback unavailable",
      message: "Failed to start the playback transport.",
    });
  });

  it("falls back to a generic sentence when a conversion failure carries no message", () => {
    expect(
      describePlanTerminal({
        reason: "transcode_node_unavailable",
        message: " ",
        retryable: true,
      }),
    ).toEqual({
      title: "Playback unavailable",
      message: "The server couldn't start converting this file. Please try again.",
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
