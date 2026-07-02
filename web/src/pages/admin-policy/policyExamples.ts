export const POLICY_EXAMPLE_INPUTS: Record<string, unknown> = {
  scope: {
    schema_version: 1,
    user_id: 42,
    session_id: "web-session",
    profile_id: "profile-main",
    account_library_ids: [1, 2, 3],
    account_restricted: true,
    account_max_playback_quality: "1080p",
    access_policy_revision: 7,
    disabled_library_ids: [3],
    profile_present: true,
    profile_max_content_rating: "PG-13",
    profile_max_playback_quality: "720p",
    profile_library_restricted: true,
    profile_allowed_library_ids: [1, 2],
    profile_has_pin: true,
    profile_verified: true,
    profile_preferred_metadata_language: "en",
    request_time: "2026-07-02T16:00:00Z",
    device_id: "web-admin-preview",
    client_ip: "203.0.113.10",
    is_api_key: false,
  },
};

export function exampleInputForDomain(domain: string) {
  return JSON.stringify(
    POLICY_EXAMPLE_INPUTS[domain] ?? {
      schema_version: 1,
      request_time: "2026-07-02T16:00:00Z",
    },
    null,
    2,
  );
}
