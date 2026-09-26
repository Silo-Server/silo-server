import { expect, it } from "vitest";

import type { AccessGroup } from "@/api/types";
import { groupPolicyChanges } from "./accessGroupPolicyChanges";

const base: AccessGroup = {
  id: 1,
  name: "Kids",
  description: "",
  library_ids: [2, 3],
  max_playback_quality: "1080p",
  download_allowed: false,
  download_transcode_allowed: false,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 1,
  max_transcodes: 0,
  max_remote_stream_bitrate_kbps: 8000,
  max_local_stream_bitrate_kbps: 0,
  allowed_permissions: [],
  requests_allowed: false,
  is_default: false,
  member_count: 0,
  created_at: "",
  updated_at: "",
};

it("reports only the policies that differ, in member-facing terms", () => {
  const target: AccessGroup = {
    ...base,
    id: 2,
    name: "Adults",
    library_ids: null,
    download_allowed: true,
    max_streams: 0,
    max_remote_stream_bitrate_kbps: 0,
    max_local_stream_bitrate_kbps: 4500,
    allowed_permissions: null,
  };
  expect(
    groupPolicyChanges(
      base,
      target,
      new Map([
        [2, "Movies"],
        [3, "Series"],
      ]),
    ),
  ).toEqual([
    { label: "Libraries", from: "Movies, Series", to: "All libraries" },
    { label: "Downloads", from: "Not allowed", to: "Allowed" },
    { label: "Concurrent streams", from: "1", to: "Unlimited" },
    { label: "Remote stream bitrate", from: "8 Mbps", to: "Unlimited" },
    { label: "Local stream bitrate", from: "Unlimited", to: "4.5 Mbps" },
    { label: "Permissions", from: "None", to: "All" },
  ]);
});

it("treats the same libraries in another order as unchanged", () => {
  expect(groupPolicyChanges(base, { ...base, library_ids: [3, 2] })).toEqual([]);
});

it("identifies the libraries when equal-sized groups grant different access", () => {
  const changes = groupPolicyChanges(
    base,
    { ...base, library_ids: [3, 4] },
    new Map([
      [2, "Movies"],
      [3, "Series"],
      [4, "Anime"],
    ]),
  );
  expect(changes).toEqual([{ label: "Libraries", from: "Movies, Series", to: "Series, Anime" }]);
});
