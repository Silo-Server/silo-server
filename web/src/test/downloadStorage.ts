import type {
  AdminDownloadDevice,
  AdminDownloadStorage,
  AdminDownloadStorageFile,
  AdminDownloadStorageLocation,
} from "@/api/v2/adminDownloadStorage";

export function makeStorageLocation(
  overrides: Partial<AdminDownloadStorageLocation> = {},
): AdminDownloadStorageLocation {
  return {
    key: "server",
    kind: "server",
    name: "Server",
    enabled: true,
    online: true,
    dir: "/srv/silo/download-artifacts",
    dir_source: "setting",
    usage: {
      measured_at: new Date().toISOString(),
      files: 312,
      bytes: 214e9,
      partial_files: 0,
      partial_bytes: 0,
      other_bytes: 0,
      fs_used_bytes: 1220e9,
      fs_total_bytes: 2000e9,
      fs_type: "ext4",
      shares_scratch: false,
      ephemeral: false,
      stale: false,
    },
    in_use_files: 230,
    in_use_bytes: 162e9,
    cached_files: 82,
    cached_bytes: 52e9,
    waiting_downloads: 41,
    stale_waiting_bytes: 0,
    untracked_files: 0,
    untracked_bytes: 0,
    budget_bytes: 750e9,
    budget_source: "setting",
    cleanup_backlog: 0,
    storage_full: false,
    replicas_disagree: false,
    ...overrides,
  };
}

export function makeStorage(overrides: Partial<AdminDownloadStorage> = {}): AdminDownloadStorage {
  // The v2 brand marks a server response; tests build the same shape.
  return {
    locations: [makeStorageLocation()],
    cache_hours: 72,
    disk_ceiling_percent: 85,
    default_budget_bytes: 750e9,
    stale_device_days: 14,
    preparing_jobs: 3,
    freed_last_30_days_bytes: 2.7e12,
    ...overrides,
  } as AdminDownloadStorage;
}

export function makeStorageFile(
  overrides: Partial<AdminDownloadStorageFile> = {},
): AdminDownloadStorageFile {
  return {
    id: "art-1",
    state: "cached",
    format: "transcode",
    location: "node:9",
    location_name: "node-gpu-1",
    media_file_id: "42",
    title: "Dune: Part Two",
    media_type: "movie",
    container: "mp4",
    video_codec: "hevc",
    audio_codec: "aac",
    resolution: "1080p",
    bitrate_kbps: 10000,
    bytes: 14.2e9,
    created_at: "2026-10-05T09:30:00.000Z",
    last_used_at: "2026-10-06T07:30:00.000Z",
    waiting: 0,
    downloading: 0,
    finished: 3,
    stale_waiting: 0,
    ...overrides,
  };
}

export function makeDownloadDevice(
  overrides: Partial<AdminDownloadDevice> = {},
): AdminDownloadDevice {
  return {
    user_id: "7",
    username: "maya",
    profile_id: "p-maya",
    profile_name: "Maya",
    device_id: "dev-pixel",
    device_name: "Pixel 8",
    platform: "android",
    last_seen_at: "2026-09-19T09:30:00.000Z",
    stale: true,
    copies: 12,
    finished: 7,
    waiting: 5,
    failed: 0,
    revoked: 0,
    bytes_on_device: 34.1e9,
    monitors: 1,
    ...overrides,
  };
}
