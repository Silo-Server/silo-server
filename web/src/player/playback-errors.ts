import { PlayerFetchError } from "./player-fetch";
import type { TerminalV3 } from "./protocol-v3";

export interface PlaybackPolicyErrorDescription {
  title: string;
  message: string;
}

/**
 * Turns a v3 terminal into something a person can act on.
 *
 * Under protocol v3 a refused plan is not an HTTP error: the start endpoint
 * answers `201` and the replan endpoint `200`, and the decision lives in the
 * body's `terminal`. The status code only describes the request. So the whole
 * "playback was refused" surface is reason-keyed, and the server's own
 * `terminal.message` is the fallback for reasons this table does not name.
 */
export function describePlanTerminal(terminal: TerminalV3): PlaybackPolicyErrorDescription {
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
    case "source_metadata_incomplete":
      return {
        title: "This file hasn't finished scanning",
        message:
          "Silo doesn't know enough about this file yet to plan playback. Try again once the scan finishes.",
      };
    case "client_hls_unsupported":
      return {
        title: "This browser can't play the stream",
        message:
          "Playing this file needs HLS, which this browser doesn't support. Try a different browser.",
      };
    case "adaptation_exhausted":
    case "adaptation_unavailable":
    case "no_alternate_version":
      return {
        title: "No playable version found",
        message:
          "Silo couldn't find a way to play this file on this device. Try another version if one is available.",
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
        message: "Silo can't convert this file into something this device can play.",
      };
    case "conversion_tool_unavailable":
    case "transcode_node_unavailable":
    case "transcode_node_capability_unavailable":
    case "transcode_start_failed":
      return {
        title: "Playback unavailable",
        message: "The server couldn't start converting this file. Please try again.",
      };
    case "capacity_unavailable":
      return {
        title: "The server is busy",
        message:
          "There's no capacity to convert this file right now. Please try again in a moment.",
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
        message:
          "Silo couldn't prepare the selected subtitles for this device. Try a different track.",
      };
    default:
      return {
        title: "Playback unavailable",
        message: terminal.message?.trim() || "Silo could not start playback.",
      };
  }
}

/**
 * Describes the transport-level failures the v3 endpoints still express as HTTP
 * status codes, which are the ones about the *request* rather than the plan.
 * `426` is the one clients must render distinctly: it means this build is too
 * old for the server's protocol and no amount of retrying will help.
 */
export function describePlaybackTransportError(
  error: unknown,
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

  if (error.status === 401 || error.status === 403) {
    return {
      title: "Playback unavailable",
      message: "You do not have permission to play this item.",
    };
  }

  if (error.status >= 500) {
    return {
      title: "Playback unavailable",
      message: "Silo could not start playback right now. Please try again.",
    };
  }

  return null;
}
