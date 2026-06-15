import { describe, expect, it } from "vitest";
import { canEditMarkers, isActingAdmin, PERMISSION_MARKER_EDIT } from "./permissions";

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

describe("isActingAdmin", () => {
  it("is true for an admin with no profile selected", () => {
    expect(isActingAdmin({ role: "admin" }, null)).toBe(true);
  });

  it("is true for an admin on the primary profile", () => {
    expect(isActingAdmin({ role: "admin" }, { is_primary: true })).toBe(true);
  });

  it("is false for an admin on a non-primary profile", () => {
    expect(isActingAdmin({ role: "admin" }, { is_primary: false })).toBe(false);
  });

  it("is false for non-admin accounts regardless of profile", () => {
    expect(isActingAdmin({ role: "user" }, { is_primary: true })).toBe(false);
    expect(isActingAdmin(null, null)).toBe(false);
  });
});
