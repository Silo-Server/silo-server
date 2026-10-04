// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  canEditMarkers,
  canUploadSubtitles,
  isActingAdmin,
  PERMISSION_MARKER_EDIT,
  PERMISSION_SUBTITLE_UPLOAD,
} from "./permissions";

describe("permissions", () => {
  it("allows admins with no profile selected to edit markers", () => {
    expect(canEditMarkers({ role: "admin", permissions: [] }, null)).toBe(true);
  });

  it("allows admins on the primary profile to edit markers", () => {
    expect(canEditMarkers({ role: "admin", permissions: [] }, { is_primary: true })).toBe(true);
  });

  it("rejects admins on a non-primary profile without the assigned permission", () => {
    expect(canEditMarkers({ role: "admin", permissions: [] }, { is_primary: false })).toBe(false);
  });

  it("allows admins on a non-primary profile with the assigned permission", () => {
    expect(
      canEditMarkers(
        { role: "admin", permissions: [PERMISSION_MARKER_EDIT] },
        { is_primary: false },
      ),
    ).toBe(true);
  });

  it("allows assigned users to edit markers", () => {
    expect(canEditMarkers({ role: "user", permissions: [PERMISSION_MARKER_EDIT] }, null)).toBe(
      true,
    );
  });

  it("rejects users without marker edit permission", () => {
    expect(canEditMarkers({ role: "user", permissions: [] }, null)).toBe(false);
  });
});

describe("canUploadSubtitles", () => {
  it("allows acting admins without an assigned permission", () => {
    expect(canUploadSubtitles({ role: "admin", permissions: [] }, null)).toBe(true);
  });

  it("rejects admins on a non-primary profile without the assigned permission", () => {
    expect(canUploadSubtitles({ role: "admin", permissions: [] }, { is_primary: false })).toBe(
      false,
    );
  });

  it("follows the assigned permission for users", () => {
    expect(
      canUploadSubtitles({ role: "user", permissions: [PERMISSION_SUBTITLE_UPLOAD] }, null),
    ).toBe(true);
    expect(canUploadSubtitles({ role: "user", permissions: [PERMISSION_MARKER_EDIT] }, null)).toBe(
      false,
    );
  });
});

describe("isActingAdmin", () => {
  it("is false for non-admin accounts regardless of profile", () => {
    expect(isActingAdmin({ role: "user" }, { is_primary: true })).toBe(false);
    expect(isActingAdmin(null, null)).toBe(false);
  });
});
