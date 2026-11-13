import { describe, expect, it } from "vitest";

import type { AccessGroupInput } from "@/api/types";

import { sameGroupBody } from "./accessGroupDraft";

const stored: AccessGroupInput = {
  name: "Family",
  description: "",
  library_ids: [3, 1, 2],
  max_playback_quality: "",
  download_allowed: true,
  download_transcode_allowed: true,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 5,
  max_transcodes: 3,
  max_remote_stream_bitrate_kbps: 0,
  max_local_stream_bitrate_kbps: 0,
  max_profiles: 5,
  allowed_permissions: ["marker_edit", "metadata_curation"],
  requests_allowed: true,
  is_default: false,
};

describe("sameGroupBody", () => {
  // A reordered list alone would show the phone Save bar and send a group
  // write that bumps the revision, giving another admin a stale-edit conflict.
  it("ignores the order of libraries and permissions", () => {
    const reordered = {
      ...stored,
      library_ids: [1, 2, 3],
      allowed_permissions: ["metadata_curation", "marker_edit"],
    };
    expect(sameGroupBody(reordered, stored)).toBe(true);
  });

  it("still sees a library or permission added or removed", () => {
    expect(sameGroupBody({ ...stored, library_ids: [1, 2] }, stored)).toBe(false);
    expect(sameGroupBody({ ...stored, library_ids: null }, stored)).toBe(false);
    expect(sameGroupBody({ ...stored, allowed_permissions: ["marker_edit"] }, stored)).toBe(false);
  });
});
