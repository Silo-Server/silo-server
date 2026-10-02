/**
 * Builds the web client's half of the v3 playback contract: the `declared`-tier
 * capability block and the per-delivery-class context the server negotiates
 * against.
 *
 * The browser can only answer MIME support probes through
 * `MediaSource.isTypeSupported(...)` and `HTMLMediaElement.canPlayType(...)`, so
 * every claim here is `declared` evidence (see §3 of
 * `docs/architecture/playback-protocol-v3.md`). We deliberately never populate
 * `video_decode[]`: on `declared` the server matches the flat codec lists and
 * ignores the detail walk, and fabricating profile/level entries we cannot
 * observe would be exactly the dishonest attestation the tier exists to avoid.
 */

import {
  PROTOCOL_V3,
  type ClientCodecCapabilitiesV3,
  type ClientPlaybackContextV3,
  type DeliveryCapabilityV3,
  type DeliveryClassV3,
  type DeliverySubtitleCapabilitiesV3,
  type HDRCapabilitiesV3,
} from "./protocol-v3";
import { isSafariBrowserV3 } from "./utils/hlsEngine";

/** App version reported to the server for diagnostics. */
const WEB_APP_VERSION = "web";

/**
 * Subtitle capabilities of the web player, identical for every delivery class:
 * the server exposes external and embedded text as session-scoped sidecars,
 * which it renders as WebVTT or ASS via JASSUB (with container font
 * attachments). It has no bitmap subtitle renderer, so PGS/VOBSUB tracks must
 * still be burned in server-side.
 */
const WEB_SUBTITLE_CAPABILITIES: DeliverySubtitleCapabilitiesV3 = {
  embedded_text: true,
  sidecar_text: true,
  ass_styling: true,
  embedded_bitmap: false,
  sidecar_bitmap: false,
  font_attachments: true,
};

export interface HLSSupportProbe {
  supported: boolean;
  native: boolean;
}

/** Detects whether HLS is available and whether the media element handles it natively. */
export function detectHLSSupport(): HLSSupportProbe {
  if (typeof document !== "undefined") {
    try {
      const video = document.createElement("video");
      if (video.canPlayType("application/vnd.apple.mpegurl") !== "") {
        return { supported: true, native: true };
      }
    } catch {
      // Fall through to the hls.js/MSE probe.
    }
  }
  if (typeof MediaSource === "undefined") return { supported: false, native: false };
  try {
    // hls.js muxes into fMP4/TS; the baseline it requires is an MSE that can
    // take an mp4 segment at all.
    return {
      supported: MediaSource.isTypeSupported('video/mp4; codecs="avc1.42E01E"'),
      native: false,
    };
  } catch {
    return { supported: false, native: false };
  }
}

export interface WebCapabilityProbe {
  /** Container names the browser reported support for. */
  containers: string[];
  /** Video codec names the browser reported support for. */
  codecsVideo: string[];
  /** Video codecs supported specifically by direct media-element playback. */
  progressiveCodecsVideo: string[];
  /** Audio codec names the browser reported support for. */
  codecsAudio: string[];
  /** Audio codecs supported specifically inside progressive MP4 delivery. */
  progressiveCodecsAudio: string[];
  /** Best-effort screen-derived resolution ceiling. */
  maxResolution: string;
  /** Best-effort HDR display detection. */
  hdr: boolean;
  /** Structured HDR formats supported by the active browser output path. */
  hdrDetails: HDRCapabilitiesV3;
  /** Whether native HLS or hls.js can be used on this browser. */
  hls: boolean;
  /** Whether HLS is handled by the browser's native media element. */
  nativeHLS: boolean;
}

/**
 * Builds the `client_capabilities` block. Every list is a flat declaration;
 * `codecs_video_hardware` mirrors `codecs_video` because a browser exposes no
 * way to tell software from hardware decode, and on the `declared` tier the
 * server treats the two lists identically.
 */
export function buildClientCapabilitiesV3(probe: WebCapabilityProbe): ClientCodecCapabilitiesV3 {
  const codecsVideo = Array.from(new Set([...probe.codecsVideo, ...probe.progressiveCodecsVideo]));
  return {
    video_evidence: "declared",
    audio_evidence: "declared",
    codecs_video: codecsVideo,
    codecs_video_hardware: codecsVideo,
    codecs_audio: probe.codecsAudio,
    containers: probe.containers,
    max_resolution: probe.maxResolution,
    hdr: probe.hdr,
    hdr_details: probe.hdrDetails,
  };
}

function buildDeliveryCapability(
  probe: WebCapabilityProbe,
  overrides: Partial<DeliveryCapabilityV3>,
): DeliveryCapabilityV3 {
  return {
    enabled: true,
    supported_on_device: true,
    containers: probe.containers,
    video_codecs: probe.codecsVideo,
    audio_decode_codecs: probe.codecsAudio,
    // A browser cannot bitstream audio to a receiver, and passthrough claims
    // are only ever honoured under the `exact` tier anyway.
    audio_passthrough_codecs: [],
    hdr_details: probe.hdrDetails,
    subtitles: WEB_SUBTITLE_CAPABILITIES,
    features: [],
    // The stream URL carries its own token; the player reloads the source
    // rather than refreshing headers in place.
    auth_header_refresh: false,
    validated_claims: [],
    transformations: [],
    ...overrides,
  };
}

/**
 * Builds the `deliveries` map. `original_http` and `progressive` ride the
 * `<video>` element directly; `hls` uses hls.js where MSE is available and the
 * media element's native HLS implementation otherwise.
 */
