import { useEffect, useState } from "react";
import { detectHLSSupport, type WebCapabilityProbe } from "../client-context-v3";

/** Maps our codec names to the MIME declarations browsers expose for them. */
const VIDEO_CODEC_MAP: Record<string, string> = {
  h264: "avc1.640028",
  hevc: "hev1.1.6.L120.90",
  av1: "av01.0.08M.08",
  vp9: "vp09.00.10.08",
};

// Silo's native Dolby Vision remux recipe preserves the DOVI configuration
// record under a dvh1 sample entry — the one Apple's HLS authoring spec calls
// for, and the only one Safari answers "probably" for. Probe exactly that
// shape: a browser that recognizes only dvhe could accept a claim here and
// then reject the dvh1 file the remux delivers, so a dvhe-only answer earns no
// claim and that browser keeps the validated HDR10 fallback instead.
// Level 6 covers the 2160p24 source class involved in the web regression.
const DOLBY_VISION_PROFILE_PROBES: Record<
  number,
  { mime: string; maxLevel: number; blCompatibilityIds?: number[] }
> = {
  5: { mime: 'video/mp4; codecs="dvh1.05.06"', maxLevel: 6 },
  // The MIME codec string identifies Profile 8 but not its base-layer
  // compatibility ID. Conservatively claim only the Profile 8.1 shape that
  // this regression and Safari's progressive remux path exercise.
  8: { mime: 'video/mp4; codecs="dvh1.08.06"', maxLevel: 6, blCompatibilityIds: [1] },
};

// Silo's Profile 7 fallback strips Dolby Vision metadata into a progressive
// MP4 whose video is a 2160p HEVC Main10 HDR10 base layer. Media Capabilities
// can query the codec, transfer function, gamut, and static metadata together,
// avoiding the old mistake of treating a generic HDR output query as proof of
// every HDR format.
// The strip remux labels its output hvc1 — the sample entry Apple requires and
// the one Safari answers for — so that is the only entry probed: an hev1-only
// answer is evidence for a file Silo never sends and earns no claim.
const HDR10_PROGRESSIVE_CONFIGURATION = {
  type: "file",
  video: {
    contentType: 'video/mp4; codecs="hvc1.2.4.L153.B0"',
    width: 3840,
    height: 2160,
    bitrate: 80_000_000,
    framerate: 24,
    colorGamut: "rec2020",
    transferFunction: "pq",
    hdrMetadataType: "smpteSt2086",
  },
} satisfies MediaDecodingConfiguration;

const AUDIO_CODEC_MAP: Record<string, string[]> = {
  aac: ['audio/mp4; codecs="mp4a.40.2"', 'video/mp4; codecs="mp4a.40.2"'],
  mp3: ["audio/mpeg"],
  opus: [
    'audio/mp4; codecs="opus"',
    'video/mp4; codecs="opus"',
    'audio/ogg; codecs="opus"',
    'audio/webm; codecs="opus"',
  ],
  vorbis: ['audio/ogg; codecs="vorbis"', 'audio/webm; codecs="vorbis"'],
  flac: ["audio/flac", 'audio/mp4; codecs="flac"', 'video/mp4; codecs="flac"'],
  ac3: ['audio/mp4; codecs="ac-3"', 'video/mp4; codecs="ac-3"'],
  eac3: ['audio/mp4; codecs="ec-3"', 'video/mp4; codecs="ec-3"'],
  dts: ['audio/mp4; codecs="dts+"', 'video/mp4; codecs="dts+"'],
};

// The scanner normalizes M4A/M4B to mp4. Standalone MP3, FLAC, and OGG keep
// their own container keys, so they need matching direct-play probes here.
const CONTAINER_MAP: Record<string, string[]> = {
  mp4: ['video/mp4; codecs="avc1.640028"', 'audio/mp4; codecs="mp4a.40.2"'],
  webm: ['video/webm; codecs="vp09.00.10.08"'],
  mkv: ['video/x-matroska; codecs="avc1.640028"'],
  mp3: ["audio/mpeg"],
  flac: ["audio/flac"],
  ogg: ["audio/ogg"],
};

export function detectMaxResolutionFromScreen(screenWidth: number, screenHeight: number): string {
  const screenH = Math.max(screenHeight, screenWidth);
  if (screenH >= 2160) return "2160p";
  if (screenH >= 1440) return "1080p";
  if (screenH >= 720) return "720p";
  return "480p";
}

