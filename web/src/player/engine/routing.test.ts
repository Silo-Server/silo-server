import { describe, expect, it } from "vitest";
import { fixturePlanV3 } from "../protocol-v3.fixtures";
import type { PlanV3 } from "../protocol-v3";
import type { EngineCapabilities } from "./capabilities";
import { resolveOriginalPlaybackRoute, type NativeOriginalSupport } from "./routing";

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

describe("resolveOriginalPlaybackRoute", () => {
  const NODE = "https://node.example.test/stream/direct/opaque";
  // The server names the stripe URL, and the engine uses it as given: no token
  // is ever added to it. A plan that names none is not striped.
  it.each([
    {
      name: "a media origin is used as given, with no login token",
      parallelRangeUrl: "https://media.example.test/api/v2/stream/s?st=x&seek=12.5",
      planUrl: "/stream/s?st=x&seek=12.5",
      streamUrl: "/api/v2/stream/s?st=x&seek=12.5&token=access",
      navigationProtocol: "h2",
      want: "https://media.example.test/api/v2/stream/s?st=x&seek=12.5",
    },
    {
      name: "a media origin without st still gets no login token",
      parallelRangeUrl: "https://media.example.test/api/v2/stream/s",
      planUrl: "/stream/s",
      streamUrl: "/api/v2/stream/s?token=access",
      navigationProtocol: "h2",
      want: "https://media.example.test/api/v2/stream/s",
    },
    {
      name: "a media origin whose URL new URL() rejects gives no stripe URL",
      parallelRangeUrl: "https://a<b.example/api/v2/stream/s?st=x",
      planUrl: "/stream/s?st=x",
      streamUrl: "/api/v2/stream/s?st=x&token=access",
      navigationProtocol: "http/1.1",
      want: undefined,
    },
    {
      name: "the plan's own stream is striped when the page came over HTTP/1.1",
      parallelRangeUrl: "/stream/s?st=x",
      planUrl: "/stream/s?st=x",
      streamUrl: "/api/v2/stream/s?st=x&token=access",
      navigationProtocol: "http/1.1",
      want: "/api/v2/stream/s?st=x&token=access",
    },
    {
      name: "the plan's own stream is not striped when the page came over HTTP/2",
      parallelRangeUrl: "/stream/s?st=x",
      planUrl: "/stream/s?st=x",
      streamUrl: "/api/v2/stream/s?st=x&token=access",
      navigationProtocol: "h2",
      want: undefined,
    },
    {
      name: "the plan's own stream is not striped when the browser names no protocol",
      parallelRangeUrl: "/stream/s?st=x",
      planUrl: "/stream/s?st=x",
      streamUrl: "/api/v2/stream/s?st=x&token=access",
      navigationProtocol: "",
      want: undefined,
    },
    {
      name: "a proxy node's URL the server names is striped as issued, whatever the page's protocol",
      parallelRangeUrl: NODE,
      planUrl: NODE,
      streamUrl: NODE,
      navigationProtocol: "h2",
      want: NODE,
    },
    {
      name: "a proxy node's URL the server did not name is not striped",
      planUrl: NODE,
      streamUrl: NODE,
      navigationProtocol: "http/1.1",
      want: undefined,
    },
    {
      name: "a plan without parallel_range_url is not striped",
      planUrl: "/stream/s?st=x",
      streamUrl: "/api/v2/stream/s?st=x&token=access",
      navigationProtocol: "http/1.1",
      want: undefined,
    },
  ])("$name", ({ parallelRangeUrl, planUrl, streamUrl, navigationProtocol, want }) => {
    const base = originalPlan({ recipe: { audio_codec: "truehd" } });
    const plan = {
      ...base,
      stream: {
        ...base.stream,
        url: planUrl,
        ...(parallelRangeUrl ? { parallel_range_url: parallelRangeUrl } : {}),
      },
    };
    const route = resolveOriginalPlaybackRoute({
      plan,
      streamUrl,
      navigationProtocol,
      native,
      engine,
      audioTracks: [],
    });
    if (route.kind !== "engine") throw new Error(`route is ${route.kind}, want engine`);
    expect(route.source.url).toBe(streamUrl);
    expect(route.source.parallelRangeUrl).toBe(want);
  });
});
