import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Collection } from "@/api/types";
import Collections from "./Collections";

const listed = vi.hoisted(() => ({ collections: [] as Collection[] }));
const capability = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/queries/collections", () => ({
  useCollectionCapabilities: capability,
  useCollections: () => ({ data: listed.collections, isLoading: false }),
  useServerCollections: () => ({ data: [] }),
  useDeleteCollection: () => ({}),
  useReorderCollections: () => ({}),
  useSetCollectionShared: () => ({}),
}));
vi.mock("@/hooks/queries/userCollectionImports", () => ({ useSyncUserCollection: () => ({}) }));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({
    data: [
      { id: "p-kid", name: "Kid" },
      { id: "p-me", name: "Me" },
      { id: "p-parent", name: "Parent" },
    ],
  }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-me" } }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "medium" } }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));

function collection(id: string, name: string, creator: string, shared = false): Collection {
  return {
    id,
    name,
    profile_id: creator,
    creator_profile_id: creator,
    collection_type: "manual",
    is_shared: shared,
    query_definition: {
      library_ids: [],
      match: "all",
      groups: [],
      sort: { field: "added_at", order: "desc" },
    },
    sort_config: {},
    sort_order: 0,
    created_at: "",
    updated_at: "",
  };
}

function show() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <Collections />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function section(name: string) {
  return screen.getByRole("region", { name });
}

describe("Collections page sharing", () => {
  beforeEach(() => {
    capability.mockReturnValue({ data: { imports: false, item_reorder: true } });
    listed.collections = [
      collection("mine", "Rainy days", "p-me", true),
      collection("mine-2", "Road trips", "p-me"),
      collection("parent", "Family night", "p-parent", true),
      collection("kid", "Cartoons", "p-kid", true),
    ];
  });

  it("puts own collections first, then the ones other profiles share, naming each owner", () => {
    show();
    const cards = within(section("All collections")).getAllByRole("listitem");
    // Owners in the login's profile-list order; a shared card's link names its owner.
    const card = (name: RegExp) =>
      cards.findIndex((item) => within(item).queryByRole("link", { name }));
    expect(
      [/^Rainy days$/, /^Road trips$/, /^Cartoons\s*by Kid$/, /^Family night\s*by Parent$/].map(
        card,
      ),
    ).toEqual([0, 1, 2, 3]);
    // The Shared pill marks the profile's own shared collection.
    expect(within(cards[0]!).getByText("Shared")).toBeTruthy();
    expect(within(cards[3]!).queryByText("Shared")).toBeNull();
  });

  it("offers management actions only on the profile's own collections", () => {
    show();
    const wall = section("All collections");
    expect(
      within(wall)
        .getAllByRole("button", { name: /^More for / })
        .map((button) => button.getAttribute("aria-label")),
    ).toEqual(["More for Rainy days", "More for Road trips"]);
    expect(within(wall).getAllByRole("button", { name: /^Drag / })).toHaveLength(2);
    const familyNight = within(wall)
      .getByRole("link", { name: /^Family night/ })
      .closest("li")!;
    expect(within(familyNight).queryAllByRole("button")).toHaveLength(0);
  });

  it("reorders only when the store supports it", () => {
    capability.mockReturnValue({ data: { imports: false, item_reorder: false } });
    show();
    expect(screen.queryAllByRole("button", { name: /^Drag / })).toHaveLength(0);
  });
});
