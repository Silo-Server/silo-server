import { isPlaybackSessionEndedError, PlayerFetchError } from "./player-fetch";
import { V2ProblemError } from "@/api/v2/request";
import type { TerminalV3 } from "./protocol-v3";

export interface PlaybackPolicyErrorDescription {
  title: string;
  message: string;
  /**
   * The v3 terminal reason, when this describes a refused plan or start rather
   * than a transport failure. Callers key recovery affordances on it.
   */
  reason?: string;
  /**
   * Whether the server marked the terminal retryable. Absent for transport
   * failures, which carry no server retry verdict.
   */
  retryable?: boolean;
}

/**
 * Turns a v3 terminal into something a person can act on.
 *
 * Under protocol v3 a refused plan is not an HTTP error: the start endpoint
 * answers `201` and the replan endpoint `200`, and the decision lives in the
 * body's `terminal`. The status code only describes the request. So the whole
 * "playback was refused" surface is reason-keyed, and the server's own
 * `terminal.message` is the fallback for reasons this table does not name.
 *
 * The reason and the server's retryable verdict ride along on the description
 * so the UI can offer a retry for the terminals the server says are worth
 * retrying instead of hard-dead-ending every refusal.
 */
export function describePlanTerminal(terminal: TerminalV3): PlaybackPolicyErrorDescription {
  return {
    ...describeTerminalCopy(terminal),
    reason: terminal.reason,
    retryable: terminal.retryable,
  };
}

function describeTerminalCopy(terminal: TerminalV3): { title: string; message: string } {
  switch (terminal.reason) {
    case "transcoding_disabled":
      return {
        title: "Transcoding is disabled",
        message: "Transcoding is disabled for your user. Ask your server administrator for access.",
      };
    case "audio_transcoding_disabled":
      return {
        title: "Audio transcoding is disabled",
        message:
          "This item requires audio conversion, but audio transcoding is disabled for your user.",
      };
    case "source_unavailable":
      return {
        title: "This video is no longer available",
        message:
          "The file needed to play it can't be found right now. Go back and try another version if one is available.",
      };
    case "source_unreadable":
      return {
        title: "This file can't be played",
        message:
          "Silo couldn't read this file. It appears to be empty or damaged. Replace the file, then rescan the library.",
      };
    case "source_metadata_incomplete":
      return {
        title: "This file hasn't finished scanning",
        message:
          "Vio doesn't know enough about this file yet to plan playback. Try again once the scan finishes.",
      };
    case "client_hls_unsupported":
      return {
        title: "This browser can't play the stream",
        message:
          "Playing this file needs HLS, which this browser doesn't support. Try a different browser.",
      };
    case "adaptation_exhausted":
    case "adaptation_unavailable":
      return {
        title: "No playable version found",
        message:
          "Vio couldn't find a way to play this file on this device. Try another version if one is available.",
      };
    case "no_alternate_version":
      return {
        title: "No playable version found",
        // The server names the policy that ruled the source out (4K
        // transcoding disabled, for one); the generic sentence only covers a
        // missing message.
        message:
          terminal.message?.trim() ||
          "Vio couldn't find a way to play this file on this device. Try another version if one is available.",
      };
    case "hdr_transcode_unsupported":
    case "dv_conversion_unsupported":
      return {
        title: "This HDR format can't be converted",
        message:
          "This file's dynamic range can't be converted for this device, and it can't be played as-is.",
      };
    case "video_conversion_unsupported":
    case "audio_conversion_unsupported":
      return {
        title: "This file can't be converted",
        message: "Vio can't convert this file into something this device can play.",
      };
    case "conversion_tool_unavailable":
    case "transcode_node_unavailable":
    case "transcode_node_capability_unavailable":
    case "transcode_start_failed":
      return {
        title: "Playback unavailable",
        // The server names which part of the conversion path failed (no
        // transcode node, settings unavailable, transport refused to start);
        // the generic sentence only covers a missing message.
        message:
          terminal.message?.trim() ||
          "The server couldn't start converting this file. Please try again.",
      };
    case "capacity_unavailable":
      return {
        title: "The server is busy",
        message:
          "There's no capacity to convert this file right now. Please try again in a moment.",
      };
    // The server has room here: this account has used its allowance. The
    // server writes these for viewers, so every client shows the same advice.
    case "stream_limit_reached":
      return {
        title: "Streaming limit reached",
        message:
          terminal.message?.trim() ||
          "Stop anything else playing or paused on your account, then try again.",
      };
    case "transcode_limit_reached":
      return {
        title: "Video conversion limit reached",
        message:
          terminal.message?.trim() ||
          "Stop anything else playing or paused on your account, then try again.",
      };
    case "session_expired":
      return {
        title: "Playback session expired",
        message: "This playback session is no longer active. Start it again to keep watching.",
      };
    case "policy_denied":
      return {
        title: "Playback unavailable",
        message: "You do not have permission to play this item.",
      };
    case "subtitle_burn_in_source_unsupported":
    case "subtitle_codec_unsupported":
    case "subtitle_conversion_unsupported":
    case "subtitle_track_invalid":
    case "subtitle_track_unavailable":
    case "subtitle_unavailable_in_version":
    case "subtitle_artifact_unavailable":
      return {
        title: "That subtitle track can't be used",
        // The server names the specific blocker (burn-in required, source
        // unsupported); the generic sentence only covers a missing message.
        message:
          terminal.message?.trim() ||
          "Vio couldn't prepare the selected subtitles for this device. Try a different track.",
      };
    case "source_mismatch":
      return {
        title: "This version's source is wrong",
        // Same vocabulary the version list uses for a row whose provider served
        // another release's bytes. The release is not dead, so the server marks
        // the terminal retryable and the message names the version list.
        message:
          terminal.message?.trim() ||
          "The provider served a different release's bytes for this version. Refresh the version list and choose again.",
      };
    case "virtual_source_unavailable":
      return {
        title: "Playback unavailable",
        // The server explains which source could not be resolved; the generic
        // sentence only covers a missing message. Retryability comes from the
        // server and is surfaced separately.
        message:
          terminal.message?.trim() || "The virtual source could not be resolved for playback.",
      };
    default:
      return {
        title: "Playback unavailable",
        message: terminal.message?.trim() || "Vio could not start playback.",
      };
  }
}

