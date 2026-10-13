import type { AdminPolicyDefaults } from "@/api/types";

// What GET /api/v2/admin/users/policy-defaults answers: full access for an
// admin, and for a regular account with no group the same except
// server-prepared downloads.
export const POLICY_DEFAULTS: AdminPolicyDefaults = {
  admin: {
    library_ids: null,
    max_playback_quality: "",
    max_streams: 0,
    max_transcodes: 0,
    max_remote_stream_bitrate_kbps: 0,
    max_local_stream_bitrate_kbps: 0,
    transcode_allowed: true,
    audio_transcode_allowed: true,
    download_allowed: true,
    download_transcode_allowed: true,
    requests_allowed: true,
  },
  ungrouped: {
    library_ids: null,
    max_playback_quality: "",
    max_streams: 0,
    max_transcodes: 0,
    max_remote_stream_bitrate_kbps: 0,
    max_local_stream_bitrate_kbps: 0,
    transcode_allowed: true,
    audio_transcode_allowed: true,
    download_allowed: true,
    download_transcode_allowed: false,
    requests_allowed: true,
  },
};
