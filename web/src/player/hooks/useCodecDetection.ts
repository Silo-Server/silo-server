import { useMemo } from "react";
import type { ClientCodecCapabilities, PlayerFileVersion } from "../types";

/** Maps our codec names to MIME codec strings for MediaSource.isTypeSupported(). */
const VIDEO_CODEC_MAP: Record<string, string> = {
  h264: "avc1.640028",
  hevc: "hev1.1.6.L120.90",
  av1: "av01.0.08M.08",
  vp9: "vp09.00.10.08",
};

const AUDIO_CODEC_MAP: Record<string, string> = {
  aac: "mp4a.40.2",
  opus: "opus",
  flac: "flac",
  ac3: "ac-3",
  eac3: "ec-3",
  dts: "dts+",
};

const CONTAINER_MAP: Record<string, string> = {
  mp4: "video/mp4",
  webm: "video/webm",
  mkv: "video/x-matroska",
};

export const RESOLUTION_ORDER: Record<string, number> = {
  "2160p": 4,
  "1080p": 3,
  "720p": 2,
  "480p": 1,
  "420p": 0,
};

/** Maps the quality_preference setting values to resolution strings. */
export const QUALITY_TO_RESOLUTION: Record<string, string> = {
  "4k": "2160p",
  "2160p": "2160p",
  "1080p": "1080p",
  "720p": "720p",
  "480p": "480p",
  "420p": "420p",
};

export interface PlaybackEnvironmentSnapshot {
  screenWidth: number;
  screenHeight: number;
  innerWidth: number;
  innerHeight: number;
  outerWidth: number;
  outerHeight: number;
  devicePixelRatio: number;
  maxResolution: string;
}

export function detectMaxResolutionFromScreen(screenWidth: number, screenHeight: number): string {
  const screenH = Math.max(screenHeight, screenWidth);
  if (screenH >= 2160) return "2160p";
  if (screenH >= 1440) return "1080p";
  if (screenH >= 720) return "720p";
  return "480p";
}

