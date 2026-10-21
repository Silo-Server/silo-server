import { describe, expect, it } from "vitest";
import type { AdminSession } from "@/api/types";
import {
  activityMethodMeta,
  classifyActivityMethod,
  compareActivityMethods,
  decisionBadgeClass,
  formatAudioDetail,
  formatAudioSummary,
  formatContainerDetail,
  formatDeliveredAudioSummary,
  formatDeliveredContainerSummary,
  formatDeliveredVideoSummary,
  formatDecisionLabel,
  formatPlaybackDecisionSummary,
  formatSourceContainerSummary,
  formatToneMapSummary,
  formatTranscodeModeSummary,
  formatVideoDetail,
  formatVideoSummary,
  getSessionClientLabelFull,
  getSessionRouteNodes,
  normalizeContainerDecision,
  normalizeStreamDecision,
} from "./adminActivityPresentation";

function makeSession(overrides: Partial<AdminSession> = {}): AdminSession {
  return {
    session_id: "session-1",
    user_id: 1,
    username: "admin",
    profile_id: "default",
    media_file_id: 2,
    requested_media_file_id: 2,
    media_title: "Example",
    media_type: "movie",
    play_method: "transcode",
    reporting_node: "local",
    file_duration: 3600,
    started_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    position_seconds: 300,
    is_paused: false,
    audio_track_index: 0,
    transcode_audio: true,
    stream_bitrate_kbps: 8000,
    target_bitrate_kbps: 8000,
    source_container: "mkv",
    source_bitrate_kbps: 9000,
    source_video_codec: "h264",
    source_video_resolution: "1080p",
    target_video_codec: "h264",
    target_resolution: "1080p",
    source_audio_codec: "aac",
    source_audio_channels: 2,
    requested_video_codec: "hevc",
    requested_video_resolution: "2160p",
    ...overrides,
  };
}

