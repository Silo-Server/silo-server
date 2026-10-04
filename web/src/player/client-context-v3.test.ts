import { afterEach, describe, expect, it, vi } from "vitest";

import {
  buildClientCapabilitiesV3,
  buildClientPlaybackContextV3,
  buildDeliveriesV3,
  detectHLSSupport,
  type WebCapabilityProbe,
} from "./client-context-v3";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("detectHLSSupport", () => {
  it("accepts native HLS when Media Source Extensions are unavailable", () => {
    vi.stubGlobal("MediaSource", undefined);
    vi.stubGlobal("document", {
      createElement: () => ({ canPlayType: () => "maybe" }),
    });

    expect(detectHLSSupport()).toEqual({ supported: true, native: true });
  });

  it("falls back to the hls.js Media Source Extensions probe", () => {
    vi.stubGlobal("document", {
      createElement: () => ({ canPlayType: () => "" }),
    });
    vi.stubGlobal("MediaSource", { isTypeSupported: () => true });

    expect(detectHLSSupport()).toEqual({ supported: true, native: false });
  });
});

describe("buildDeliveriesV3", () => {
  it("advertises the embedded text and PGS artifacts rendered by the web player", () => {
    const deliveries = buildDeliveriesV3({
      containers: ["mp4"],
      codecsVideo: ["h264"],
      progressiveCodecsVideo: ["h264"],
      codecsAudio: ["aac"],
      progressiveCodecsAudio: ["aac"],
      maxResolution: "1080p",
      hdr: false,
      hdrDetails: {
        hdr10: false,
        hdr10_plus: false,
        hlg: false,
        dolby_vision_profiles: [],
        dolby_vision_profile_levels: [],
      },
      hls: true,
      nativeHLS: false,
    });

    for (const delivery of Object.values(deliveries)) {
      expect(delivery.subtitles.embedded_text).toBe(true);
      expect(delivery.subtitles.sidecar_text).toBe(true);
      expect(delivery.subtitles.embedded_bitmap).toBe(true);
      expect(delivery.subtitles.sidecar_bitmap).toBe(false);
    }
  });
});

describe("structured HDR capabilities", () => {
  const safariUA =
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/26.0 Safari/605.1.15";
  const chromeUA =
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/151.0.0.0 Safari/537.36";
  const probe: WebCapabilityProbe = {
    containers: ["mp4"],
    codecsVideo: ["hevc"],
    progressiveCodecsVideo: ["hevc"],
    codecsAudio: ["eac3"],
    progressiveCodecsAudio: ["eac3"],
    maxResolution: "2160p",
    hdr: true,
    hdrDetails: {
      hdr10: true,
      hdr10_plus: false,
      hlg: false,
      hdr10_max_width: 3840,
      hdr10_max_height: 2160,
      hdr10_max_frame_rate: 24,
      hdr10_max_bitrate_kbps: 80_000,
      dolby_vision_profiles: [8],
      dolby_vision_profile_levels: [{ profile: 8, max_level: 6, bl_compatibility_ids: [1] }],
    },
    hls: true,
    nativeHLS: false,
  };

  it("publishes the structured formats in both device and active-output contexts", () => {
    expect(buildClientCapabilitiesV3(probe).hdr_details).toEqual(probe.hdrDetails);
    expect(buildClientPlaybackContextV3(probe).output.hdr_details).toEqual(probe.hdrDetails);
  });

  it("scopes normalized HDR sample entries to native HLS", () => {
    const deliveries = buildDeliveriesV3({ ...probe, nativeHLS: true }, safariUA);

    expect(deliveries.progressive?.hdr_details?.dolby_vision_profiles).toEqual([]);
    expect(deliveries.hls?.hdr_details).toEqual(probe.hdrDetails);
    expect(deliveries.hls?.video_codecs).toContain("hevc");
    expect(deliveries.original_http?.hdr_details?.hdr10).toBe(false);
    expect(deliveries.original_http?.hdr_details?.dolby_vision_profiles).toEqual([]);
  });

  it("keeps Chromium native-HLS evidence scoped to its hls.js engine", () => {
    const chromiumProbe = {
      ...probe,
      nativeHLS: true,
      codecsVideo: ["h264"],
      progressiveCodecsVideo: ["h264", "hevc"],
    };

    const deliveries = buildDeliveriesV3(chromiumProbe, chromeUA);

    expect(deliveries.progressive?.hdr_details).toEqual(probe.hdrDetails);
    expect(deliveries.hls?.hdr_details?.hdr10).toBe(false);
    expect(deliveries.hls?.hdr_details?.dolby_vision_profiles).toEqual([]);
    expect(deliveries.hls?.video_codecs).toEqual(["h264"]);
  });

  it("keeps normalized HDR sample entries on progressive without native HLS", () => {
    const deliveries = buildDeliveriesV3({ ...probe, nativeHLS: false });

    expect(deliveries.progressive?.hdr_details).toEqual(probe.hdrDetails);
    expect(deliveries.hls?.hdr_details?.dolby_vision_profiles).toEqual([]);
    expect(deliveries.original_http?.hdr_details?.hdr10).toBe(false);
    expect(deliveries.original_http?.hdr_details?.dolby_vision_profiles).toEqual([]);
  });

  it("keeps media-element-only HEVC evidence out of original and HLS delivery", () => {
    const progressiveOnlyProbe = {
      ...probe,
      codecsVideo: ["h264"],
      progressiveCodecsVideo: ["h264", "hevc"],
    };

    expect(buildClientCapabilitiesV3(progressiveOnlyProbe).codecs_video).toEqual(["h264", "hevc"]);
    const deliveries = buildDeliveriesV3(progressiveOnlyProbe);
    expect(deliveries.original_http?.video_codecs).toEqual(["h264"]);
    expect(deliveries.progressive?.video_codecs).toEqual(["h264", "hevc"]);
    expect(deliveries.hls?.video_codecs).toEqual(["h264"]);
  });

  it("scopes container-specific audio evidence to progressive MP4", () => {
    const containerScopedProbe = {
      ...probe,
      codecsAudio: ["aac", "vorbis"],
      progressiveCodecsAudio: ["aac"],
    };

    const deliveries = buildDeliveriesV3(containerScopedProbe);

    expect(deliveries.original_http?.audio_decode_codecs).toEqual(["aac", "vorbis"]);
    expect(deliveries.progressive?.audio_decode_codecs).toEqual(["aac"]);
    expect(deliveries.hls?.audio_decode_codecs).toEqual(["aac", "vorbis"]);
  });
});