export function getPlaybackEnvironmentSnapshot(): PlaybackEnvironmentSnapshot | null {
  if (
    typeof window === "undefined" ||
    typeof screen === "undefined" ||
    typeof window.screen === "undefined"
  ) {
    return null;
  }

  return {
    screenWidth: window.screen.width,
    screenHeight: window.screen.height,
    innerWidth: window.innerWidth,
    innerHeight: window.innerHeight,
    outerWidth: window.outerWidth,
    outerHeight: window.outerHeight,
    devicePixelRatio: window.devicePixelRatio,
    maxResolution: detectMaxResolutionFromScreen(window.screen.width, window.screen.height),
  };
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

function testCodec(mimeWithCodec: string): boolean {
  if (typeof MediaSource === "undefined") return false;
  try {
    return MediaSource.isTypeSupported(mimeWithCodec);
  } catch {
    return false;
  }
}

/**
 * Scores versions against resume hints (resolution > HDR > codec).
 * Returns the best-matching version, or null if no hints or no versions.
 */
export function matchByTraits(
  versions: PlayerFileVersion[],
  hints: {
    lastResolution?: string;
    lastHDR?: boolean;
    lastCodecVideo?: string;
    lastEditionKey?: string;
  },
): PlayerFileVersion | null {
  if (versions.length === 0) return null;
  if (
    !hints.lastResolution &&
    hints.lastHDR == null &&
    !hints.lastCodecVideo &&
    !hints.lastEditionKey
  )
    return null;

  const candidates =
    hints.lastEditionKey && versions.some((v) => v.edition_key === hints.lastEditionKey)
      ? versions.filter((v) => v.edition_key === hints.lastEditionKey)
      : versions;

  let best: PlayerFileVersion | null = null;
  let bestScore = -1;

  for (const v of candidates) {
    let score = 0;
    if (hints.lastEditionKey && v.edition_key === hints.lastEditionKey) score += 1000;
    if (hints.lastResolution && v.resolution === hints.lastResolution) score += 100;
    if (hints.lastHDR != null && v.hdr === hints.lastHDR) score += 10;
    if (hints.lastCodecVideo && v.codec_video === hints.lastCodecVideo) score += 1;
    if (score > bestScore) {
      bestScore = score;
      best = v;
    }
  }

  return bestScore > 0 ? best : null;
}

/**
 * Detects the browser's codec capabilities and provides a function to select
 * the best file version from a list.
 */
export function useCodecDetection() {
  const capabilities = useMemo<ClientCodecCapabilities>(() => {
    const codecs_video: string[] = [];
    const codecs_audio: string[] = [];
    const containers: string[] = [];

    // Test containers.
    for (const [name, mime] of Object.entries(CONTAINER_MAP)) {
      if (testCodec(`${mime}; codecs="avc1.640028"`)) {
        containers.push(name);
      }
    }

    // Test video codecs (in mp4 container).
    for (const [name, codec] of Object.entries(VIDEO_CODEC_MAP)) {
      if (testCodec(`video/mp4; codecs="${codec}"`)) {
        codecs_video.push(name);
      }
    }

    // Test audio codecs.
    for (const [name, codec] of Object.entries(AUDIO_CODEC_MAP)) {
      if (testCodec(`audio/mp4; codecs="${codec}"`) || testCodec(`video/mp4; codecs="${codec}"`)) {
        codecs_audio.push(name);
      }
    }

    // Detect max resolution via reported screen dimensions.
    const max_resolution = detectMaxResolutionFromScreen(screen.width, screen.height);

    // HDR detection (best effort). Wrap matchMedia so it keeps its Window
    // receiver — invoking a detached reference throws in some browsers.
    const hdr = detectHDRFromMatchMedia(
      typeof matchMedia !== "undefined" ? (query) => matchMedia(query) : undefined,
    );

    return { codecs_video, codecs_audio, containers, max_resolution, hdr };
  }, []);

  /**
   * Selects the best file version from a list.
   * Prefers direct-playable, then user's quality preference, then highest
   * resolution, then smallest file.
   */
  function selectBestVersion(
    versions: PlayerFileVersion[],
    qualityPreference?: string | null,
  ): PlayerFileVersion | null {
    if (versions.length === 0) return null;

    const targetRes =
      qualityPreference && qualityPreference !== "auto"
        ? QUALITY_TO_RESOLUTION[qualityPreference]
        : null;
    const targetOrder = targetRes ? (RESOLUTION_ORDER[targetRes] ?? 0) : 0;

    const scored = versions.map((v) => {
      const videoOk = capabilities.codecs_video.includes(v.codec_video);
      const audioOk = capabilities.codecs_audio.includes(v.codec_audio);
      const containerOk = capabilities.containers.includes(v.container);

      // Score: 3 = direct, 2 = remux, 1 = transcode
      let compatibility = 1;
      if (videoOk && audioOk && containerOk) compatibility = 3;
      else if (videoOk && audioOk) compatibility = 2;

      const resOrder = RESOLUTION_ORDER[v.resolution] ?? 0;

      // Preference match: 2 = exact, 1 = closest below target, 0 = above/no pref
      let preferenceMatch = 0;
      if (targetRes) {
        if (v.resolution === targetRes) preferenceMatch = 2;
        else if (resOrder < targetOrder) preferenceMatch = 1;
      }

      return { version: v, compatibility, preferenceMatch, resOrder };
    });

    scored.sort((a, b) => {
      if (a.compatibility !== b.compatibility) return b.compatibility - a.compatibility;
      if (a.preferenceMatch !== b.preferenceMatch) return b.preferenceMatch - a.preferenceMatch;
      if (a.resOrder !== b.resOrder) return b.resOrder - a.resOrder;
      return a.version.file_size - b.version.file_size;
    });

    return scored[0]?.version ?? null;
  }

  return { capabilities, selectBestVersion };
}
