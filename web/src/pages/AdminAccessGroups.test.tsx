import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { AdminAccessGroup } from "@/api/types";
import AdminAccessGroups from "./AdminAccessGroups";

const groups: AdminAccessGroup[] = [
  {
    id: 1,
    slug: "standard_user",
    name: "User",
    description: "Default user access",
    policy: {},
    built_in: true,
    protected: true,
    member_count: 3,
  },
];

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminAccessGroups: () => ({ data: groups, isLoading: false }),
  useAdminAccessGroup: () => ({ data: undefined, isLoading: false }),
  useCreateAdminAccessGroup: () => ({ isPending: false, mutate: vi.fn() }),
  useUpdateAdminAccessGroup: () => ({ isPending: false, mutate: vi.fn() }),
  useDeleteAdminAccessGroup: () => ({ isPending: false, mutate: vi.fn() }),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [] }),
}));

describe("AdminAccessGroups", () => {
  it("renders clickable group rows with member counts", () => {
    const markup = renderToStaticMarkup(<AdminAccessGroups />);

    expect(markup).toContain(">Members<");
    expect(markup).toContain(">3<");
    expect(markup).toContain('aria-label="View User access group"');
  });

  it("renders helper controls for presets, actions, and policy limits", async () => {
    render(<AdminAccessGroups />);

    await userEvent.click(screen.getByRole("button", { name: "Add Group" }));

    expect(screen.getByLabelText(/^Junior Admin preset help:/)).toBeInTheDocument();
    expect(screen.getByLabelText(/^Server View help:/)).toBeInTheDocument();
    expect(screen.getByLabelText(/^Max Streams help:/)).toBeInTheDocument();
  });
});
