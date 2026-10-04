import type {
  AdminDownloadPreparation,
  AdminDownloadPreparationList,
} from "@/api/v2/adminDownloadPreparations";

/** A running 1080p transcode of a 100-minute movie, 25% through at 2×. */
export function makePreparation(
  overrides: Partial<AdminDownloadPreparation> = {},
): AdminDownloadPreparation {
  return {
    id: "art-1",
    state: "running",
    format: "transcode",
    log_session_id: "download-prepare-art-1",
    media_file_id: "42",
    content_id: "movie:example",
    media_title: "Example Movie",
    media_type: "movie",
    source: {
      container: "mkv",
      video_codec: "hevc",
      resolution: "2160p",
      hdr: true,
      audio_tracks: [
        { codec: "truehd", channels: 8, language: "eng" },
        { codec: "aac", channels: 2 },
      ],
      file_size: 20 * 1024 ** 3,
      duration_seconds: 6000,
      bitrate_kbps: 40_000,
    },
    output: {
      container: "mp4",
      video_codec: "h264",
      audio_codec: "aac",
      resolution: "1080p",
      bitrate_kbps: 10_000,
      tone_map_mode: "software",
      all_audio_tracks: true,
    },
    worker: { kind: "node", node_id: "9", name: "gpu-01" },
    attempts: 1,
    max_attempts: 3,
    created_at: "2026-01-01T12:00:00.000Z",
    started_at: "2026-01-01T12:00:00.000Z",
    progress: {
      encoded_seconds: 1500,
      duration_seconds: 6000,
      speed: 2,
      updated_at: "2026-01-01T12:12:30.000Z",
    },
    progress_unavailable: false,
    requesters: [
      {
        user_id: "7",
        username: "alex",
        profile_id: "p1",
        profile_name: "Alex",
        device_id: "d1",
        device_name: "Alex's iPhone",
        device_platform: "ios",
        status: "preparing",
      },
    ],
    ...overrides,
  };
}

export function makePreparationList(
  items: AdminDownloadPreparation[],
  counts: Partial<AdminDownloadPreparationList["counts"]> = {},
): AdminDownloadPreparationList {
  const count = (state: AdminDownloadPreparation["state"]) =>
    items.filter((item) => item.state === state).length;
  // The v2 brand marks a server response; tests build the same shape.
  return {
    counts: {
      running: count("running"),
      queued: count("queued"),
      retrying: count("retrying"),
      paused: count("paused"),
      failed_recent: count("failed"),
      ...counts,
    },
    items,
  } as AdminDownloadPreparationList;
}
