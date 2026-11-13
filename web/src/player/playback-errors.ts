import { serverText, t, type MessageKey } from "@/i18n";
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
 * The text is in the language of the moment the terminal arrived.
 */
export function describePlanTerminal(terminal: TerminalV3): PlaybackPolicyErrorDescription {
  switch (terminal.reason) {
    case "transcoding_disabled":
      return {
        title: t("web.player.error.transcodingDisabled.title"),
        message: t("web.player.error.transcodingDisabled.message"),
      };
    case "audio_transcoding_disabled":
      return {
        title: t("web.player.error.audioTranscodingDisabled.title"),
        message: t("web.player.error.audioTranscodingDisabled.message"),
      };
    case "source_unavailable":
      return {
        title: t("web.player.error.sourceUnavailable.title"),
        message: t("web.player.error.sourceUnavailable.message"),
      };
    case "source_unreadable":
      return {
        title: t("web.player.error.sourceUnreadable.title"),
        message: t("web.player.error.sourceUnreadable.message"),
      };
    case "source_metadata_incomplete":
      return {
        title: t("web.player.error.sourceMetadataIncomplete.title"),
        message: t("web.player.error.sourceMetadataIncomplete.message"),
      };
    case "client_hls_unsupported":
      return {
        title: t("web.player.error.clientHlsUnsupported.title"),
        message: t("web.player.error.clientHlsUnsupported.message"),
      };
    case "adaptation_exhausted":
    case "adaptation_unavailable":
      return {
        title: t("web.player.error.noPlayableVersion.title"),
        message: t("web.player.error.noPlayableVersion.message"),
      };
    case "no_alternate_version":
      return {
        title: t("web.player.error.noPlayableVersion.title"),
        // The server names the policy that ruled the source out (4K
        // transcoding disabled, for one); the generic sentence only covers a
        // missing message.
        message: terminalMessage(terminal, "web.player.error.noPlayableVersion.message"),
      };
    case "hdr_transcode_unsupported":
    case "dv_conversion_unsupported":
      return {
        title: t("web.player.error.hdrConversionUnsupported.title"),
        message: t("web.player.error.hdrConversionUnsupported.message"),
      };
    case "video_conversion_unsupported":
    case "audio_conversion_unsupported":
      return {
        title: t("web.player.error.conversionUnsupported.title"),
        message: t("web.player.error.conversionUnsupported.message"),
      };
    case "conversion_tool_unavailable":
    case "transcode_node_unavailable":
    case "transcode_node_capability_unavailable":
    case "transcode_start_failed":
      return {
        title: t("web.player.watch.playbackUnavailable"),
        // The server names which part of the conversion path failed (no
        // transcode node, settings unavailable, transport refused to start);
        // the generic sentence only covers a missing message.
        message: terminalMessage(terminal, "web.player.error.conversionUnavailable.message"),
      };
    case "capacity_unavailable":
      return {
        title: t("web.player.error.capacityUnavailable.title"),
        message: t("web.player.error.capacityUnavailable.message"),
      };
    // The server has room here: this account has used its allowance. The
    // server writes these for viewers, so every client shows the same advice.
    case "stream_limit_reached":
      return {
        title: t("web.player.error.streamLimitReached.title"),
        message: terminalMessage(terminal, "web.player.error.limitReached.message"),
      };
    case "transcode_limit_reached":
      return {
        title: t("web.player.error.transcodeLimitReached.title"),
        message: terminalMessage(terminal, "web.player.error.limitReached.message"),
      };
    case "session_expired":
      return {
        title: t("web.player.error.sessionExpired.title"),
        message: t("web.player.error.sessionExpired.message"),
      };
    case "policy_denied":
      return {
        title: t("web.player.watch.playbackUnavailable"),
        message: t("web.player.error.notPermitted.message"),
      };
    case "subtitle_burn_in_source_unsupported":
    case "subtitle_codec_unsupported":
    case "subtitle_conversion_unsupported":
    case "subtitle_track_invalid":
    case "subtitle_track_unavailable":
    case "subtitle_unavailable_in_version":
    case "subtitle_artifact_unavailable":
      return {
        title: t("web.player.watch.subtitleTrackUnusable"),
        // The server names the specific blocker (burn-in required, source
        // unsupported); the generic sentence only covers a missing message.
        message: terminalMessage(terminal, "web.player.error.subtitleUnusable.message"),
      };
    default:
      return {
        title: t("web.player.watch.playbackUnavailable"),
        // No client sentence names this reason, so the server's is shown as
        // sent in every language, as for an unknown problem type.
        message: terminal.message?.trim() || t("web.player.watch.startFailed"),
      };
  }
}

/**
 * What the viewer is told when a stream that was already playing lost its
 * connection to the server and the player could not get it back.
 */
export function connectionLostError(): PlaybackPolicyErrorDescription {
  return {
    title: t("web.player.connection.lostTitle"),
    message: t("web.player.error.connectionLost.message"),
  };
}

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
      title: t("web.player.error.upgradeRequired.title"),
      message: t("web.player.error.upgradeRequired.message"),
    };
  }

  if (error.code === "playback_session_not_found") {
    return {
      title: t("web.player.error.sessionExpired.title"),
      message: t("web.player.error.sessionExpired.message"),
    };
  }

  if (error.status === 404) {
    return {
      title: t("web.player.error.itemUnavailable.title"),
      message: t("web.player.error.itemUnavailable.message"),
    };
  }

  if (error.status === 401) {
    return {
      title: t("web.player.error.authenticationRequired.title"),
      message: t("web.player.error.authenticationRequired.message"),
    };
  }

  if (error.status === 403) {
    return {
      title: t("web.player.watch.playbackUnavailable"),
      message: t("web.player.error.notPermitted.message"),
    };
  }

  if (error.status >= 500) {
    return {
      title: t("web.player.watch.playbackUnavailable"),
      message:
        phase === "start"
          ? t("web.player.error.serverError.start")
          : t("web.player.error.serverError.update"),
    };
  }

  return null;
}

/**
 * The server's own sentence for a terminal reason this table names, shown as
 * sent in English. A translated interface shows the client's sentence for the
 * reason when its catalog has one, as v2 problems show `error.<type>` before
 * the server's detail.
 */
function terminalMessage(terminal: TerminalV3, id: MessageKey): string {
  const english = terminal.message?.trim();
  return english ? serverText(id, undefined, english) : t(id);
}
