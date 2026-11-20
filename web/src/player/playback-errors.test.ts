import { describe, expect, it } from "vitest";

import {
  describePlanTerminal,
  describePlaybackTransportError,
  isAdaptationUnavailableReason,
  isDeadPlaybackSessionError,
  isPlaybackSessionGoneError,
  isPlaybackSessionGoneResponse,
} from "./playback-errors";
import { PlayerFetchError } from "./player-fetch";
import { V2ProblemError, V2TransportError } from "@/api/v2/request";

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
      reason: "transcoding_disabled",
      retryable: false,
    });
  });

  it("tells an unreadable file apart from one that has not been scanned", () => {
    const unreadable = describePlanTerminal({
      reason: "source_unreadable",
      message: "The source file could not be read; it appears to be empty or damaged.",
      retryable: false,
    });
    expect(unreadable.title).toBe("This file can't be played");
    expect(unreadable.message).toContain("empty or damaged");
    expect(unreadable.message).not.toMatch(/scan finishes/);

    expect(
      describePlanTerminal({
        reason: "source_metadata_incomplete",
        message: "The source is missing video metadata required for a validated playback route.",
        retryable: true,
      }).title,
    ).toBe("This file hasn't finished scanning");
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
      reason: "audio_transcoding_disabled",
      retryable: false,
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
      reason: "subtitle_conversion_unsupported",
      retryable: false,
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
      reason: "no_alternate_version",
      retryable: false,
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
        "Vio couldn't find a way to play this file on this device. Try another version if one is available.",
      reason: "adaptation_exhausted",
      retryable: false,
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
      reason: "transcode_start_failed",
      retryable: true,
    });
  });

  it("names a source mismatch and keeps the server's version-list hint", () => {
    expect(
      describePlanTerminal({
        reason: "source_mismatch",
        message:
          "The provider served a different release's bytes for this version. Refresh the version list and choose again.",
        retryable: true,
      }),
    ).toEqual({
      title: "This version's source is wrong",
      message:
        "The provider served a different release's bytes for this version. Refresh the version list and choose again.",
      reason: "source_mismatch",
      retryable: true,
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
      reason: "some_future_reason",
      retryable: false,
    });
  });

  it("still says something useful when the server sends no message", () => {
    expect(
      describePlanTerminal({ reason: "some_future_reason", message: "", retryable: false }),
    ).toEqual({
      title: "Playback unavailable",
      message: "Vio could not start playback.",
      reason: "some_future_reason",
      retryable: false,
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
      retryable: false,
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
      retryable: false,
    });
  });

  it("describes 401 authentication errors distinctly from 403 permissions errors", () => {
    expect(describePlaybackTransportError(new PlayerFetchError(401, "Unauthorized"))).toEqual({
      title: "Authentication required",
      message: "Your sign-in session has expired. Sign in again to continue playback.",
      retryable: false,
    });

    expect(describePlaybackTransportError(new PlayerFetchError(403, "Forbidden"))).toEqual({
      title: "Playback unavailable",
      message: "You do not have permission to play this item.",
      retryable: false,
    });
  });

  it("does not call a failed change to a playing stream a failure to start", () => {
    const error = new PlayerFetchError(502, "Bad Gateway");

    expect(describePlaybackTransportError(error)?.message).toBe(
      "Vio could not start playback right now. Please try again.",
    );
    expect(describePlaybackTransportError(error, "update")?.message).toBe(
      "Vio could not update playback right now. Please try again.",
    );
  });

  it("marks a server 5xx retryable, including the store fault it now answers with 503", () => {
    expect(
      describePlaybackTransportError(new PlayerFetchError(502, "Bad Gateway"))?.retryable,
    ).toBe(true);
    expect(
      describePlaybackTransportError(
        new PlayerFetchError(503, "Dependency unavailable", "dependency_unavailable"),
      )?.retryable,
    ).toBe(true);
  });

  it("marks a request that never completed retryable", () => {
    // The start lane aborts on its own deadline with a TimeoutError, and a
    // fetch that never reached the server rejects with a TypeError.
    expect(
      describePlaybackTransportError(new DOMException("Playback start timed out", "TimeoutError")),
    ).toMatchObject({ title: "Connection problem", retryable: true });
    expect(describePlaybackTransportError(new TypeError("Failed to fetch"))).toMatchObject({
      title: "Connection problem",
      retryable: true,
    });
    // A deliberate abort is not a failure worth retrying; it has no copy.
    expect(
      describePlaybackTransportError(new DOMException("The operation was aborted", "AbortError")),
    ).toBeNull();
  });

  it("keeps the definitive answers non-retryable", () => {
    const retryable = (error: PlayerFetchError) => describePlaybackTransportError(error)?.retryable;
    // A missing file or version, an auth refusal, and a protocol this build
    // cannot speak are all verdicts: asking again cannot change them.
    expect(retryable(new PlayerFetchError(404, "Not Found"))).toBe(false);
    expect(retryable(new PlayerFetchError(401, "Unauthorized"))).toBe(false);
    expect(retryable(new PlayerFetchError(403, "Forbidden"))).toBe(false);
    expect(retryable(new PlayerFetchError(426, "Upgrade required"))).toBe(false);
  });

  it("ignores 4xx statuses it has nothing specific to say about", () => {
    expect(
      describePlaybackTransportError(new PlayerFetchError(409, "Conflict", "stale_playback_plan")),
    ).toBeNull();
  });
});

