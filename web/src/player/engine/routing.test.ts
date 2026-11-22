import { describe, expect, it } from "vitest";
import { fixturePlanV3 } from "../protocol-v3.fixtures";
import type { PlanV3 } from "../protocol-v3";
import type { EngineCapabilities } from "./capabilities";
import {
  pageOriginProtocol,
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

describe("pageOriginProtocol", () => {
  const origin = "https://app.example.test";
  const same = (nextHopProtocol: string, startTime: number) => ({
    name: `${origin}/assets/app.js`,
    nextHopProtocol,
    startTime,
    redirectStart: 0,
  });
  // A same-origin URL that redirected, as a stored poster does to its signed storage URL: the name
  // is the requested URL and the protocol is the one of the host it ended on.
  const redirected = (nextHopProtocol: string, startTime: number) => ({
    name: `${origin}/api/v2/items/i/poster`,
    nextHopProtocol,
    startTime,
    redirectStart: startTime,
  });
  const cross = (nextHopProtocol: string, startTime: number) => ({
    name: "https://media.example.test/api/v2/stream/s",
    nextHopProtocol,
    startTime,
    redirectStart: 0,
  });

  it.each([
    // On a repeat load the navigation entry's protocol is empty.
    {
      name: "a repeat visit takes a same-origin resource",
      navigation: "",
      resources: [same("http/1.1", 10)],
      want: "http/1.1",
    },
    {
      name: "the newest same-origin resource wins",
      navigation: "",
      resources: [same("h2", 10), same("http/1.1", 20)],
      want: "http/1.1",
    },
    {
      name: "the newest wins regardless of list order",
      navigation: "",
      resources: [same("http/1.1", 20), same("h2", 10)],
      want: "http/1.1",
    },
    {
      name: "an empty protocol is skipped",
      navigation: "",
      resources: [same("http/1.1", 10), same("", 20)],
      want: "http/1.1",
    },
    {
      name: "a cross-origin resource is ignored",
      navigation: "",
      resources: [cross("http/1.1", 10)],
      want: "",
    },
    {
      name: "an unparsable name is skipped",
      navigation: "",
      resources: [
        { name: "not a url", nextHopProtocol: "http/1.1", startTime: 10, redirectStart: 0 },
      ],
      want: "",
    },
    {
      name: "no resource falls back to the navigation",
      navigation: "http/1.1",
      resources: [],
      want: "http/1.1",
    },
    { name: "nothing usable gives empty", navigation: "", resources: [], want: "" },
    {
      name: "the navigation protocol wins over a same-origin resource",
      navigation: "http/1.1",
      resources: [same("h2", 10)],
      want: "http/1.1",
    },
    {
      name: "a redirected same-origin resource is skipped for a direct one",
      navigation: "",
      resources: [same("http/1.1", 10), redirected("h2", 20)],
      want: "http/1.1",
    },
    {
      name: "a redirected same-origin resource alone gives empty",
      navigation: "",
      resources: [redirected("h2", 20)],
      want: "",
    },
  ])("$name", ({ navigation, resources, want }) => {
    expect(pageOriginProtocol(navigation, resources, origin)).toBe(want);
  });
});
