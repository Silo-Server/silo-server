import { useMemo } from "react";
import { detectHLSSupport, type WebCapabilityProbe } from "../client-context-v3";

/** Maps our codec names to the MIME declarations browsers expose for them. */
const VIDEO_CODEC_MAP: Record<string, string> = {
  h264: "avc1.640028",
  hevc: "hev1.1.6.L120.90",
  av1: "av01.0.08M.08",
  vp9: "vp09.00.10.08",
};

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

  const maxResolution =
    typeof screen !== "undefined"
      ? detectMaxResolutionFromScreen(screen.width, screen.height)
      : "1080p";

  // HDR detection (best effort). Wrap matchMedia so it keeps its Window
  // receiver — invoking a detached reference throws in some browsers.
  const hdr = detectHDRFromMatchMedia(
    typeof matchMedia !== "undefined" ? (query) => matchMedia(query) : undefined,
  );

  return {
    containers,
    codecsVideo,
    codecsAudio,
    maxResolution,
    hdr,
    hls: detectHLSSupport(),
  };
}

/**
 * Memoizes the browser capability probe for the lifetime of the component.
 * Nothing it measures changes while the page is open.
 */
export function useCodecDetection(): WebCapabilityProbe {
  return useMemo(() => probeWebCapabilities(), []);
}
