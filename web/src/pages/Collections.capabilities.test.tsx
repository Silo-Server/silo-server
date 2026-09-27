import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import Collections from "./Collections";

const capability = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/queries/collections", () => ({
  useCollectionCapabilities: capability,
  useCollections: () => ({ data: [], isLoading: false }),
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
  CollectionTemplateGallery: () => <div>Import gallery</div>,
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));

function show() {
  render(
    <MemoryRouter>
      <Collections />
    </MemoryRouter>,
  );
}

describe("collection capability controls", () => {
  beforeEach(() => vi.clearAllMocks());
  it("keeps manual creation while hiding unsupported imports", () => {
    capability.mockReturnValue({
      data: { imports: false, groups: false, artwork: false, item_reorder: false },
    });
    show();
    expect(screen.getByRole("button", { name: "New Collection" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Browse Templates" })).toBeNull();
    expect(screen.queryByText("Import gallery")).toBeNull();
  });
  it("shows import entry points when the store supports them", () => {
    capability.mockReturnValue({
      data: { imports: true, groups: true, artwork: true, item_reorder: true },
    });
    show();
    expect(screen.getByRole("button", { name: "Browse Templates" })).toBeTruthy();
    expect(screen.getByText("Import gallery")).toBeTruthy();
  });
});