describe("adminActivityPresentation", () => {
  it.each([
    ["fmp4", "hls", "fMP4 (HLS)"],
    ["mpegts", "hls", "MPEG-TS (HLS)"],
    ["fmp4", "http", "fMP4"],
  ])("uses the reported %s container over %s", (container, protocol, label) => {
    const session = makeSession({
      play_method: "remux",
      output_container: container,
      output_protocol: protocol,
    });
    expect(formatDeliveredContainerSummary(session)).toBe(label);
    expect(formatContainerDetail(session)).toBe(`MKV → ${label}`);
    expect(classifyActivityMethod(session)).toBe("direct_stream");
  });

  it("keeps bit-for-bit stream copy explicit without changing remux aggregation", () => {
    expect(formatDecisionLabel("copy")).toBe("Copy");
    expect(decisionBadgeClass("copy")).toBe(activityMethodMeta("remux").badgeClass);
    expect(normalizeStreamDecision("copy")).toBe("copy");
    expect(normalizeStreamDecision("remux")).toBe("copy");
    const copied = makeSession({
      play_method: "remux",
      video_decision: "copy",
      audio_decision: "remux",
      transcode_audio: false,
    });
    expect(classifyActivityMethod(copied)).toBe("remux");
    expect(formatPlaybackDecisionSummary(copied)).toBe("copy");
    expect(formatAudioDetail(copied)).toBe("Copied without re-encoding");
    expect(formatDecisionLabel("remux")).toBe("Remux");
    expect(decisionBadgeClass("remux")).toBe(activityMethodMeta("remux").badgeClass);
  });

  it("explains auto-switched requested sources in the detail line", () => {
    expect(
      formatVideoDetail(
        makeSession({
          media_file_id: 2,
          requested_media_file_id: 1,
        }),
      ),
    ).toBe("Auto-switched from HEVC · 2160p · Output → H.264 · 1080p");
  });

  it("summarizes the delivered transcode output for compact rows", () => {
    const session = makeSession({
      source_video_codec: "hevc",
      source_video_resolution: "2160p",
      target_video_codec: "h264",
      target_resolution: "1080p",
      source_audio_codec: "eac3",
      source_audio_channels: 6,
      target_audio_codec: "aac",
      target_audio_channels: 6,
    });

    expect(formatPlaybackDecisionSummary(session)).toBe("transcode");
    expect(normalizeContainerDecision(session.play_method)).toBe("hls");
    expect(normalizeStreamDecision(session.video_decision || session.play_method)).toBe(
      "transcode",
    );
    expect(
      normalizeStreamDecision(
        session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method),
      ),
    ).toBe("transcode");
    expect(formatSourceContainerSummary(session)).toBe("MKV");
    expect(formatDeliveredContainerSummary(session)).toBe("Unknown output container");
    expect(formatContainerDetail(session)).toBe(
      "Repackaged for streaming; output container not reported",
    );
    expect(formatDeliveredVideoSummary(session)).toBe("H.264 · 1080p");
    expect(formatDeliveredAudioSummary(session)).toBe("AAC 5.1");
    expect(formatTranscodeModeSummary(session)).toBe("HW/SW unknown");
  });

  it("labels a full-resolution transcode like its source and keeps a downscale's label", () => {
    const scope4K = {
      play_method: "transcode",
      video_decision: "transcode",
      source_video_codec: "hevc",
      source_video_resolution: "2160p",
      source_video_height: 1600,
      target_video_codec: "hevc",
    } as const;
    const fullSize = makeSession({ ...scope4K, target_resolution: "1600p" });
    expect(formatVideoSummary(fullSize)).toBe("HEVC · 2160p");
    expect(formatDeliveredVideoSummary(fullSize)).toBe("HEVC · 2160p");
    expect(formatVideoDetail(fullSize)).toBe("Output → HEVC · 2160p");

    expect(
      formatDeliveredVideoSummary(makeSession({ ...scope4K, target_resolution: "720p" })),
    ).toBe("HEVC · 720p");
    expect(
      formatDeliveredVideoSummary(
        makeSession({ ...scope4K, source_video_height: undefined, target_resolution: "1600p" }),
      ),
    ).toBe("HEVC · 1600p");
  });

  it("does not infer an output container from HLS or an unknown route", () => {
    for (const play_method of ["transcode", "remux", "", "future-route"]) {
      const session = makeSession({ play_method, output_container: undefined });
      expect(formatDeliveredContainerSummary(session)).toBe("Unknown output container");
      expect(formatDeliveredContainerSummary({ ...session, output_protocol: "hls" })).toBe(
        "Unknown output container (HLS)",
      );
      if (play_method === "remux") {
        expect(formatContainerDetail(session)).toBe(
          "Repackaged for streaming; output container not reported",
        );
      }
    }
  });

  it("keeps direct playback summaries on the effective source", () => {
    const session = makeSession({
      play_method: "direct",
      video_decision: "direct",
      audio_decision: "direct",
      transcode_audio: false,
      source_video_codec: "hevc",
      source_video_resolution: "2160p",
      target_video_codec: "h264",
      target_resolution: "1080p",
      source_audio_codec: "eac3",
      source_audio_channels: 6,
      target_audio_codec: "aac",
      output_container: "fmp4",
      output_protocol: "hls",
    });

    expect(formatPlaybackDecisionSummary(session)).toBe("direct");
    expect(normalizeContainerDecision(session.play_method)).toBe("direct");
    expect(normalizeStreamDecision(session.video_decision)).toBe("direct");
    expect(normalizeStreamDecision(session.audio_decision)).toBe("direct");
    expect(formatDeliveredContainerSummary(session)).toBe("MKV");
    expect(formatContainerDetail(session)).toBe("Original container");
    expect(formatDeliveredVideoSummary(session)).toBe("HEVC · 2160p");
    expect(formatDeliveredAudioSummary(session)).toBe("EAC3 5.1");
    expect(formatTranscodeModeSummary(session)).toBeNull();
  });

  it("labels hardware and software transcode modes", () => {
    expect(formatTranscodeModeSummary(makeSession({ transcode_hw_accel: "qsv" }))).toBe("HW QSV");
    expect(formatTranscodeModeSummary(makeSession({ transcode_hw_accel: "vaapi" }))).toBe(
      "HW VAAPI",
    );
    expect(formatTranscodeModeSummary(makeSession({ transcode_hw_accel: "nvenc" }))).toBe(
      "HW NVENC",
    );
    expect(formatTranscodeModeSummary(makeSession({ transcode_hw_accel: "videotoolbox" }))).toBe(
      "HW VideoToolbox",
    );
    expect(formatTranscodeModeSummary(makeSession({ transcode_hw_accel: "none" }))).toBe("SW");
    expect(
      formatTranscodeModeSummary(
        makeSession({
          play_method: "remux",
          video_decision: "remux",
          audio_decision: "transcode",
          transcode_audio: true,
          transcode_hw_accel: "qsv",
        }),
      ),
      // Audio-only re-encodes have no video encoder, so they get named for the
      // work rather than for an acceleration mode they never used.
    ).toBe("Audio Transcode");
  });

  it("labels a transcode's audio with the target channel count, never the source's", () => {
    // TrueHD 7.1 downmixed to AAC 5.1 must not claim 7.1 output.
    const downmixed = makeSession({
      audio_decision: "transcode",
      source_audio_codec: "truehd",
      source_audio_channels: 8,
      target_audio_codec: "aac",
      target_audio_channels: 6,
    });
    expect(formatDeliveredAudioSummary(downmixed)).toBe("AAC 5.1");
    expect(formatAudioDetail(downmixed)).toBe("→ AAC 5.1");

    // Server did not report a target count → codec alone, not the source's.
    const unknownTarget = makeSession({
      audio_decision: "transcode",
      source_audio_codec: "truehd",
      source_audio_channels: 8,
      target_audio_codec: "aac",
    });
    expect(formatDeliveredAudioSummary(unknownTarget)).toBe("AAC");
    expect(formatAudioDetail(unknownTarget)).toBe("→ AAC");

    // The source summary still describes the source in full.
    expect(formatAudioSummary(unknownTarget)).toBe("TrueHD 7.1");
  });

  it("reports only confirmed tone-map executors", () => {
    expect(formatToneMapSummary(makeSession({ tone_map_mode: "hardware" }))).toEqual({
      badge: "HW Tone map",
      detail: "Hardware",
      mode: "hardware",
    });
    expect(formatToneMapSummary(makeSession({ tone_map_mode: "software" }))).toEqual({
      badge: "SW Tone map",
      detail: "Software",
      mode: "software",
    });
    expect(formatToneMapSummary(makeSession())).toBeNull();
    expect(formatToneMapSummary(makeSession({ tone_map_mode: "future-mode" }))).toBeNull();
    expect(
      formatToneMapSummary(
        makeSession({
          play_method: "remux",
          video_decision: "remux",
          audio_decision: "transcode",
          tone_map_mode: "hardware",
        }),
      ),
    ).toBeNull();
  });

  it("buckets activity sessions by the backend's per-stream decisions", () => {
    const cases: [Partial<AdminSession>, string][] = [
      // Only the audio stream re-encoded (video copied) → "direct_stream".
      [
        {
          play_method: "remux",
          video_decision: "remux",
          audio_decision: "transcode",
          transcode_audio: true,
        },
        "direct_stream",
      ],
      // Same audio-only shape reported via the HLS path (play_method "transcode",
      // video copied) still counts as an audio transcode, not video.
      [
        {
          play_method: "transcode",
          video_decision: "remux",
          audio_decision: "transcode",
          transcode_audio: true,
        },
        "direct_stream",
      ],
      // Full video transcode (with or without audio) stays in the "transcode" bucket.
      [
        {
          play_method: "transcode",
          video_decision: "transcode",
          audio_decision: "transcode",
          transcode_audio: true,
        },
        "transcode",
      ],
      // Video-copy HLS repackage (play_method "transcode" but nothing re-encoded)
      // must count as remux, NOT a video transcode.
      [
        {
          play_method: "transcode",
          video_decision: "remux",
          audio_decision: "remux",
          transcode_audio: false,
        },
        "remux",
      ],
      // Direct play and plain remux keep their expected buckets.
      [
        {
          play_method: "direct",
          video_decision: "direct",
          audio_decision: "direct",
          transcode_audio: false,
        },
        "direct",
      ],
      [
        {
          play_method: "remux",
          video_decision: "remux",
          audio_decision: "remux",
          transcode_audio: false,
        },
        "remux",
      ],
    ];
    for (const [overrides, bucket] of cases) {
      expect(classifyActivityMethod(makeSession(overrides)), JSON.stringify(overrides)).toBe(
        bucket,
      );
    }
  });

  it("prefers the server-computed effective_play_method when present", () => {
    // The server already reduced the decisions; the local fallback must not
    // second-guess it even when the raw fields disagree.
    expect(
      classifyActivityMethod(
        makeSession({
          effective_play_method: "remux",
          play_method: "transcode",
          video_decision: "transcode",
          audio_decision: "transcode",
        }),
      ),
    ).toBe("remux");
  });

  it("buckets rows with unknown stream state as unknown, not audio", () => {
    // A legacy/stale row (unrecognized play_method, no decisions) with the
    // transcode_audio flag set is not a known audio transcode.
    expect(
      classifyActivityMethod(
        makeSession({
          play_method: "",
          video_decision: undefined,
          audio_decision: undefined,
          transcode_audio: true,
        }),
      ),
    ).toBe("unknown");
    expect(
      classifyActivityMethod(
        makeSession({
          play_method: "hls",
          video_decision: undefined,
          audio_decision: undefined,
          transcode_audio: true,
        }),
      ),
    ).toBe("unknown");
  });

  it("orders Jellyfin-style activity buckets from lowest to highest server work", () => {
    const sorted = ["direct_stream", "unknown", "transcode", "direct", "remux"].sort(
      compareActivityMethods,
    );
    expect(sorted).toEqual(["direct", "remux", "direct_stream", "transcode", "unknown"]);
  });

  it.each<[string, Partial<AdminSession>, ReturnType<typeof getSessionRouteNodes>]>([
    [
      "shows both transcode and proxy nodes in playback route order",
      {
        reporting_node: "transcode-legacy",
        routing_workload: "video_transcode",
        routing_execution: "transcode",
        routing_execution_node_id: 21,
        routing_execution_node_name: "silo-transcode-01",
        routing_egress: "proxy",
        routing_egress_node_id: 34,
        routing_egress_node_name: "silo-proxy-02",
      },
      [
        {
          key: "transcode:21",
          kind: "transcode",
          label: "Transcode",
          name: "silo-transcode-01",
        },
        { key: "proxy:34", kind: "proxy", label: "Proxy", name: "silo-proxy-02" },
      ],
    ],
    [
      "shows a proxy once when it both executes and serves a remux",
      {
        routing_workload: "remux",
        routing_execution: "proxy",
        routing_execution_node_id: 34,
        routing_execution_node_name: "silo-proxy-02",
        routing_egress: "proxy",
        routing_egress_node_id: 34,
        routing_egress_node_name: "silo-proxy-02",
      },
      [{ key: "proxy:34", kind: "proxy", label: "Proxy", name: "silo-proxy-02" }],
    ],
    [
      "preserves distinct proxy execution and egress nodes",
      {
        routing_workload: "remux",
        routing_execution: "proxy",
        routing_execution_node_id: 34,
        routing_execution_node_name: "silo-proxy-executor",
        routing_egress: "proxy",
        routing_egress_node_id: 35,
        routing_egress_node_name: "silo-proxy-egress",
      },
      [
        { key: "proxy:34", kind: "proxy", label: "Proxy", name: "silo-proxy-executor" },
        { key: "proxy:35", kind: "proxy", label: "Proxy", name: "silo-proxy-egress" },
      ],
    ],
    [
      "uses the resolved display name for a reconstructed remote transcode",
      {
        node_display_name: "silo-transcode-recovered",
        routing_workload: "video_transcode",
        routing_execution: "transcode",
        routing_egress: "api",
      },
      [
        {
          key: "transcode:silo-transcode-recovered",
          kind: "transcode",
          label: "Transcode",
          name: "silo-transcode-recovered",
        },
        { key: "server:local", kind: "server", label: "Server", name: "Local server" },
      ],
    ],
    [
      "falls back to routing node IDs when a registered node name is unavailable",
      {
        routing_workload: "video_transcode",
        routing_execution: "transcode",
        routing_execution_node_id: 21,
        routing_egress: "proxy",
        routing_egress_node_id: 34,
      },
      [
        { key: "transcode:21", kind: "transcode", label: "Transcode", name: "Node #21" },
        { key: "proxy:34", kind: "proxy", label: "Proxy", name: "Node #34" },
      ],
    ],
  ])("%s", (_name, overrides, nodes) => {
    expect(getSessionRouteNodes(makeSession(overrides))).toEqual(nodes);
  });

  it("keeps local and legacy activity rows readable", () => {
    expect(
      getSessionRouteNodes(
        makeSession({
          reporting_node: "local",
          routing_workload: "direct_play",
          routing_execution: "none",
          routing_egress: "api",
        }),
      ),
    ).toEqual([{ key: "server:local", kind: "server", label: "Server", name: "Local server" }]);

    expect(
      getSessionRouteNodes(
        makeSession({ reporting_node: "worker-legacy", node_display_name: "Basement worker" }),
      ),
    ).toEqual([
      {
        key: "legacy:Basement worker",
        kind: "legacy",
        label: "Node",
        name: "Basement worker",
      },
    ]);
  });

  it("falls back to the compact label, then to name and version", () => {
    expect(
      getSessionClientLabelFull(
        makeSession({ client_label: "Chrome 120", client_name: "Chrome", client_version: "120.0" }),
      ),
    ).toBe("Chrome 120");
    expect(
      getSessionClientLabelFull(makeSession({ client_name: "Silo iOS", client_version: "2.1.0" })),
    ).toBe("Silo iOS 2.1.0");
    expect(getSessionClientLabelFull(makeSession({ client_name: "Silo iOS" }))).toBe("Silo iOS");
    expect(getSessionClientLabelFull(makeSession())).toBe("");
  });
});
