import { useEffect, useState } from "react";
import { detectHLSSupport, type WebCapabilityProbe } from "../client-context-v3";
import { isFirefoxUserAgent } from "../utils/browser";

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

/** A small SDR HEVC stream that any hardware HEVC decoder plays in real time. */
const HEVC_REFERENCE_CONFIGURATION = {
  type: "file",
  video: {
    contentType: 'video/mp4; codecs="hvc1.1.6.L90.90"',
    width: 640,
    height: 360,
    bitrate: 1_000_000,
    framerate: 24,
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

/** The `max_resolution` reported until, or without, a Media Capabilities answer. */
const UNPROBED_MAX_RESOLUTION = "2160p";

/**
 * How long a playback start waits for the decoder size probe. Starts already
 * wait for the HDR10 probe; a slow size answer must not add more than this.
 */
const MAX_DECODE_RESOLUTION_WAIT_MS = 2_000;

function mp4VideoConfiguration(
  codec: string,
  width: number,
  height: number,
  bitrate: number,
): MediaDecodingConfiguration {
  return {
    type: "file",
    video: { contentType: `video/mp4; codecs="${codec}"`, width, height, bitrate, framerate: 30 },
  };
}

/**
 * A 2160p30 query for each codec the server may direct play, each paired with
 * a 720p30 stream of the same codec for the power-efficient exception in
 * decodesInRealTime.
 */
const UHD_DECODE_PROBES = [
  // H.264 High, levels 5.1 and 3.1.
  { uhd: "avc1.640033", reference: "avc1.64001F" },
  // HEVC Main, levels 5.0 and 3.1.
  { uhd: "hvc1.1.6.L150.B0", reference: "hvc1.1.6.L93.B0" },
  // VP9 profile 0, levels 5.1 and 3.1.
  { uhd: "vp09.00.51.08", reference: "vp09.00.31.08" },
  // AV1 Main, levels 5.0 and 3.1.
  { uhd: "av01.0.12M.08", reference: "av01.0.05M.08" },
].map(({ uhd, reference }) => ({
  uhd: mp4VideoConfiguration(uhd, 3840, 2160, 20_000_000),
  reference: mp4VideoConfiguration(reference, 1280, 720, 2_000_000),
}));

const H264_1080P_CONFIGURATION = mp4VideoConfiguration("avc1.640028", 1920, 1080, 10_000_000);

/**
 * Whether the browser decodes a configuration in real time.
 *
 * The answer has to be smooth, with one exception. iOS Safari answers
 * `smooth: false` for every HEVC configuration, even a small SDR stream that
 * any hardware decoder plays in real time, while `powerEfficient: true` shows
 * its hardware decoder. Where the browser answers that way for the small
 * same-codec reference stream too, its smooth flag says nothing, and a
 * power-efficient decoder is trusted. Elsewhere `smooth: false` stands:
 * Chromium, for one, derives it from frames dropped in earlier playback.
 */
async function decodesInRealTime(
  configuration: MediaDecodingConfiguration,
  reference: MediaDecodingConfiguration,
): Promise<boolean> {
  const result = await navigator.mediaCapabilities.decodingInfo(configuration);
  if (!result.supported) return false;
  if (result.smooth) return true;
  if (!result.powerEfficient) return false;
  const referenceResult = await navigator.mediaCapabilities.decodingInfo(reference);
  return referenceResult.supported && !referenceResult.smooth && referenceResult.powerEfficient;
}

/**
 * Finds the largest video size this browser decodes, for `max_resolution`.
 *
 * Auto quality treats `max_resolution` as a device limit and transcodes
 * anything taller, so it describes the decoder, never the screen: the browser
 * scales a 1080p picture into a 720p window itself. Web evidence is `declared`
 * with flat codec lists, so this is the only size bound the web sends.
 *
 * One value bounds every codec, so the 4K queries cover H.264, HEVC, VP9 and
 * AV1 in parallel, and a browser that decodes any of them in real time
 * (decodesInRealTime) reports 2160p. The `file` type matches the media element
 * loading a direct-play original. Only when all four fail does a browser that
 * decodes 1080p H.264 get a 1080p cap. With no usable answer (the API is
 * missing, the 1080p query throws, or the browser rejects H.264 even at
 * 1080p, which says nothing about size) it reports 2160p: the codec lists
 * still gate decoding, and the server's failure recovery replans a stream
 * that will not play.
 */
export async function probeMaxDecodeResolution(): Promise<string> {
  if (typeof navigator === "undefined" || !navigator.mediaCapabilities) {
    return UNPROBED_MAX_RESOLUTION;
  }

  try {
    // A query the browser rejects (some Safari versions throw on codec
    // strings they cannot parse) means that codec does not decode, and must
    // not hide the other codecs' answers.
    const decodes4K = await Promise.allSettled(
      UHD_DECODE_PROBES.map((probe) => decodesInRealTime(probe.uhd, probe.reference)),
    );
    if (decodes4K.some((answer) => answer.status === "fulfilled" && answer.value)) return "2160p";
    const fhd = await navigator.mediaCapabilities.decodingInfo(H264_1080P_CONFIGURATION);
    return fhd.supported ? "1080p" : UNPROBED_MAX_RESOLUTION;
  } catch {
    return UNPROBED_MAX_RESOLUTION;
  }
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
 * content onto SDR outputs. The shape has to decode in real time
 * (decodesInRealTime), checked against a small SDR HEVC reference stream.
 */
export async function probeHDR10PlaybackSupport(): Promise<boolean> {
  if (typeof navigator === "undefined" || !navigator.mediaCapabilities) return false;

  try {
    return await decodesInRealTime(HDR10_PROGRESSIVE_CONFIGURATION, HEVC_REFERENCE_CONFIGURATION);
  } catch {
    return false;
  }
}

let initialHDR10Probe: Promise<boolean> | null = null;
let maxDecodeResolutionProbe: Promise<string> | null = null;
let maxDecodeResolutionAnswer: string | null = null;

/**
 * The decoder's size limit does not change with the output route, so unlike
 * the HDR10 probe it runs once for the lifetime of the app.
 */
function startMaxDecodeResolutionProbe(): Promise<string> {
  maxDecodeResolutionProbe ??= probeMaxDecodeResolution().then((resolution) => {
    maxDecodeResolutionAnswer = resolution;
    return resolution;
  });
  return maxDecodeResolutionProbe;
}

/**
 * The size probe's answer, or 2160p if it has not answered within
 * MAX_DECODE_RESOLUTION_WAIT_MS. Once the probe has answered, even late, its
 * answer is used without waiting, so an output-change refresh settles as
 * soon as its HDR10 re-probe does.
 */
function maxDecodeResolutionWithDeadline(): Promise<string> {
  const probe = startMaxDecodeResolutionProbe();
  if (maxDecodeResolutionAnswer !== null) return Promise.resolve(maxDecodeResolutionAnswer);
  return new Promise((resolve) => {
    const deadline = setTimeout(
      () => resolve(UNPROBED_MAX_RESOLUTION),
      MAX_DECODE_RESOLUTION_WAIT_MS,
    );
    void probe.then((resolution) => {
      clearTimeout(deadline);
      resolve(resolution);
    });
  });
}

/**
 * Starts the expensive Media Capabilities queries once for the lifetime of the
 * app. The shell calls this before playback is requested so opening a video
 * does not have to wait several seconds for the browser's first decoder probe.
 */
export function prewarmCodecDetection(): Promise<boolean> {
  void startMaxDecodeResolutionProbe();
  initialHDR10Probe ??= probeHDR10PlaybackSupport();
  return initialHDR10Probe;
}

export function resetCodecDetectionForTests(): void {
  initialHDR10Probe = null;
  maxDecodeResolutionProbe = null;
  maxDecodeResolutionAnswer = null;
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
 * detail it cannot observe. The HDR media queries are hints about the
 * *output*, not the decoder, and the server treats them as such.
 * `maxResolution` starts at 2160p until useCodecDetection joins the
 * asynchronous decoder probe (see probeMaxDecodeResolution).
 */
export function probeWebCapabilities(): WebCapabilityProbe {
  const codecsVideo: string[] = [];
  const codecsAudio: string[] = [];
  const progressiveCodecsAudio: string[] = [];
  const containers: string[] = [];
  const isFirefox =
    typeof navigator !== "undefined" && isFirefoxUserAgent(navigator.userAgent ?? "");

  // Test containers.
  for (const [name, mimeTypes] of Object.entries(CONTAINER_MAP)) {
    // Firefox 145+ reports native Matroska support, but its demuxer still
    // requires the complete resource before starting common audio+video MKVs
    // (Mozilla bug 2000420). Advertising that container turns direct play into
    // a full-file download. Keep the codec claims so the planner can select a
    // codec-copy remux instead.
    if (name === "mkv" && isFirefox) continue;
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
    if (
      mimeTypes
        .filter((mime) => mime.startsWith("audio/mp4") || mime.startsWith("video/mp4"))
        .some(testMediaType)
    ) {
      progressiveCodecsAudio.push(name);
    }
  }

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
    progressiveCodecsAudio,
    maxResolution: UNPROBED_MAX_RESOLUTION,
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
    const refresh = (hdr10Probe: Promise<boolean>) => {
      const generation = ++probeGeneration;
      const next = probeWebCapabilities();
      setCapabilities({ ...next, settled: false });

      const probes = Promise.all([hdr10Probe, maxDecodeResolutionWithDeadline()]);
      void probes.then(([hdr10, maxResolution]) => {
        if (disposed || generation !== probeGeneration) return;
        setCapabilities((current) => ({
          ...current,
          settled: true,
          maxResolution,
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
    refresh(prewarmCodecDetection());
    const refreshForOutputChange = () => refresh(probeHDR10PlaybackSupport());
    for (const query of queries) {
      if (typeof query.addEventListener === "function")
        query.addEventListener("change", refreshForOutputChange);
      else query.addListener?.(refreshForOutputChange);
    }
    return () => {
      disposed = true;
      for (const query of queries) {
        if (typeof query.removeEventListener === "function")
          query.removeEventListener("change", refreshForOutputChange);
        else query.removeListener?.(refreshForOutputChange);
      }
    };
  }, []);

  return capabilities;
}