/**
 * Detects HDR display support (best effort). Firefox's `dynamic-range` query
 * reflects the browser canvas and reports `standard` even on HDR displays;
 * the video plane is exposed via `video-dynamic-range` (Firefox 116+), so
 * accept either. Browsers treat unknown media features as non-matching, so
 * querying both is safe everywhere.
 */
export function detectHDRFromMatchMedia(matchMediaFn: typeof matchMedia | undefined): boolean {
  if (!matchMediaFn) return false;
  return (
    matchMediaFn("(dynamic-range: high)").matches ||
    matchMediaFn("(video-dynamic-range: high)").matches
  );
}

/**
 * Probes the exact HDR10 progressive shape produced by Silo's remux path.
 * Deliberately independent of the `dynamic-range` media query: that query
 * describes the active output, not the decoder, and browsers tone-map HDR
 * content onto SDR outputs.
 */
export async function probeHDR10PlaybackSupport(): Promise<boolean> {
  if (typeof navigator === "undefined" || !navigator.mediaCapabilities) return false;

  try {
    const result = await navigator.mediaCapabilities.decodingInfo(HDR10_PROGRESSIVE_CONFIGURATION);
    return result.supported && result.smooth;
  } catch {
    return false;
  }
}

function testMediaType(mime: string): boolean {
  if (typeof MediaSource !== "undefined") {
    try {
      if (MediaSource.isTypeSupported(mime)) return true;
    } catch {
      // Fall through to the media element probe.
    }
  }

  if (typeof document === "undefined") return false;
  try {
    return (
      document.createElement(mime.startsWith("audio/") ? "audio" : "video").canPlayType(mime) !== ""
    );
  } catch {
    return false;
  }
}

function testMediaElementType(mime: string): boolean {
  if (typeof document === "undefined") return false;
  try {
    // `maybe` only recognizes the container/type. Structured Dolby Vision
    // claims require the media element's definitive answer for the exact
    // sample entry.
    return document.createElement("video").canPlayType(mime) === "probably";
  } catch {
    return false;
  }
}

/**
 * Probes what this browser will admit to decoding.
 *
 * Every answer here comes from `MediaSource.isTypeSupported(...)` or
 * `HTMLMediaElement.canPlayType(...)`, which is why the v3 capability block
 * built from this probe is `declared` evidence and never claims hardware decode
 * detail it cannot observe. The screen-derived resolution and the HDR media
 * queries are hints about the *output*, not the decoder, and the server treats
 * them as such.
 */
