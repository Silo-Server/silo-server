import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Collection } from "@/api/types";
import Collections from "./Collections";

const collection = (id: string, creator: string) =>
  ({
    id,
    name: id,
    creator_profile_id: creator,
    collection_type: "manual",
    is_shared: creator !== "me",
    group_id: null,
    last_sync_status: "",
    sync_schedule: "",
  }) as Collection;

// Server order interleaves profiles; the page lists this profile's own
// collections first, then other profiles' grouped by owner.
const listed = [
  collection("Theirs 1", "them"),
  collection("Mine 1", "me"),
  collection("Other 1", "other"),
  collection("Theirs 2", "them"),
  collection("Mine 2", "me"),
];

vi.mock("@/hooks/queries/collections", () => ({
  useCollectionCapabilities: () => ({
    data: { imports: false, groups: true, artwork: false, item_reorder: true },
  }),
  useCollections: () => ({ data: listed, isLoading: false }),
  useCollectionGroups: () => ({ data: [] }),
  useServerCollections: () => ({ data: [] }),
  useCreateCollectionGroup: () => ({}),
  useDeleteCollection: () => ({}),
  useDeleteCollectionGroup: () => ({}),
  useReorderCollectionGroups: () => ({}),
  useReorderCollections: () => ({}),
  useUpdateCollection: () => ({}),
  useUpdateCollectionGroup: () => ({}),
}));
vi.mock("@/hooks/queries/userCollectionImports", () => ({ useSyncUserCollection: () => ({}) }));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "medium" } }),
}));
vi.mock("@/components/CollectionTemplateGallery", () => ({
  CollectionTemplateGallery: () => null,
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
const current = vi.hoisted(() => ({ profile: { id: "me" } as { id: string } | null }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: current.profile }),
}));

describe("collection order on the Collections page", () => {
  beforeEach(() => {
    current.profile = { id: "me" };
  });

  it("lists the profile's own collections first, then each owner's shared ones together", () => {
    render(
      <MemoryRouter>
        <Collections />
      </MemoryRouter>,
    );
    const names = screen
      .getAllByRole("link")
      .map((link) => link.textContent)
      .filter((name) => listed.some((item) => item.name === name));
    expect(names).toEqual(["Mine 1", "Mine 2", "Other 1", "Theirs 1", "Theirs 2"]);
  });

  it("offers dragging only on collections the profile created", () => {
    render(
      <MemoryRouter>
        <Collections />
      </MemoryRouter>,
    );
    expect(screen.getByRole("button", { name: "Drag Mine 1" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Drag Mine 2" })).toBeTruthy();
    for (const name of ["Theirs 1", "Theirs 2", "Other 1"])
      expect(screen.queryByRole("button", { name: `Drag ${name}` })).toBeNull();
  });

  it("keeps the server's order and offers no dragging until the profile resolves", () => {
    current.profile = null;
    render(
      <MemoryRouter>
        <Collections />
      </MemoryRouter>,
    );
    const names = screen
      .getAllByRole("link")
      .map((link) => link.textContent)
      .filter((name) => listed.some((item) => item.name === name));
    expect(names).toEqual(listed.map((item) => item.name));
    for (const item of listed)
      expect(screen.queryByRole("button", { name: `Drag ${item.name}` })).toBeNull();
  });
});
