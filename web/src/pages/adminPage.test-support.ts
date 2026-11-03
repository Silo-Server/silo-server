import type { AdminUser } from "@/api/types";

/**
 * Gives jsdom what Radix Select, Tabs and DropdownMenu need to open:
 * ResizeObserver, pointer capture and scrollIntoView. Each test file runs in
 * its own environment, so calling this once at the top of a file is enough.
 */
export function stubRadixDom() {
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  Object.assign(Element.prototype, {
    hasPointerCapture: () => false,
    setPointerCapture: () => {},
    releasePointerCapture: () => {},
    scrollIntoView: () => {},
  });
}

/** A regular, enabled account with no overrides of its own. */
export function makeAdminUser(overrides: Partial<AdminUser> = {}): AdminUser {
  return {
    id: 7,
    username: "taylor",
    email: "taylor@example.test",
    role: "user",
    permissions: [],
    enabled: true,
    library_ids: null,
    access_group_id: null,
    max_playback_quality: null,
    max_streams: null,
    max_transcodes: null,
    max_remote_stream_bitrate_kbps: null,
    max_local_stream_bitrate_kbps: null,
    transcode_allowed: null,
    audio_transcode_allowed: null,
    max_profiles: null,
    download_allowed: null,
    download_transcode_allowed: null,
    requests_allowed: null,
    password_login: true,
    password_change_required: false,
    is_owner: false,
    break_glass: false,
    effective_policy: {
      library_ids: null,
      max_playback_quality: "",
      max_streams: 0,
      max_transcodes: 0,
      max_remote_stream_bitrate_kbps: 0,
      max_local_stream_bitrate_kbps: 0,
      max_profiles: 4,
      transcode_allowed: true,
      audio_transcode_allowed: true,
      download_allowed: true,
      download_transcode_allowed: true,
      requests_allowed: true,
      permissions: [],
    },
    created_at: "2026-07-01T12:00:00Z",
    updated_at: "2026-07-01T12:00:00Z",
    ...overrides,
  };
}