export function probeWebCapabilities(): WebCapabilityProbe {
  const codecsVideo: string[] = [];
  const codecsAudio: string[] = [];
  const containers: string[] = [];

  // Test containers.
  for (const [name, mimeTypes] of Object.entries(CONTAINER_MAP)) {
    if (mimeTypes.some(testMediaType)) {
      containers.push(name);
    }
  }

  // Test video codecs (in mp4 container).
  for (const [name, codec] of Object.entries(VIDEO_CODEC_MAP)) {
    if (testMediaType(`video/mp4; codecs="${codec}"`)) {
      codecsVideo.push(name);
    }
  }

  // Test audio codecs.
  for (const [name, mimeTypes] of Object.entries(AUDIO_CODEC_MAP)) {
    if (mimeTypes.some(testMediaType)) {
      codecsAudio.push(name);
    }
  }

  // `screen` reports logical CSS pixels; a 2160p-class panel on a 2x display
  // measures 1080p without the device pixel ratio applied.
  const pixelRatio =
    typeof window !== "undefined" &&
    typeof window.devicePixelRatio === "number" &&
    Number.isFinite(window.devicePixelRatio) &&
    window.devicePixelRatio > 0
      ? window.devicePixelRatio
      : 1;
  const maxResolution =
    typeof screen !== "undefined"
      ? detectMaxResolutionFromScreen(screen.width * pixelRatio, screen.height * pixelRatio)
      : "1080p";

  // HDR detection (best effort). Wrap matchMedia so it keeps its Window
  // receiver — invoking a detached reference throws in some browsers.
  const hdr = detectHDRFromMatchMedia(
    typeof matchMedia !== "undefined" ? (query) => matchMedia(query) : undefined,
  );
  // Decoder capability and active-output HDR are separate facts: browsers
  // tone-map HDR content onto SDR outputs, and Safari 26 reports
  // `dynamic-range: standard` even on an XDR panel. Exact positive decode
  // evidence must not be discarded because the coarse output query says no, so
  // the sample-entry probes run unconditionally and `hdr` stays a best-effort
  // output signal only.
  const dolbyVisionProfiles = Object.entries(DOLBY_VISION_PROFILE_PROBES)
    .filter(([, probe]) => testMediaElementType(probe.mime))
    .map(([profile]) => Number(profile));
  const progressiveCodecsVideo = [...codecsVideo];
  if (dolbyVisionProfiles.length > 0 && !progressiveCodecsVideo.includes("hevc")) {
    // Every Dolby Vision profile probed above uses an HEVC base layer. The
    // planner requires the flat base-codec claim as well as the HDR profile,
    // but this media-element evidence must not leak into hls.js' MSE path.
    progressiveCodecsVideo.push("hevc");
  }
  const hdrDetails = {
    // Generic HDR output eligibility does not prove either static HDR format.
    // Only publish the exact Dolby Vision formats tested above.
    hdr10: false,
    hdr10_plus: false,
    hlg: false,
    dolby_vision_profiles: dolbyVisionProfiles,
    dolby_vision_profile_levels: dolbyVisionProfiles.map((profile) => {
      const profileProbe = DOLBY_VISION_PROFILE_PROBES[profile]!;
      return {
        profile,
        max_level: profileProbe.maxLevel,
        ...(profileProbe.blCompatibilityIds
          ? { bl_compatibility_ids: profileProbe.blCompatibilityIds }
          : {}),
      };
    }),
  };
  const hlsSupport = detectHLSSupport();

  return {
    containers,
    codecsVideo,
    progressiveCodecsVideo,
    codecsAudio,
    maxResolution,
    hdr,
    hdrDetails,
    hls: hlsSupport.supported,
    nativeHLS: hlsSupport.native,
  };
}

/**
 * Keeps the browser capability probe current with the active output route.
 * Moving a window between SDR and HDR displays can change the media-query
 * result without remounting the player, so refresh when either query changes.
 */
export interface SettledWebCapabilityProbe extends WebCapabilityProbe {
  /** Whether asynchronous format probes have joined the synchronous evidence. */
  settled: boolean;
}

export function useCodecDetection(): SettledWebCapabilityProbe {
  const [capabilities, setCapabilities] = useState<SettledWebCapabilityProbe>(() => ({
    ...probeWebCapabilities(),
    settled: false,
  }));

  useEffect(() => {
    let disposed = false;
    let probeGeneration = 0;
    const queries =
      typeof matchMedia === "undefined"
        ? []
        : [matchMedia("(dynamic-range: high)"), matchMedia("(video-dynamic-range: high)")];
    const refresh = () => {
      const generation = ++probeGeneration;
      const next = probeWebCapabilities();
      setCapabilities({ ...next, settled: false });

      void probeHDR10PlaybackSupport().then((hdr10) => {
        if (disposed || generation !== probeGeneration) return;
        setCapabilities((current) => ({
          ...current,
          settled: true,
          // The exact HDR10 query proves the HEVC Main10 base codec for the
          // progressive MP4 route even when the separate generic HEVC probe was
          // rejected. Keep that evidence scoped away from original and HLS.
          progressiveCodecsVideo:
            !hdr10 || current.progressiveCodecsVideo.includes("hevc")
              ? current.progressiveCodecsVideo
              : [...current.progressiveCodecsVideo, "hevc"],
          hdrDetails: {
            ...current.hdrDetails,
            ...(hdr10
              ? {
                  hdr10: true,
                  hdr10_max_width: 3840,
                  hdr10_max_height: 2160,
                  hdr10_max_frame_rate: 24,
                  hdr10_max_bitrate_kbps: 80_000,
                }
              : {}),
          },
        }));
      });
    };
    refresh();
    for (const query of queries) {
      if (typeof query.addEventListener === "function") query.addEventListener("change", refresh);
      else query.addListener?.(refresh);
    }
    return () => {
      disposed = true;
      for (const query of queries) {
        if (typeof query.removeEventListener === "function")
          query.removeEventListener("change", refresh);
        else query.removeListener?.(refresh);
      }
    };
  }, []);

  return capabilities;
}
