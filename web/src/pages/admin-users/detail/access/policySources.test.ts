import { describe, expect, it } from "vitest";

import type { AccessGroup, AdminUser, Library } from "@/api/types";
import { policyInheritHints, savedUserPolicyInheritHints } from "@/components/UserPolicyFields";

import {
  countCustomPolicyRows,
  countCustomRequestTerms,
  formatBitrateCap,
  formatStreams,
  formatVideoTranscoding,
  inheritContextFor,
  inheritedValueText,
  permissionLock,
  rowSource,
  videoTranscodingFromEffective,
  videoTranscodingFromOverrides,
  videoTranscodingOverrides,
  type VideoTranscoding,
} from "./policySources";

const USER: AdminUser = {
  id: 7,
  username: "jamie",
  email: "jamie@example.test",
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
    max_profiles: 5,
    transcode_allowed: true,
    audio_transcode_allowed: true,
    download_allowed: true,
    download_transcode_allowed: false,
    requests_allowed: true,
    permissions: [],
  },
  created_at: "2026-03-02T12:00:00Z",
  updated_at: "2026-09-28T12:00:00Z",
};

const FAMILY: AccessGroup = {
  id: 3,
  name: "Family",
  description: "",
  library_ids: [1, 2],
  max_playback_quality: "1080p",
  download_allowed: true,
  download_transcode_allowed: false,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 2,
  max_transcodes: 1,
  max_remote_stream_bitrate_kbps: 8000,
  max_local_stream_bitrate_kbps: 0,
  max_profiles: 5,
  allowed_permissions: ["marker_edit"],
  requests_allowed: true,
  is_default: false,
  member_count: 1,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};
const LIBRARIES = [
  { id: 1, name: "Movies" },
  { id: 2, name: "TV" },
] as Library[];

const grouped: AdminUser = { ...USER, access_group_id: 3 };
const hintsFor = (user: AdminUser) =>
  savedUserPolicyInheritHints(user, policyInheritHints(user.access_group_id, [FAMILY]));

describe("sources", () => {
  it("tags each row by where its value comes from", () => {
    const server = inheritContextFor(USER, [FAMILY]);
    expect(server).toEqual({ kind: "server" });
    expect(rowSource(USER, "maxStreams", server)).toBe("default");

    const group = inheritContextFor(grouped, [FAMILY]);
    expect(group).toEqual({ kind: "group", name: "Family" });
    expect(rowSource(grouped, "maxStreams", group)).toBe("group");
    expect(rowSource({ ...grouped, max_streams: 3 }, "maxStreams", group)).toBe("custom");
    // A false override is still an override.
    expect(rowSource({ ...grouped, requests_allowed: false }, "requests", group)).toBe("custom");
  });

  it("treats an admin as ungrouped even with a stored group", () => {
    expect(inheritContextFor({ ...grouped, role: "admin" }, [FAMILY])).toEqual({ kind: "server" });
  });

  it("names a group missing from the list by its id", () => {
    expect(inheritContextFor({ ...USER, access_group_id: 9 }, [FAMILY])).toEqual({
      kind: "group",
      name: "#9",
    });
  });
});

describe("inheritedValueText", () => {
  it("words the server default", () => {
    const ctx = inheritContextFor(USER, []);
    const hints = hintsFor(USER);
    expect(inheritedValueText("maxStreams", hints, ctx, LIBRARIES)).toBe("Default: unlimited");
    expect(inheritedValueText("videoTranscoding", hints, ctx, LIBRARIES)).toBe(
      "Default: allowed, unlimited",
    );
    expect(inheritedValueText("remoteBitrate", hints, ctx, LIBRARIES)).toBe("Default: no cap");
    expect(inheritedValueText("maxProfiles", hints, ctx, LIBRARIES)).toBe("Default: 5");
    expect(inheritedValueText("libraries", hints, ctx, LIBRARIES)).toBe("Default: all libraries");
  });

  it("words the group's value, including under an override", () => {
    const user = {
      ...grouped,
      max_streams: 3,
      download_transcode_allowed: true,
      max_remote_stream_bitrate_kbps: 12000,
      max_playback_quality: "2160p",
    };
    const ctx = inheritContextFor(user, [FAMILY]);
    const hints = hintsFor(user);
    expect(inheritedValueText("maxStreams", hints, ctx, LIBRARIES)).toBe("Group: 2");
    expect(inheritedValueText("serverPrepared", hints, ctx, LIBRARIES)).toBe("Group: not allowed");
    expect(inheritedValueText("remoteBitrate", hints, ctx, LIBRARIES)).toBe("Group: 8 Mbps");
    expect(inheritedValueText("maxQuality", hints, ctx, LIBRARIES)).toBe("Group: 1080p");
  });

  it("keeps library names as they are", () => {
    const user = { ...grouped, library_ids: [1] };
    const ctx = inheritContextFor(user, [FAMILY]);
    expect(inheritedValueText("libraries", hintsFor(user), ctx, LIBRARIES)).toBe(
      "Group: Movies, TV",
    );
  });

  it("is unknown while the group is not loaded", () => {
    const user = { ...USER, access_group_id: 9, max_streams: 3 };
    const ctx = inheritContextFor(user, [FAMILY]);
    const hints = savedUserPolicyInheritHints(user, policyInheritHints(9, [FAMILY]));
    expect(inheritedValueText("maxStreams", hints, ctx, LIBRARIES)).toBeUndefined();
  });
});