/**
 * Terminal reasons that mean the server found no playable adaptation for this
 * source on this device. Unlike a refused track or quality pick, there is no
 * plan left to keep on screen: the route itself could not be built.
 */
const ADAPTATION_UNAVAILABLE_REASONS = new Set(["adaptation_exhausted", "adaptation_unavailable"]);

/** True when `reason` is a decision outcome that offers no playable plan. */
export function isAdaptationUnavailableReason(reason: string | null | undefined): boolean {
  const normalized = reason?.trim() ?? "";
  return normalized !== "" && ADAPTATION_UNAVAILABLE_REASONS.has(normalized);
}

/**
 * True when a transport error means the playback session itself is gone.
 *
 * None of these can be repaired by retrying against the same session id:
 * `playback_session_not_found` (404) is a reaped session, `playback_session_ended`
 * (410) is one the server has stopped, and a 401 that survives the one token
 * refresh `playerFetchResponse` already performs means the sign-in is gone.
 * Callers retire the dead session and start a new one instead of reusing it.
 *
 * A plain 404 (`not_found`) is deliberately *not* dead: it names a missing
 * source file, which a fresh start cannot conjure back.
 */
export function isDeadPlaybackSessionError(error: unknown): boolean {
  if (!(error instanceof PlayerFetchError)) {
    return false;
  }
  return (
    error.code === "playback_session_not_found" ||
    error.code === "playback_session_ended" ||
    error.status === 401
  );
}

/**
 * The wire problem ids that mean a playback session is gone.
 *
 * The v2 handler names the missing session `session_not_found`, but that code
 * is not in the shared problem catalog, so `playbackProblemType` renders it as
 * the catalog's plain `not_found` — that is the code a live progress 404
 * actually carries. The v1/bridge surfaces name the session directly
 * (`playback_session_not_found`), and `playback_session_ended` is the 410 the
 * bridge sends for a stopped session. Note the mismatch: the wire code the v2
 * handler emits is `session_not_found`, while the protocol docs advertise
 * `playback_session_not_found`; the wire code stays as the server sends it.
 */
const SESSION_GONE_PROBLEM_CODES = new Set([
  "not_found",
  "session_not_found",
  "playback_session_not_found",
  "playback_session_ended",
]);

/**
 * The problem ids a *session-scoped media route* (a stream or subtitle URL)
 * uses to say the playback session itself is gone.
 *
 * Unlike `SESSION_GONE_PROBLEM_CODES`, a plain `not_found` is deliberately
 * absent: a subtitle artifact or a source file can be missing while the session
 * is perfectly alive, and treating that as a dead session would end playback
 * over one unavailable track.
 */
const SESSION_GONE_MEDIA_PROBLEM_CODES = new Set([
  "session_not_found",
  "playback_session_not_found",
  "playback_session_ended",
]);

