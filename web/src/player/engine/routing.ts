import type { FailureV3, PlanV3 } from "../protocol-v3";
import type { PlayerAudioTrack } from "../types";
import type { EngineCapabilities } from "./capabilities";
import type { EngineSourceSpec, EngineTransfer } from "./source";

/** What the browser's own media element accepts for an untouched original. */
export interface NativeOriginalSupport {
  containers: string[];
  videoCodecs: string[];
  audioCodecs: string[];
}

export type OriginalPlaybackRoute =
  | { kind: "native" }
  | { kind: "engine"; source: EngineSourceSpec }
  | { kind: "unsupported"; failure: FailureV3 };

/** Typed failure the server's replan ladder records against this plan. */
export const ENGINE_UNSUPPORTED_SOURCE = "browser_engine_unsupported_source";

function lower(value: string | undefined | null): string {
  return (value ?? "").trim().toLowerCase();
}

/** The audio ordinal a media element plays when it is not told otherwise. */
export function containerDefaultAudioOrdinal(audioTracks: readonly PlayerAudioTrack[]): number {
  const index = audioTracks.findIndex((track) => track.default === true);
  return index >= 0 ? index : 0;
}

/**
 * Maps a plan's dynamic range onto the transfer the engine presents, or null
 * when no route can present it. Dolby Vision is played through its
 * standards-compatible base layer: Profile 8.1 and Profile 7 carry HDR10, 8.4
 * carries HLG and 8.2 carries SDR. Profile 5 has no compatible base layer.
 */
export function engineTransferForPlan(plan: PlanV3): EngineTransfer | null {
  const range = lower(plan.effective_recipe.dynamic_range || plan.source.dynamic_range);
  switch (range) {
    case "":
    case "sdr":
      return "sdr";
    case "hdr10":
    case "hdr10_plus":
    case "hdr_unknown":
      return "pq";
    case "hlg":
      return "hlg";
    case "dolby_vision":
      switch (plan.source.dv_bl_compat_id) {
        case 1:
        case 6:
          return "pq";
        case 4:
          return "hlg";
        case 2:
          return "sdr";
        default:
          return null;
      }
    default:
      return null;
  }
}

function isAbsoluteUrl(url: string): boolean {
  return url.startsWith("http://") || url.startsWith("https://");
}

/**
 * The URL the engine stripes range reads on, or undefined for the stock
 * source. The server names it in `parallel_range_url`; a plan without one is
 * never striped. The URL authorizes itself, so it is used exactly as given and
 * no token is added to it. The server names one of three:
 *
 * 1. The plan's own stream on the page's origin, as a reference relative to
 *    the API root. The engine stripes on `streamUrl`, and only when the page's
 *    origin answers over HTTP/1.1, where the browser opens several
 *    connections. Over HTTP/2 or HTTP/3 the ranges would share one.
 * 2. The proxy node's URL the plan already uses. The engine stripes on it.
 * 3. The same stream on the administrator's media origin. A URL that does not
 *    parse gives no stripe URL.
 */
function parallelRangeUrlFor(
  plan: PlanV3,
  streamUrl: string,
  navigationProtocol: string,
): string | undefined {
  const named = plan.stream.parallel_range_url;
  if (!named) return undefined;
  if (!isAbsoluteUrl(named)) {
    return !isAbsoluteUrl(streamUrl) && navigationProtocol === "http/1.1" ? streamUrl : undefined;
  }
  if (named === plan.stream.url) return streamUrl;
  try {
    new URL(named);
  } catch {
    // Go's URL parser accepts origins, such as `https://a<b.example`, that `new URL()` rejects.
    return undefined;
  }
  return named;
}

/**
 * The protocol the page's origin answers on: the navigation entry's when it has one, else the
 * `nextHopProtocol` of the newest same-origin resource that has one. A repeat load can leave the
 * navigation entry's protocol empty while the page's resources still report it. A resource entry
 * names the requested URL but reports the protocol of the host it ended on, so an entry that
 * followed a redirect (`redirectStart > 0`) can carry another host's protocol under a same-origin
 * name, as a stored poster does when it redirects to its storage URL; those entries are skipped.
 * Entries whose name is not a URL are skipped too.
 */
