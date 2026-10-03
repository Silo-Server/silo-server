import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { Collection } from "@/api/types";
import Collections from "./Collections";

const collection = (name: string, creator: string) =>
  ({
    id: name,
    name,
    creator_profile_id: creator,
    collection_type: "mdblist",
    is_shared: creator !== "me",
    group_id: null,
    last_sync_status: "",
    sync_schedule: "",
  }) as Collection;

vi.mock("@/hooks/queries/collections", () => ({
  useCollectionCapabilities: () => ({
    data: { imports: true, groups: false, artwork: false, item_reorder: false },
  }),
  useCollections: () => ({
    data: [collection("Mine", "me"), collection("Theirs", "them")],
    isLoading: false,
  }),
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
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "me" } }),
}));

function card(name: string) {
  const link = screen.getByRole("link", { name });
  const found = link.closest('[data-slot="card"]');
  if (!(found instanceof HTMLElement)) throw new Error(`no card for ${name}`);
  return within(found);
}

describe("collection actions on the Collections page", () => {
  it("offers Sync, Edit and Delete only on collections the profile created", () => {
    render(
      <MemoryRouter>
        <Collections />
      </MemoryRouter>,
    );
    for (const action of ["Sync collection", "Edit collection", "Delete collection"]) {
      expect(card("Mine").getByRole("button", { name: action })).toBeTruthy();
      expect(card("Theirs").queryByRole("button", { name: action })).toBeNull();
    }
  });
});
