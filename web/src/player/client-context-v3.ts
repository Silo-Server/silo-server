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
} from "./protocol-v3";

/** App version reported to the server for diagnostics. */
const WEB_APP_VERSION = "web";

/**
 * Subtitle capabilities of the web player, identical for every delivery class:
 * it renders sidecar text (WebVTT and ASS via JASSUB, with container font
 * attachments) but has no bitmap subtitle renderer, so PGS/VOBSUB tracks must
 * be burned in server-side.
 */
const WEB_SUBTITLE_CAPABILITIES: DeliverySubtitleCapabilitiesV3 = {
  embedded_text: false,
  sidecar_text: true,
  ass_styling: true,
  embedded_bitmap: false,
  sidecar_bitmap: false,
  font_attachments: true,
};

/** Detects whether either hls.js or the native media element can play HLS. */
export function detectHLSSupport(): boolean {
  if (typeof document !== "undefined") {
    try {
      const video = document.createElement("video");
      if (video.canPlayType("application/vnd.apple.mpegurl") !== "") return true;
    } catch {
      // Fall through to the hls.js/MSE probe.
    }
  }
  if (typeof MediaSource === "undefined") return false;
  try {
    // hls.js muxes into fMP4/TS; the baseline it requires is an MSE that can
    // take an mp4 segment at all.
    return MediaSource.isTypeSupported('video/mp4; codecs="avc1.42E01E"');
  } catch {
    return false;
  }
}

export interface WebCapabilityProbe {
  /** Container names the browser reported support for. */
  containers: string[];
  /** Video codec names the browser reported support for. */
  codecsVideo: string[];
  /** Audio codec names the browser reported support for. */
  codecsAudio: string[];
  /** Best-effort screen-derived resolution ceiling. */
  maxResolution: string;
  /** Best-effort HDR display detection. */
  hdr: boolean;
  /** Whether hls.js can be used on this browser. */
  hls: boolean;
}

/**
 * Builds the `client_capabilities` block. Every list is a flat declaration;
 * `codecs_video_hardware` mirrors `codecs_video` because a browser exposes no
 * way to tell software from hardware decode, and on the `declared` tier the
 * server treats the two lists identically.
 */
export function buildClientCapabilitiesV3(probe: WebCapabilityProbe): ClientCodecCapabilitiesV3 {
  return {
    video_evidence: "declared",
    audio_evidence: "declared",
    codecs_video: probe.codecsVideo,
    codecs_video_hardware: probe.codecsVideo,
    codecs_audio: probe.codecsAudio,
    containers: probe.containers,
    max_resolution: probe.maxResolution,
    hdr: probe.hdr,
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
): Partial<Record<DeliveryClassV3, DeliveryCapabilityV3>> {
  return {
    original_http: buildDeliveryCapability(probe, {}),
    progressive: buildDeliveryCapability(probe, {}),
    hls: buildDeliveryCapability(probe, {
      supported_on_device: probe.hls,
      ...(probe.hls ? {} : { failure_reason: "media_source_extensions_unavailable" }),
      containers: ["hls"],
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
    output: {},
    deliveries: buildDeliveriesV3(probe),
  };
}
