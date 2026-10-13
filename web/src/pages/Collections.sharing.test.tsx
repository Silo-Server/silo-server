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
    <MemoryRouter>
      <Collections />
    </MemoryRouter>,
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

  it("splits own collections from the ones other profiles share", () => {
    show();
    const own = section("Your collections");
    expect(within(own).getByRole("link", { name: "Rainy days" })).toBeTruthy();
    expect(within(own).getByRole("link", { name: "Road trips" })).toBeTruthy();
    expect(within(own).queryByRole("link", { name: "Family night" })).toBeNull();
    // The Shared pill marks the profile's own shared collection.
    expect(within(own).getAllByText("Shared")).toHaveLength(1);

    const shared = section("Shared with me");
    // Owners in the login's profile-list order.
    expect(
      within(shared)
        .getAllByRole("heading")
        .map((heading) => heading.textContent),
    ).toEqual(["Shared with me", "by Kid", "by Parent"]);
    expect(within(shared).getByRole("link", { name: "Family night" })).toBeTruthy();
    expect(within(shared).getByRole("link", { name: "Cartoons" })).toBeTruthy();
  });

  it("offers management actions only on the profile's own collections", () => {
    show();
    const own = section("Your collections");
    expect(
      within(own)
        .getAllByRole("button", { name: /^More for / })
        .map((button) => button.getAttribute("aria-label")),
    ).toEqual(["More for Rainy days", "More for Road trips"]);
    expect(within(own).getAllByRole("button", { name: /^Drag / })).toHaveLength(2);
    const shared = section("Shared with me");
    expect(within(shared).queryAllByRole("button")).toHaveLength(0);
    expect(within(shared).getByRole("link", { name: "Family night" })).toBeTruthy();
  });

  it("reorders only when the store supports it", () => {
    capability.mockReturnValue({ data: { imports: false, item_reorder: false } });
    show();
    expect(screen.queryAllByRole("button", { name: /^Drag / })).toHaveLength(0);
  });

  it("hides Shared with me when no other profile shares a collection", () => {
    listed.collections = [collection("mine", "Rainy days", "p-me")];
    show();
    expect(screen.queryByRole("region", { name: "Shared with me" })).toBeNull();
    expect(section("Your collections")).toBeTruthy();
  });
});