export function pageOriginProtocol(
  navigationProtocol: string,
  resources: readonly {
    name: string;
    nextHopProtocol: string;
    startTime: number;
    redirectStart: number;
  }[],
  origin: string,
): string {
  if (navigationProtocol !== "") return navigationProtocol;
  let newest: { nextHopProtocol: string; startTime: number } | undefined;
  for (const resource of resources) {
    if (resource.nextHopProtocol === "" || resource.redirectStart > 0) continue;
    if (newest && resource.startTime <= newest.startTime) continue;
    try {
      if (new URL(resource.name).origin !== origin) continue;
    } catch {
      continue;
    }
    newest = resource;
  }
  return newest?.nextHopProtocol ?? "";
}

function unsupported(message: string): OriginalPlaybackRoute {
  return { kind: "unsupported", failure: { classification: ENGINE_UNSUPPORTED_SOURCE, message } };
}

/**
 * Chooses who plays an `original_http` plan: the media element when it can
 * take the file as it is, otherwise the decode engine, otherwise nobody — in
 * which case the typed failure lets the server replan onto a packaged route.
 *
 * The media element wins whenever it can, because it decodes in hardware with
 * the browser's own A/V sync. It cannot pick an audio track, present an HDR
 * source on a page it does not control, or decode the formats the engine
 * exists for.
 */
export function resolveOriginalPlaybackRoute(input: {
  plan: PlanV3;
  streamUrl: string;
  native: NativeOriginalSupport;
  engine: EngineCapabilities | null;
  audioTracks: readonly PlayerAudioTrack[];
  /** The protocol the page's origin answers on (see `pageOriginProtocol`); empty when unknown. */
  navigationProtocol?: string;
}): OriginalPlaybackRoute {
  const { plan, streamUrl, native, engine, audioTracks } = input;
  const parallelRangeUrl = parallelRangeUrlFor(plan, streamUrl, input.navigationProtocol ?? "");
  const container = lower(plan.source.container || plan.stream.container);
  const videoCodec = lower(plan.effective_recipe.video_codec || plan.source.video_codec);
  const audioCodec = lower(plan.effective_recipe.audio_codec || plan.source.audio_codec);
  const defaultOrdinal = containerDefaultAudioOrdinal(audioTracks);
  const audioOrdinal = plan.selected_tracks.audio?.index ?? defaultOrdinal;
  const transfer = engineTransferForPlan(plan);

  // An HDR original the plan validated for this display plays natively, in
  // HDR; the engine would tone-map it down to SDR.
  const claims = plan.claims.video;
  const nativeRange =
    transfer === "sdr" ||
    (transfer === "hlg" && claims.hlg) ||
    (transfer === "pq" && (claims.hdr10 || claims.hdr10_plus || claims.dolby_vision));
  const nativePlays =
    native.containers.includes(container) &&
    native.videoCodecs.includes(videoCodec) &&
    nativeRange &&
    (audioCodec === "" || native.audioCodecs.includes(audioCodec)) &&
    audioOrdinal === defaultOrdinal;
  if (nativePlays) return { kind: "native" };

  if (!engine) return unsupported("The browser cannot play this file as it is.");
  if (!engine.containers.includes(container)) {
    return unsupported(`The browser engine cannot read ${container || "this"} containers.`);
  }
  if (!engine.videoCodecs.includes(videoCodec)) {
    return unsupported(`This browser cannot decode ${videoCodec || "this"} video.`);
  }
  if (audioCodec !== "" && !engine.audioCodecs.includes(audioCodec)) {
    return unsupported(`The browser engine cannot decode ${audioCodec} audio.`);
  }
  if (transfer === null) {
    return unsupported("The browser engine cannot present this dynamic range.");
  }
  if (transfer !== "sdr" && !engine.hdr) {
    return unsupported("HDR needs a working WebGPU presenter, which this browser does not have.");
  }

  return {
    kind: "engine",
    source: {
      url: streamUrl,
      ...(parallelRangeUrl ? { parallelRangeUrl } : {}),
      headers: plan.stream.headers ?? {},
      container,
      durationSeconds: plan.source.duration_seconds ?? null,
      startSeconds: Math.max(0, plan.timeline.player_start_seconds),
      video: {
        codec: videoCodec,
        width: plan.source.width ?? 0,
        height: plan.source.height ?? 0,
        transfer,
        stripDolbyVision: lower(plan.source.dynamic_range) === "dolby_vision",
      },
      audio:
        audioCodec === ""
          ? null
          : {
              ordinal: audioOrdinal,
              codec: audioCodec,
              channels: plan.effective_recipe.audio_channels ?? plan.source.audio_channels ?? null,
            },
    },
  };
}