export function buildDeliveriesV3(
  probe: WebCapabilityProbe,
  userAgent = typeof navigator !== "undefined" ? navigator.userAgent : "",
): Partial<Record<DeliveryClassV3, DeliveryCapabilityV3>> {
  const nonProgressiveHDRDetails: HDRCapabilitiesV3 = {
    ...probe.hdrDetails,
    // The Dolby Vision and HDR10 probes cover direct progressive playback
    // through the media element after Silo normalizes the sample entry. They
    // say nothing about an untouched original or hls.js' MediaSource path.
    hdr10: false,
    dolby_vision_profiles: [],
    dolby_vision_profile_levels: [],
  };
  delete nonProgressiveHDRDetails.hdr10_max_width;
  delete nonProgressiveHDRDetails.hdr10_max_height;
  delete nonProgressiveHDRDetails.hdr10_max_frame_rate;
  delete nonProgressiveHDRDetails.hdr10_max_bitrate_kbps;
  const nativeHLSPreferred = probe.nativeHLS && isSafariBrowserV3(userAgent);
  const progressiveHDRDetails = nativeHLSPreferred ? nonProgressiveHDRDetails : probe.hdrDetails;
  const hlsHDRDetails = nativeHLSPreferred ? probe.hdrDetails : nonProgressiveHDRDetails;
  const hlsVideoCodecs = nativeHLSPreferred ? probe.progressiveCodecsVideo : probe.codecsVideo;
  return {
    original_http: buildDeliveryCapability(probe, {
      hdr_details: nonProgressiveHDRDetails,
    }),
    progressive: buildDeliveryCapability(probe, {
      video_codecs: probe.progressiveCodecsVideo,
      audio_decode_codecs: probe.progressiveCodecsAudio,
      hdr_details: progressiveHDRDetails,
    }),
    hls: buildDeliveryCapability(probe, {
      supported_on_device: probe.hls,
      ...(probe.hls ? {} : { failure_reason: "media_source_extensions_unavailable" }),
      containers: ["hls"],
      video_codecs: hlsVideoCodecs,
      hdr_details: hlsHDRDetails,
    }),
  };
}

/**
 * The slice of the Network Information API we use. It is not in the DOM lib
 * and is absent in Safari and Firefox, so everything here is best effort and
 * every reader tolerates `undefined`.
 */
interface NetworkInformationLike {
  saveData?: boolean;
  downlink?: number;
  type?: string;
}

function networkInformation(): NetworkInformationLike | undefined {
  if (typeof navigator === "undefined") return undefined;
  return (navigator as Navigator & { connection?: NetworkInformationLike }).connection;
}

/**
 * Whether the user is on a connection they would rather not spend. Only
 * Data Saver is a real signal in a browser — cellular detection via
 * `connection.type` is unimplemented nearly everywhere — so we report exactly
 * that and let the server treat the absence as "unknown, assume unmetered".
 */
export function detectMeteredV3(): boolean {
  return networkInformation()?.saveData === true;
}

/**
 * Best-effort downlink estimate in kbps for the planner's ladder choice.
 *
 * `connection.downlink` is a coarse, rounded, deliberately fingerprint-resistant
 * number, so it is a hint and not a measurement. Returns undefined rather than a
 * fabricated value when the API is absent or the reading falls outside the
 * contract's 100..1_000_000 kbps bounds.
 */
export function detectBandwidthEstimateKbpsV3(): number | undefined {
  const downlinkMbps = networkInformation()?.downlink;
  if (typeof downlinkMbps !== "number" || !Number.isFinite(downlinkMbps)) return undefined;
  const kbps = Math.round(downlinkMbps * 1000);
  if (kbps < 100 || kbps > 1_000_000) return undefined;
  return kbps;
}

/** Coarse form factor, used by the server only for quirk lookups. */
function detectFormFactor(): string {
  if (typeof navigator === "undefined") return "desktop";
  const ua = navigator.userAgent.toLowerCase();
  if (/\b(smart-?tv|smarttv|googletv|appletv|hbbtv|netcast|webos|tizen)\b/.test(ua)) return "tv";
  if (/\b(ipad|tablet)\b/.test(ua)) return "tablet";
  if (/\b(iphone|ipod|android.*mobile|mobile)\b/.test(ua)) return "phone";
  return "desktop";
}

/** Bounded (≤128 chars) opaque platform detail; the server treats it as text. */
function boundedDetail(value: string | undefined): string | undefined {
  if (!value) return undefined;
  const trimmed = value.trim();
  if (trimmed.length === 0) return undefined;
  return trimmed.slice(0, 128);
}

/**
 * Builds `client_playback_context`. `output.output_context_id` is deliberately
 * omitted: the browser exposes no stable identity for the current output route,
 * and the server only ever compares that token for equality, so a synthesized
 * value would invalidate plans at random.
 */
export function buildClientPlaybackContextV3(probe: WebCapabilityProbe): ClientPlaybackContextV3 {
  const platformDetails: Record<string, string> = {};
  const userAgent = boundedDetail(typeof navigator !== "undefined" ? navigator.userAgent : "");
  if (userAgent) platformDetails["user_agent"] = userAgent;
  if (typeof window !== "undefined" && typeof window.devicePixelRatio === "number") {
    platformDetails["device_pixel_ratio"] = String(window.devicePixelRatio);
  }

  return {
    protocol_version: PROTOCOL_V3,
    form_factor: detectFormFactor(),
    app_version: WEB_APP_VERSION,
    device: {
      platform: "web",
      ...(Object.keys(platformDetails).length > 0 ? { platform_details: platformDetails } : {}),
    },
    output: { hdr_details: probe.hdrDetails },
    deliveries: buildDeliveriesV3(
      probe,
      typeof navigator !== "undefined" ? navigator.userAgent : "",
    ),
  };
}