describe("countCustomRequestTerms", () => {
  it("counts the request limit and approval the account sets itself", () => {
    const quota = { unlimited: false as const, max: 5, days: 7 };
    expect(countCustomRequestTerms(undefined)).toBe(0);
    expect(
      countCustomRequestTerms({
        quota,
        quotaSource: { kind: "account" },
        autoApprove: true,
        approvalSource: { kind: "group", name: "Family" },
      }),
    ).toBe(1);
    expect(
      countCustomRequestTerms({
        quota,
        quotaSource: { kind: "account" },
        autoApprove: false,
        approvalSource: { kind: "account" },
      }),
    ).toBe(2);
  });
});

describe("countCustomPolicyRows", () => {
  it("counts video transcoding once for its two fields", () => {
    expect(countCustomPolicyRows(USER)).toBe(0);
    expect(countCustomPolicyRows({ ...USER, transcode_allowed: true, max_transcodes: 1 })).toBe(1);
    expect(
      countCustomPolicyRows({
        ...USER,
        max_streams: 2,
        transcode_allowed: true,
        max_transcodes: 1,
        library_ids: [],
        requests_allowed: false,
      }),
    ).toBe(4);
  });

  it("counts a profile limit set on the account", () => {
    expect(countCustomPolicyRows({ ...USER, max_profiles: 3 })).toBe(1);
  });
});

describe("video transcoding", () => {
  it.each<[string, VideoTranscoding | null, boolean | null, number | null]>([
    ["Off", { mode: "off" }, false, null],
    ["Unlimited", { mode: "unlimited" }, true, 0],
    ["Up to 2", { mode: "limit", max: 2 }, true, 2],
    ["Default", null, null, null],
  ])("round-trips %s", (_, value, allowed, max) => {
    expect(videoTranscodingOverrides(value)).toEqual({
      transcode_allowed: allowed,
      max_transcodes: max,
    });
    expect(videoTranscodingFromOverrides(allowed, max)).toEqual(value);
  });

  it("reads an overridden count with an inherited switch as custom", () => {
    // The card shows it from the effective policy and only rewrites the pair
    // when the admin changes this row.
    expect(videoTranscodingFromOverrides(null, 3)).not.toBeNull();
    expect(videoTranscodingFromEffective(true, 3)).toEqual({ mode: "limit", max: 3 });
    expect(videoTranscodingFromEffective(false, 3)).toEqual({ mode: "off" });
  });

  it("formats each mode", () => {
    expect(formatVideoTranscoding({ mode: "off" })).toBe("Off");
    expect(formatVideoTranscoding({ mode: "unlimited" })).toBe("Unlimited");
    expect(formatVideoTranscoding({ mode: "limit", max: 1 })).toBe("Up to 1 at a time");
    expect(formatStreams(0)).toBe("Unlimited");
    expect(formatBitrateCap(0)).toBe("No cap");
    expect(formatBitrateCap(12000)).toBe("12 Mbps");
  });
});

describe("permissionLock", () => {
  it("locks a permission the group does not allow", () => {
    expect(permissionLock(grouped, [FAMILY], "metadata_curation")).toEqual({
      locked: true,
      groupName: "Family",
    });
    expect(permissionLock(grouped, [FAMILY], "marker_edit")).toEqual({
      locked: false,
      groupName: "Family",
    });
  });

  it("never locks an admin, an ungrouped account, an unloaded group, or an open ceiling", () => {
    expect(
      permissionLock({ ...grouped, role: "admin" }, [FAMILY], "metadata_curation").locked,
    ).toBe(false);
    expect(permissionLock(USER, [FAMILY], "metadata_curation").locked).toBe(false);
    expect(permissionLock(grouped, [], "metadata_curation").locked).toBe(false);
    expect(
      permissionLock(grouped, [{ ...FAMILY, allowed_permissions: null }], "metadata_curation")
        .locked,
    ).toBe(false);
  });
});
