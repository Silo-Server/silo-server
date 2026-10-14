import { describe, expect, it } from "vitest";
import { fixturePlanV3 } from "../protocol-v3.fixtures";
import type { PlanV3 } from "../protocol-v3";
import type { EngineCapabilities } from "./capabilities";
import {
  containerDefaultAudioOrdinal,
  ENGINE_UNSUPPORTED_SOURCE,
  engineTransferForPlan,
  resolveOriginalPlaybackRoute,
  type NativeOriginalSupport,
} from "./routing";

const native: NativeOriginalSupport = {
  containers: ["mp4", "mkv"],
  videoCodecs: ["h264", "av1", "vp9"],
  audioCodecs: ["aac", "opus", "flac"],
};

const engine: EngineCapabilities = {
  containers: ["mkv", "mp4"],
  videoCodecs: ["h264", "hevc", "av1", "vp9"],
  audioCodecs: ["aac", "opus", "flac", "ac3", "eac3", "dts", "truehd", "mlp"],
  hdr: true,
};

function originalPlan(overrides: {
  source?: Partial<PlanV3["source"]>;
  recipe?: Partial<PlanV3["effective_recipe"]>;
  audioIndex?: number;
}): PlanV3 {
  const base = fixturePlanV3();
  return {
    ...base,
    delivery: "original_http",
    stream: { ...base.stream, protocol: "http_progressive", container: "mkv", headers: {} },
    timeline: { ...base.timeline, player_start_seconds: 42 },
    selected_tracks:
      overrides.audioIndex === undefined
        ? base.selected_tracks
        : { audio: { id: `file:1:audio:${overrides.audioIndex}`, index: overrides.audioIndex } },
    effective_recipe: { video_codec: "h264", audio_codec: "aac", ...overrides.recipe },
    source: {
      ...base.source,
      container: "mkv",
      video_codec: "h264",
      audio_codec: "aac",
      dynamic_range: "sdr",
      width: 1920,
      height: 1080,
      duration_seconds: 3600,
      ...overrides.source,
    },
  };
}

const audioTracks = [{ codec: "aac" }, { codec: "truehd", default: true }];

