import { describe, expect, it } from "vitest";
import { makePreparation } from "@/test/downloadPreparations";
import {
  formatAgo,
  formatPreparationAudioOutput,
  formatPreparationKind,
  formatPreparationOutput,
  formatPreparationSource,
  formatRemaining,
  formatRetryIn,
  formatSpeed,
  preparationCompactTitle,
  preparationPercent,
  preparationRemainingSeconds,
  preparationSubtitle,
  preparationTitle,
  preparationWorker,
  requesterDevice,
} from "./adminDownloadPreparationPresentation";

const episode = makePreparation({
  media_title: "Severance",
  media_type: "series",
  series_name: "Severance",
  episode_name: "Hello, Ms. Cobel",
  season_number: 2,
  episode_number: 1,
});

describe("download preparation presentation", () => {
  it("titles movies and episodes", () => {
    expect(preparationTitle(makePreparation())).toBe("Example Movie");
    expect(preparationSubtitle(makePreparation())).toBe("Movie");
    expect(preparationTitle(episode)).toBe("Hello, Ms. Cobel");
    expect(preparationSubtitle(episode)).toBe("S02E01 · Severance");
    expect(preparationCompactTitle(episode)).toBe("Severance S02E01");
  });

  it("summarizes source and output", () => {
    const prep = makePreparation();
    expect(formatPreparationSource(prep)).toBe("MKV · 2160p HEVC HDR · 20.0 GB");
    expect(formatPreparationOutput(prep)).toBe("1080p H.264 · 10 Mbps · HDR → SDR");
    expect(formatPreparationKind(prep)).toBe("Transcode 1080p H.264");
    expect(formatPreparationAudioOutput(prep)).toBe("All 2 tracks → stereo AAC");

    const remux = makePreparation({
      format: "remux",
      output: {
        container: "mp4",
        video_codec: "copy",
        audio_codec: "copy",
        all_audio_tracks: true,
      },
    });
    expect(formatPreparationOutput(remux)).toBe("MP4 · streams copied");
    expect(formatPreparationKind(remux)).toBe("Remux");
    expect(formatPreparationAudioOutput(remux)).toBe("All 2 tracks copied");
  });

  it("derives percent and time left from the latest reading", () => {
    const prep = makePreparation();
    expect(preparationPercent(prep)).toBe(25);
    // 4500 s of media left at 2× realtime.
    expect(preparationRemainingSeconds(prep)).toBe(2250);
    expect(formatRemaining(2250)).toBe("about 38 min left");
    expect(formatRemaining(30)).toBe("under 1 min left");
    expect(formatRemaining(2 * 3600 + 5 * 60)).toBe("about 2 h 5 min left");
    expect(formatSpeed(3.44)).toBe("3.4×");
    expect(formatSpeed(41.2)).toBe("41×");

    const unknown = makePreparation({ progress: undefined });
    expect(preparationPercent(unknown)).toBeNull();
    expect(preparationRemainingSeconds(unknown)).toBeNull();
    expect(formatRemaining(null)).toBe("");
    const stalled = makePreparation({
      progress: { encoded_seconds: 10, duration_seconds: 6000, speed: 0, updated_at: "x" },
    });
    expect(preparationRemainingSeconds(stalled)).toBeNull();
    const over = makePreparation({
      progress: { encoded_seconds: 7000, duration_seconds: 6000, speed: 1, updated_at: "x" },
    });
    expect(preparationPercent(over)).toBe(100);
  });

  it("labels workers and requesters", () => {
    expect(preparationWorker(makePreparation())).toEqual({
      key: "node:9",
      label: "Node",
      name: "gpu-01",
    });
    expect(preparationWorker(makePreparation({ worker: { kind: "server", name: "" } }))).toEqual({
      key: "server:",
      label: "Server",
      name: "Local server",
    });
    expect(preparationWorker(makePreparation({ worker: undefined }))).toBeNull();
    expect(
      requesterDevice({ user_id: "1", username: "a", profile_id: "p", status: "preparing" }),
    ).toBe("Web download");
  });

  it("formats relative times", () => {
    const now = Date.parse("2026-01-01T12:00:00.000Z");
    expect(formatRetryIn("2026-01-01T12:00:45.000Z", now)).toBe("in 45 s");
    expect(formatRetryIn("2026-01-01T12:03:00.000Z", now)).toBe("in 3 min");
    expect(formatRetryIn("2026-01-01T11:59:00.000Z", now)).toBe("now");
    expect(formatAgo("2026-01-01T11:48:00.000Z", now)).toBe("12 min ago");
    expect(formatAgo("2026-01-01T09:00:00.000Z", now)).toBe("3 h ago");
  });
});
