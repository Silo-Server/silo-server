// @vitest-environment node

import { describe, expect, it } from "vitest";

import {
  ADMIN_ONLY_SETTINGS_PATHS,
  USER_SETTINGS_GROUPS,
  visibleUserSettingsGroups,
} from "./userSettingsNav";

const ALL_PATHS = USER_SETTINGS_GROUPS.flatMap((group) => group.items.map((item) => item.path));

function visiblePaths(access: {
  canManageProfiles: boolean;
  requestsEnabled: boolean;
  actingAdmin: boolean;
}): string[] {
  return visibleUserSettingsGroups(access).flatMap((group) => group.items.map((item) => item.path));
}

const nonAdmin = { canManageProfiles: false, requestsEnabled: true, actingAdmin: false };

describe("visibleUserSettingsGroups", () => {
  it("offers an administrator every settings page", () => {
    const paths = visiblePaths({ ...nonAdmin, canManageProfiles: true, actingAdmin: true });
    expect([...paths].sort()).toEqual([...ALL_PATHS].sort());
  });

  it("withholds the admin-only pages from a profile viewer", () => {
    const paths = visiblePaths(nonAdmin);
    for (const path of ADMIN_ONLY_SETTINGS_PATHS) {
      expect(paths).not.toContain(path);
    }
  });

  it("keeps the pages a viewer still has to reach", () => {
    // How things play, how subtitles look, their own devices, and their alerts.
    expect(visiblePaths(nonAdmin)).toEqual(
      expect.arrayContaining(["playback", "subtitle-appearance", "devices", "notifications"]),
    );
  });

  it("does not let a primary profile through the admin gate", () => {
    // canManageProfiles covers Profiles and Account for a household parent. It
    // must not also re-open the admin-only pages, which are role-gated.
    const paths = visiblePaths({ ...nonAdmin, canManageProfiles: true });
    for (const path of ADMIN_ONLY_SETTINGS_PATHS) {
      expect(paths).not.toContain(path);
    }
    expect(paths).toEqual(expect.arrayContaining(["profiles", "account"]));
  });

  it("drops the requests page when the server has requests turned off", () => {
    const on = visiblePaths({ ...nonAdmin, actingAdmin: true });
    const off = visiblePaths({ ...nonAdmin, actingAdmin: true, requestsEnabled: false });
    expect(on).toContain("requests");
    expect(off).not.toContain("requests");
  });
});
