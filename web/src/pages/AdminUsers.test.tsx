import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { AdminUser } from "@/api/types";
import AdminUsers from "./AdminUsers";

const users: AdminUser[] = [
  {
    id: 2,
    username: "tom",
    email: "tom@example.com",
    role: "user",
    permissions: [],
    access_groups: [
      {
        id: 1,
        slug: "standard_user",
        name: "User",
        description: "Default user access",
        policy: {},
        built_in: true,
        protected: true,
        member_count: 1,
      },
    ],
    enabled: true,
    library_ids: null,
    max_playback_quality: "",
    max_streams: 4,
    max_transcodes: 1,
    max_profiles: 5,
    download_allowed: true,
    download_transcode_allowed: false,
    created_at: "2026-06-30T12:00:00Z",
    updated_at: "2026-06-30T12:00:00Z",
  },
];

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({ data: users, isLoading: false }),
  useAdminAccessGroups: () => ({ data: [], isLoading: false }),
  useAdminUserAccessExplanation: () => ({ data: undefined, isLoading: false }),
  useCreateUser: () => ({ isPending: false, mutate: vi.fn() }),
  useUpdateUser: () => ({ isPending: false, mutate: vi.fn() }),
  useDeleteUser: () => ({ isPending: false, mutate: vi.fn() }),
}));

vi.mock("@/hooks/useAdminCapabilities", () => ({
  useAdminAccess: () => ({
    actingAdmin: true,
    actionSet: new Set<string>(),
    can: () => true,
    canAccessAdmin: true,
    isLoading: false,
  }),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [] }),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: {} }),
  useUpdateServerSetting: () => ({ isPending: false, mutate: vi.fn() }),
}));

vi.mock("./admin-settings/InviteCodesTab", () => ({
  default: () => null,
}));

describe("AdminUsers", () => {
  it("renders an access explanation action for each user", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <AdminUsers />
      </MemoryRouter>,
    );

    expect(markup).toContain('aria-label="View tom access explanation"');
  });
});