describe("isDeadPlaybackSessionError", () => {
  it("names the errors a fresh start can repair", () => {
    expect(
      isDeadPlaybackSessionError(
        new PlayerFetchError(404, "Playback session not found", "playback_session_not_found"),
      ),
    ).toBe(true);
    expect(
      isDeadPlaybackSessionError(
        new PlayerFetchError(410, "Playback session has ended", "playback_session_ended"),
      ),
    ).toBe(true);
    // A 401 has already survived the one token-refresh retry in player-fetch.
    expect(isDeadPlaybackSessionError(new PlayerFetchError(401, "Unauthorized"))).toBe(true);
  });

  it("keeps a missing source file and non-fetch failures out", () => {
    expect(
      isDeadPlaybackSessionError(
        new PlayerFetchError(404, "Source media file is missing", "not_found"),
      ),
    ).toBe(false);
    expect(isDeadPlaybackSessionError(new PlayerFetchError(500, "Server error"))).toBe(false);
    expect(isDeadPlaybackSessionError(new Error("boom"))).toBe(false);
  });
});

describe("isPlaybackSessionGoneError", () => {
  it("rejects a proxy's untyped 404 whose HTML body has no problem code", () => {
    // `mutationFetch` throws this for any non-2xx: an HTML proxy page parses
    // to no problem, so the code is undefined. It is a transport failure, not
    // the session being gone, and must stay on the retry path.
    expect(
      isPlaybackSessionGoneError(
        new PlayerFetchError(404, "Not Found", undefined, "<html>404 Not Found</html>"),
      ),
    ).toBe(false);
    expect(isPlaybackSessionGoneError(new PlayerFetchError(404, "Not Found"))).toBe(false);
  });

  it("accepts the problem codes the server sends for a gone session", () => {
    // `not_found` is what the v2 progress adapter renders `session_not_found`
    // as; the longer names are the v1/bridge and stopped-session forms.
    expect(
      isPlaybackSessionGoneError(
        new PlayerFetchError(404, "Playback session not found", "not_found"),
      ),
    ).toBe(true);
    expect(
      isPlaybackSessionGoneError(
        new PlayerFetchError(404, "Playback session not found", "session_not_found"),
      ),
    ).toBe(true);
    expect(
      isPlaybackSessionGoneError(
        new PlayerFetchError(404, "Playback session not found", "playback_session_not_found"),
      ),
    ).toBe(true);
    expect(
      isPlaybackSessionGoneError(
        new PlayerFetchError(410, "Playback session has ended", "playback_session_ended"),
      ),
    ).toBe(true);
  });

  it("rejects a typed player error that names some other failure", () => {
    expect(
      isPlaybackSessionGoneError(new PlayerFetchError(404, "Source missing", "source_missing")),
    ).toBe(false);
    expect(
      isPlaybackSessionGoneError(new PlayerFetchError(503, "Server busy", "unavailable")),
    ).toBe(false);
  });

  it("accepts a v2 problem 404/410 and rejects everything untyped", () => {
    const problem = (status: number) =>
      new V2ProblemError("op", {
        type: "https://siloserver.org/docs/api/v2/problems/not_found",
        title: "Not found",
        status,
        detail: "Playback session not found.",
        instance: "/api/v2/playback/session-1/progress",
      });

    expect(isPlaybackSessionGoneError(problem(404))).toBe(true);
    expect(isPlaybackSessionGoneError(problem(410))).toBe(true);
    // A proxy's non-problem body is a `V2TransportError`, never the session.
    expect(isPlaybackSessionGoneError(new V2TransportError("op", 404, "not a problem"))).toBe(
      false,
    );
    expect(isPlaybackSessionGoneError(new Error("404"))).toBe(false);
    expect(isPlaybackSessionGoneError(null)).toBe(false);
  });
});

function problemResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("isAdaptationUnavailableReason", () => {
  it("names only the outcomes that leave no playable plan", () => {
    expect(isAdaptationUnavailableReason("adaptation_unavailable")).toBe(true);
    expect(isAdaptationUnavailableReason("adaptation_exhausted")).toBe(true);
    expect(isAdaptationUnavailableReason(" subtitle_conversion_unsupported ")).toBe(false);
    expect(isAdaptationUnavailableReason(null)).toBe(false);
    expect(isAdaptationUnavailableReason("")).toBe(false);
  });
});

describe("isPlaybackSessionGoneResponse", () => {
  it("treats a 410 on a session media route as the session being gone", async () => {
    expect(await isPlaybackSessionGoneResponse(problemResponse(410, {}))).toBe(true);
  });

  it("needs an explicit session code on a 404 so a missing artifact keeps retrying", async () => {
    expect(
      await isPlaybackSessionGoneResponse(
        problemResponse(404, { error: "playback_session_not_found" }),
      ),
    ).toBe(true);
    expect(
      await isPlaybackSessionGoneResponse(
        problemResponse(404, {
          type: "https://siloserver.org/docs/api/v2/problems/session_not_found",
          status: 404,
        }),
      ),
    ).toBe(true);
    // A subtitle artifact or source file 404 is not a dead session.
    expect(await isPlaybackSessionGoneResponse(problemResponse(404, { error: "not_found" }))).toBe(
      false,
    );
    expect(
      await isPlaybackSessionGoneResponse(problemResponse(404, { error: "source_missing" })),
    ).toBe(false);
  });

  it("ignores every other status", async () => {
    expect(
      await isPlaybackSessionGoneResponse(problemResponse(503, { error: "unavailable" })),
    ).toBe(false);
    expect(await isPlaybackSessionGoneResponse(problemResponse(500, {}))).toBe(false);
  });
});
