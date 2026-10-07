// @vitest-environment node

import { afterEach, describe, expect, it, vi } from "vitest";
import { PERMISSION_SUBTITLE_UPLOAD } from "@/lib/permissions";
import { useCanUploadSubtitles } from "./useCanUploadSubtitles";

const mocks = vi.hoisted(() => ({ auth: vi.fn(), profile: vi.fn() }));
vi.mock("@/hooks/useAuth", () => ({ useOptionalAuth: mocks.auth }));
vi.mock("@/hooks/useCurrentProfile", () => ({ useCurrentProfile: mocks.profile }));

afterEach(() => {
  mocks.auth.mockReset();
  mocks.profile.mockReset();
});

function given(user: object, profile: object | null, hasSelectedProfile: boolean) {
  mocks.auth.mockReturnValue({ user });
  mocks.profile.mockReturnValue({ profile, hasSelectedProfile });
}

describe("useCanUploadSubtitles", () => {
  it("fails closed while a selected profile is still loading", () => {
    given({ role: "admin", permissions: [] }, null, true);
    expect(useCanUploadSubtitles()).toBe(false);
  });

  it("grants an admin acting through the primary profile", () => {
    given({ role: "admin", permissions: [] }, { is_primary: true }, true);
    expect(useCanUploadSubtitles()).toBe(true);
  });

  it("grants an admin with no profile selected", () => {
    given({ role: "admin", permissions: [] }, null, false);
    expect(useCanUploadSubtitles()).toBe(true);
  });

  it("requires the assigned permission on a non-primary profile", () => {
    given({ role: "admin", permissions: [] }, { is_primary: false }, true);
    expect(useCanUploadSubtitles()).toBe(false);
    given({ role: "user", permissions: [PERMISSION_SUBTITLE_UPLOAD] }, { is_primary: false }, true);
    expect(useCanUploadSubtitles()).toBe(true);
  });
});
