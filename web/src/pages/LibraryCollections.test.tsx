import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";

import LibraryCollections from "./LibraryCollections";

const auth = vi.hoisted(() => ({
  user: { id: 1, role: "user" } as { id: number; role: string },
  profile: { id: "p-me", is_primary: true } as { id: string; is_primary: boolean },
}));

vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => ({ user: auth.user, profile: auth.profile }),
}));

vi.mock("@/hooks/queries/libraryCollections", () => ({
  useLibraryCollections: () => ({
    isLoading: false,
    data: {
      groups: [
        {
          id: "lcg_user_1",
          name: "My collections",
          kind: "user_collections",
          sort_mode: "manual",
          sort_order: 0,
          collections: [
            {
              id: "mine",
              title: "Rainy days",
              poster_url: "",
              item_count: 3,
              creator_profile_id: "p-me",
            },
            {
              id: "theirs",
              title: "Family night",
              poster_url: "",
              item_count: 5,
              creator_profile_id: "p-parent",
            },
          ],
        },
      ],
      ungrouped: null,
    },
  }),
}));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({
    data: [
      { id: "p-me", name: "Me" },
      { id: "p-parent", name: "Parent" },
    ],
  }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: auth.profile, hasSelectedProfile: true }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({
    cardPresentation: { poster_size: "medium", caption: "title_metadata" },
  }),
}));
vi.mock("@/hooks/queries/sidebarPins", () => ({
  useToggleSidebarPin: () => ({ togglePin: vi.fn(), isPinned: () => false, canToggle: false }),
}));
vi.mock("@/hooks/useViewTransition", () => ({ useViewTransitionNavigate: () => vi.fn() }));

describe("LibraryCollections", () => {
  it("labels another profile's shared collection with its owner", () => {
    render(
      <MemoryRouter>
        <LibraryCollections libraryId={1} />
      </MemoryRouter>,
    );
    expect(screen.getByText("by Parent")).toBeTruthy();
    expect(screen.queryByText("by Me")).toBeNull();
    expect(screen.getByText("User collection")).toBeTruthy();
  });

  it("links the acting admin to Arrange shelves for this library", () => {
    auth.user = { id: 1, role: "admin" };
    auth.profile = { id: "p-me", is_primary: true };
    render(
      <MemoryRouter>
        <LibraryCollections libraryId={7} />
      </MemoryRouter>,
    );
    expect(screen.getByRole("link", { name: "Arrange shelves" })).toHaveAttribute(
      "href",
      "/admin/collections?libraryId=7&view=arrange",
    );
  });

  it("offers Arrange shelves to no one acting without admin powers", () => {
    for (const [user, profile] of [
      [
        { id: 1, role: "admin" },
        { id: "p-kid", is_primary: false },
      ],
      [
        { id: 2, role: "user" },
        { id: "p-me", is_primary: true },
      ],
    ] as const) {
      auth.user = user;
      auth.profile = profile;
      const { unmount } = render(
        <MemoryRouter>
          <LibraryCollections libraryId={7} />
        </MemoryRouter>,
      );
      expect(screen.queryByRole("link", { name: "Arrange shelves" })).not.toBeInTheDocument();
      unmount();
    }
  });
});
