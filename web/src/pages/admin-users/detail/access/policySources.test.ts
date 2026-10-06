import { describe, expect, it } from "vitest";

import type { AccessGroup, AdminUser, Library } from "@/api/types";
import { policyInheritHints, savedUserPolicyInheritHints } from "@/components/UserPolicyFields";
import { POLICY_DEFAULTS } from "@/test/policyDefaults";

import {
  countCustomPolicyRows,
  countCustomRequestTerms,
  inheritContextFor,
  inheritedValueText,
  permissionLock,
  rowSource,
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
  max_profiles: 5,
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
  savedUserPolicyInheritHints(
    user,
    policyInheritHints(user.role, user.access_group_id, [FAMILY], POLICY_DEFAULTS),
  );

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
  it("words an admin's default as full access", () => {
    const admin: AdminUser = { ...USER, role: "admin", download_transcode_allowed: false };
    const ctx = inheritContextFor(admin, []);
    expect(inheritedValueText("serverPrepared", hintsFor(admin), ctx, LIBRARIES)).toBe(
      "Default: allowed",
    );
    expect(inheritedValueText("serverPrepared", hintsFor(USER), ctx, LIBRARIES)).toBe(
      "Default: not allowed",
    );
  });

  it("is unknown while the server defaults are not loaded", () => {
    const admin: AdminUser = { ...USER, role: "admin", max_streams: 2 };
    const hints = savedUserPolicyInheritHints(
      admin,
      policyInheritHints(admin.role, null, [], undefined),
    );
    expect(
      inheritedValueText("maxStreams", hints, inheritContextFor(admin, []), LIBRARIES),
    ).toBeUndefined();
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

  it("is unknown while the group is not loaded", () => {
    const user = { ...USER, access_group_id: 9, max_streams: 3 };
    const ctx = inheritContextFor(user, [FAMILY]);
    const hints = savedUserPolicyInheritHints(
      user,
      policyInheritHints(user.role, 9, [FAMILY], POLICY_DEFAULTS),
    );
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
});

describe("permissionLock", () => {
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