describe("resolveOriginalPlaybackRoute", () => {
  it("leaves files the media element plays to the media element", () => {
    const route = resolveOriginalPlaybackRoute({
      plan: originalPlan({ audioIndex: 1, recipe: { audio_codec: "aac" } }),
      streamUrl: "/stream",
      native,
      engine,
      audioTracks: [{ codec: "aac" }, { codec: "aac", default: true }],
    });
    expect(route).toEqual({ kind: "native" });
  });

  it("sends audio the element cannot decode to the engine", () => {
    const route = resolveOriginalPlaybackRoute({
      plan: originalPlan({ audioIndex: 1, recipe: { audio_codec: "truehd", audio_channels: 8 } }),
      streamUrl: "/stream?st=1",
      native,
      engine,
      audioTracks,
    });
    expect(route).toEqual({
      kind: "engine",
      source: {
        url: "/stream?st=1",
        headers: {},
        container: "mkv",
        durationSeconds: 3600,
        startSeconds: 42,
        video: {
          codec: "h264",
          width: 1920,
          height: 1080,
          transfer: "sdr",
          stripDolbyVision: false,
        },
        audio: { ordinal: 1, codec: "truehd", channels: 8 },
      },
    });
  });

  it("uses the engine for a non-default audio track the element cannot choose", () => {
    const route = resolveOriginalPlaybackRoute({
      plan: originalPlan({ audioIndex: 0 }),
      streamUrl: "/stream",
      native,
      engine,
      audioTracks,
    });
    expect(route.kind).toBe("engine");
  });

  it("presents HDR and Dolby Vision base layers through the engine", () => {
    const hdr = resolveOriginalPlaybackRoute({
      plan: originalPlan({
        source: { dynamic_range: "hdr10", video_codec: "hevc" },
        recipe: { video_codec: "hevc" },
      }),
      streamUrl: "/stream",
      native,
      engine,
      audioTracks: [],
    });
    expect(hdr.kind === "engine" && hdr.source.video.transfer).toBe("pq");

    const dv = resolveOriginalPlaybackRoute({
      plan: originalPlan({
        source: { dynamic_range: "dolby_vision", dv_bl_compat_id: 4, video_codec: "hevc" },
        recipe: { video_codec: "hevc" },
      }),
      streamUrl: "/stream",
      native,
      engine,
      audioTracks: [],
    });
    expect(dv.kind === "engine" && dv.source.video).toMatchObject({
      transfer: "hlg",
      stripDolbyVision: true,
    });
  });

  it("refuses with a typed failure what neither can play", () => {
    const profile5 = resolveOriginalPlaybackRoute({
      plan: originalPlan({
        source: { dynamic_range: "dolby_vision", dv_bl_compat_id: 0, video_codec: "hevc" },
        recipe: { video_codec: "hevc" },
      }),
      streamUrl: "/stream",
      native,
      engine,
      audioTracks: [],
    });
    expect(profile5).toMatchObject({
      kind: "unsupported",
      failure: { classification: ENGINE_UNSUPPORTED_SOURCE },
    });

    const noWebGPU = resolveOriginalPlaybackRoute({
      plan: originalPlan({ source: { dynamic_range: "hdr10" } }),
      streamUrl: "/stream",
      native,
      engine: { ...engine, hdr: false },
      audioTracks: [],
    });
    expect(noWebGPU.kind).toBe("unsupported");

    const avi = resolveOriginalPlaybackRoute({
      plan: originalPlan({ source: { container: "avi" }, recipe: { audio_codec: "dts" } }),
      streamUrl: "/stream",
      native,
      engine,
      audioTracks: [],
    });
    expect(avi.kind).toBe("unsupported");
  });

  it("reports a typed failure instead of guessing when the engine is off", () => {
    const route = resolveOriginalPlaybackRoute({
      plan: originalPlan({ recipe: { audio_codec: "dts" } }),
      streamUrl: "/stream",
      native,
      engine: null,
      audioTracks: [],
    });
    expect(route.kind).toBe("unsupported");
  });
});

describe("engineTransferForPlan", () => {
  it("maps every server range label", () => {
    const transfer = (dynamic_range: string, dv_bl_compat_id?: number) =>
      engineTransferForPlan(originalPlan({ source: { dynamic_range, dv_bl_compat_id } }));
    expect(transfer("sdr")).toBe("sdr");
    expect(transfer("hdr10")).toBe("pq");
    expect(transfer("hdr10_plus")).toBe("pq");
    expect(transfer("hdr_unknown")).toBe("pq");
    expect(transfer("hlg")).toBe("hlg");
    expect(transfer("dolby_vision", 1)).toBe("pq");
    expect(transfer("dolby_vision", 6)).toBe("pq");
    expect(transfer("dolby_vision", 2)).toBe("sdr");
    expect(transfer("dolby_vision", 0)).toBeNull();
    expect(transfer("something_new")).toBeNull();
  });

  it("prefers the plan's effective range over the source's", () => {
    const plan = originalPlan({
      source: { dynamic_range: "dolby_vision", dv_bl_compat_id: 1 },
      recipe: { dynamic_range: "hdr10" },
    });
    expect(engineTransferForPlan(plan)).toBe("pq");
  });
});

describe("containerDefaultAudioOrdinal", () => {
  it("matches the server: the first default-flagged track, else the first", () => {
    expect(containerDefaultAudioOrdinal([])).toBe(0);
    expect(containerDefaultAudioOrdinal([{}, { default: true }, { default: true }])).toBe(1);
    expect(containerDefaultAudioOrdinal([{}, {}])).toBe(0);
  });
});
