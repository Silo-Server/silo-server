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
  // The stripe URL decides where the login token goes: a parallel_range_url
  // with st is authorized by that reference alone and gets no token; one
  // without st gets the token; a proxy node never does.
  it.each([
    {
      name: "a parallel_range_url with st carries no login token",
      parallelRangeUrl: "https://media.example.test/api/v2/stream/s?st=x",
      streamUrl: "/api/v2/stream/s?st=x&token=access",
      navigationProtocol: "h2",
      want: "https://media.example.test/api/v2/stream/s?st=x",
    },
    {
      name: "a parallel_range_url with st keeps its other parameters and no token",
      parallelRangeUrl: "https://media.example.test/api/v2/stream/s?st=x&seek=12.5",
      streamUrl: "/api/v2/stream/s?st=x&seek=12.5&token=access",
      navigationProtocol: "h2",
      want: "https://media.example.test/api/v2/stream/s?st=x&seek=12.5",
    },
    {
      name: "a parallel_range_url without st carries the login token",
      parallelRangeUrl: "https://media.example.test/api/v2/stream/s",
      streamUrl: "/api/v2/stream/s?token=access",
      navigationProtocol: "h2",
      want: "https://media.example.test/api/v2/stream/s?token=access",
    },
    {
      name: "a plan without parallel_range_url reads stream.url with its token",
      streamUrl: "/api/v2/stream/s?st=x&token=access",
      navigationProtocol: "http/1.1",
      want: "/api/v2/stream/s?st=x&token=access",
    },
    {
      name: "a proxy node URL is striped as given, without the login token",
      streamUrl: "https://node.example.test/stream/direct/opaque",
      navigationProtocol: "h2",
      want: "https://node.example.test/stream/direct/opaque",
    },
    {
      name: "the page origin is striped when the page itself came over HTTP/1.1",
      streamUrl: "/api/v2/stream/s?st=x&token=access",
      navigationProtocol: "http/1.1",
      want: "/api/v2/stream/s?st=x&token=access",
    },
    {
      name: "the page origin is not striped over HTTP/2",
      streamUrl: "/api/v2/stream/s?st=x&token=access",
      navigationProtocol: "h2",
      want: undefined,
    },
  ])("$name", ({ parallelRangeUrl, streamUrl, navigationProtocol, want }) => {
    const base = originalPlan({ recipe: { audio_codec: "truehd" } });
    const plan = parallelRangeUrl
      ? { ...base, stream: { ...base.stream, parallel_range_url: parallelRangeUrl } }
      : base;
    const route = resolveOriginalPlaybackRoute({
      plan,
      streamUrl,
      accessToken: "access",
      navigationProtocol,
      native,
      engine,
      audioTracks: [],
    });
    if (route.kind !== "engine") throw new Error(`route is ${route.kind}, want engine`);
    expect(route.source.url).toBe(streamUrl);
    expect(route.source.parallelRangeUrl).toBe(want);
    if (streamUrl.startsWith("https://node.")) {
      expect(new URL(route.source.parallelRangeUrl ?? "").searchParams.has("token")).toBe(false);
    }
  });
});
