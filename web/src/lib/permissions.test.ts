import { describe, expect, it } from "vitest";
import { canEditMarkers, PERMISSION_MARKER_EDIT } from "./permissions";

describe("permissions", () => {
  it("allows admins to edit markers", () => {
    expect(canEditMarkers({ role: "admin", permissions: [] })).toBe(true);
  });

  it("allows assigned users to edit markers", () => {
    expect(canEditMarkers({ role: "user", permissions: [PERMISSION_MARKER_EDIT] })).toBe(true);
  });

  it("rejects users without marker edit permission", () => {
    expect(canEditMarkers({ role: "user", permissions: [] })).toBe(false);
  });
});