describe("browser decode engine capabilities", () => {
  const probe: WebCapabilityProbe = {
    containers: ["mp4", "webm"],
    codecsVideo: ["h264", "av1"],
    progressiveCodecsVideo: ["h264", "av1"],
    codecsAudio: ["aac", "opus"],
    progressiveCodecsAudio: ["aac", "opus"],
    maxResolution: "2160p",
    hdr: false,
    hdrDetails: {
      hdr10: false,
      hdr10_plus: false,
      hlg: false,
      dolby_vision_profiles: [],
      dolby_vision_profile_levels: [],
    },
    hls: true,
    nativeHLS: false,
  };
  const engine = {
    containers: ["mkv", "mp4"],
    videoCodecs: ["h264", "hevc", "av1"],
    audioCodecs: ["aac", "opus", "ac3", "eac3", "dts", "truehd"],
    hdr: true,
  };

  it("widens only original_http and claims track selection and managed range", () => {
    const deliveries = buildDeliveriesV3(probe, "test", engine);
    expect(deliveries.original_http).toMatchObject({
      containers: ["mp4", "webm", "mkv"],
      video_codecs: ["h264", "av1", "hevc"],
      audio_decode_codecs: ["aac", "opus", "ac3", "eac3", "dts", "truehd"],
      validated_claims: ["client_selected_audio_track_v1", "client_managed_dynamic_range_v1"],
    });
    expect(deliveries.progressive?.audio_decode_codecs).toEqual(["aac", "opus"]);
    expect(deliveries.progressive?.validated_claims).toEqual([]);
    expect(deliveries.hls?.video_codecs).toEqual(["h264", "av1"]);
  });

  it("claims managed range only when WebGPU can tone-map", () => {
    const deliveries = buildDeliveriesV3(probe, "test", { ...engine, hdr: false });
    expect(deliveries.original_http?.validated_claims).toEqual(["client_selected_audio_track_v1"]);
  });

  it("carries the engine codecs in the device-level lists the planner reads first", () => {
    const capabilities = buildClientCapabilitiesV3(probe, engine);
    expect(capabilities.codecs_audio).toContain("truehd");
    expect(capabilities.codecs_video).toContain("hevc");
    expect(capabilities.containers).toContain("mkv");
    expect(buildClientCapabilitiesV3(probe).codecs_audio).toEqual(["aac", "opus"]);
    expect(
      buildClientPlaybackContextV3(probe, engine).deliveries.original_http?.validated_claims,
    ).toHaveLength(2);
  });
});
