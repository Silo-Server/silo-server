import { describe, expect, it } from "vitest";

import type { PlanV3 } from "../protocol-v3";
import { fixturePlanV3 } from "../protocol-v3.fixtures";
import { keepsOriginalStream } from "./planTransport";

const original = (overrides: Partial<PlanV3> = {}) =>
  fixturePlanV3({
    delivery: "original_http",
    stream: {
      url: "/stream/session-1?st=first",
      protocol: "http_progressive",
      container: "mp4",
      mime_type: "video/mp4",
      headers: {},
      header_refresh: "none",
      parallel_range_url: "/stream/session-1?st=first",
    },
    subtitle: { mode: "off", inventory: [] },
    ...overrides,
  });

describe("keepsOriginalStream", () => {
  const playing = original();

  it("keeps the stream when only the subtitle, the token and the start change", () => {
    const next = original({
      plan_id: "plan:next",
      plan_attempt_key: "v3:next",
      stream: {
        ...playing.stream,
        url: "/stream/session-1?st=second",
        parallel_range_url: "/stream/session-1?st=second",
      },
      timeline: { ...playing.timeline, source_start_seconds: 45, player_start_seconds: 45 },
      selected_tracks: {
        ...playing.selected_tracks,
        subtitle: { id: "file:7:subtitle:0", index: 0 },
      },
      subtitle: { mode: "render", inventory: [] },
    });
    expect(keepsOriginalStream(playing, next)).toBe(true);
  });

  it.each<[string, Partial<PlanV3>]>([
    ["a burned-in subtitle", { subtitle: { mode: "burn_in", inventory: [] } }],
    ["a remux", { delivery: "server_remux_progressive" }],
    ["another session", { session_id: "session-2" }],
    ["another file", { effective_media_file_id: 8 }],
    ["another address", { stream: { ...original().stream, url: "/stream/session-2?st=first" } }],
    [
      "a proxy address with the token in its path",
      { stream: { ...original().stream, url: "https://node.example/stream/direct/second" } },
    ],
    ["other headers", { stream: { ...original().stream, headers: { Authorization: "Bearer x" } } }],
    [
      "another header refresh address",
      { stream: { ...original().stream, header_refresh_url: "/playback/session-1/headers" } },
    ],
    ["another audio track", { selected_tracks: { audio: { id: "file:7:audio:1", index: 1 } } }],
    [
      "another recipe",
      { effective_recipe: { video_codec: "h264", audio_codec: "eac3", height: 1080 } },
    ],
    ["another stream origin", { timeline: { ...original().timeline, stream_origin_seconds: 30 } }],
  ])("opens a new stream for %s", (_, overrides) => {
    expect(keepsOriginalStream(playing, original(overrides))).toBe(false);
  });

  it("opens a new stream when the playing plan is not the original file", () => {
    const transcode = fixturePlanV3();
    expect(keepsOriginalStream(transcode, { ...transcode, plan_id: "plan:next" })).toBe(false);
  });
});