/**
 * Whether a session-scoped call was answered that the playback session no
 * longer exists. Unlike `isDeadPlaybackSessionError`, this covers the v2
 * handshake and progress calls, where a 404 *is* the session being gone:
 * neither can answer 404 or 410 for anything else.
 *
 * A retried mint or progress sample for a dead session id can never succeed,
 * so callers retire the session. But `mutationFetch` builds a typed
 * `PlayerFetchError` from *any* non-2xx response, including a proxy's HTML
 * error page (a mis-prefixed route, a captive portal, a stale ingress) whose
 * body does not parse to a problem and leaves the code undefined. Requiring a
 * recognized problem code is what keeps such a transport 404 on the retry
 * path instead of killing playback on the next progress tick.
 *
 * A `V2ProblemError` already proves the server spoke the contract, so this
 * branch accepts *any* 404/410 by status alone. That is deliberately broader
 * than a missing-session answer: the v2 router's unmatched-route 404 is itself
 * a well-formed problem document (`not_found`), so a route mismatch inside
 * `/api/v2` also classifies as gone. The playback routes are static, so a
 * mismatch would be a build error rather than a live condition, and widening
 * this would need a route-scoped problem code the server does not emit.
 */
export function isPlaybackSessionGoneError(error: unknown): boolean {
  if (error instanceof PlayerFetchError) {
    // A 410 that names playback_session_ended only in the problem body is
    // still terminal: the progress lane must stop and hand the player its
    // ended verdict, not keep polling a session an administrator closed.
    if (isPlaybackSessionEndedError(error)) return true;
    return (
      (error.status === 404 || error.status === 410) &&
      error.code !== undefined &&
      SESSION_GONE_PROBLEM_CODES.has(error.code)
    );
  }
  if (error instanceof V2ProblemError) {
    return error.status === 404 || error.status === 410;
  }
  return false;
}

/**
 * Whether a session-scoped media response (a stream or subtitle fetch) says the
 * playback session no longer exists.
 *
 * The media routes are session-scoped, so a 410 there is the ended session and
 * has no other meaning. A 404 is only the session when the problem body names
 * it: a missing artifact or source file answers 404 too, and retrying those is
 * still the right behavior.
 */
export async function isPlaybackSessionGoneResponse(response: Response): Promise<boolean> {
  if (response.status !== 404 && response.status !== 410) return false;
  if (response.status === 410) return true;
  try {
    const body = (await response.json()) as {
      error?: unknown;
      type?: unknown;
      title?: unknown;
      detail?: unknown;
    };
    const errorCode = typeof body.error === "string" ? body.error.trim() : "";
    const typeCode =
      typeof body.type === "string"
        ? (body.type.split("?")[0]?.split("/").pop()?.replace(/#.*$/, "") ?? "")
        : "";
    return SESSION_GONE_MEDIA_PROBLEM_CODES.has(errorCode || typeCode);
  } catch {
    return false;
  }
}

/**
 * What the viewer is told when a session-scoped call proves the playback
 * session no longer exists on the server. Restarting is a fresh start, never a
 * replan against the dead session.
 */
export const PLAYBACK_SESSION_GONE_ERROR: PlaybackPolicyErrorDescription = {
  title: "Playback session expired",
  message: "This playback session is no longer active. Start it again to keep watching.",
};

/**
 * What the viewer is told when a stream that was already playing lost its
 * connection to the server and the player could not get it back.
 */
export const CONNECTION_LOST_ERROR: PlaybackPolicyErrorDescription = {
  title: "Connection lost",
  message:
    "Silo lost its connection to the server and couldn't reconnect. Check your connection, then try again to continue where you left off.",
};

/**
 * Describes the transport-level failures the v3 endpoints still express as HTTP
 * status codes, which are the ones about the *request* rather than the plan.
 * `426` is the one clients must render distinctly: it means this build is too
 * old for the server's protocol and no amount of retrying will help.
 *
 * `phase` says whether the request started playback or changed a stream that
 * already plays, so a server error on a replan is not reported as a failure to
 * start.
 */
export function describePlaybackTransportError(
  error: unknown,
  phase: "start" | "update" = "start",
): PlaybackPolicyErrorDescription | null {
  if (!(error instanceof PlayerFetchError)) {
    return null;
  }

  if (error.status === 426 || error.code === "client_upgrade_required") {
    return {
      title: "Update required",
      message:
        "This server speaks a newer playback protocol than this app. Reload the page to pick up the current version.",
    };
  }

  if (error.code === "playback_session_not_found") {
    return {
      title: "Playback session expired",
      message: "This playback session is no longer active. Start it again to keep watching.",
    };
  }

  if (error.status === 404) {
    return {
      title: "This item is no longer available",
      message:
        "The file needed to play this item can't be found right now. Go back and try another version if one is available.",
    };
  }

  if (error.status === 401) {
    return {
      title: "Authentication required",
      message: "Your sign-in session has expired. Sign in again to continue playback.",
    };
  }

  if (error.status === 403) {
    return {
      title: "Playback unavailable",
      message: "You do not have permission to play this item.",
    };
  }

  if (error.status >= 500) {
    return {
      title: "Playback unavailable",
      message:
        phase === "start"
          ? "Vio could not start playback right now. Please try again."
          : "Vio could not update playback right now. Please try again.",
    };
  }

  return null;
}
