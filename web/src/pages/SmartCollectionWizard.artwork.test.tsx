import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LibraryCollection } from "@/api/types";
import SmartCollectionWizard from "./SmartCollectionWizard";
const mocks = vi.hoisted(() => ({ update: vi.fn(), remove: vi.fn() }));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useCreateAdminCollection: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateAdminCollection: () => ({ mutate: mocks.update, isPending: false }),
  useDeleteCollectionImage: () => ({ mutate: mocks.remove }),
  useAdminCollectionCapabilities: () => ({ data: { artwork: true } }),
}));
vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: () => ({ data: { totalItems: 1, pages: new Map() }, isLoading: false }),
}));
vi.mock("@/hooks/queries/libraries", () => ({ useUserLibraries: () => ({ data: [] }) }));
vi.mock("@/components/catalog/CatalogFiltersPanel", () => ({ default: () => null }));
vi.mock("@/components/ItemGrid", () => ({ default: () => null }));
vi.mock("@/components/ImageUploadField", () => ({
  ImageUploadField: ({
    label,
    currentUrl,
    onDelete,
    onFileChange,
    onSourceUrlChange,
  }: {
    label: string;
    currentUrl?: string;
    onDelete?: () => void;
    onFileChange: (file: File) => void;
    onSourceUrlChange: (url: string) => void;
  }) => (
    <div>
      {currentUrl && <img alt={label} src={currentUrl} />}
      <button type="button" onClick={onDelete}>
        Remove {label}
      </button>
      <button
        type="button"
        onClick={() => onFileChange(new File(["replacement"], "replacement.png"))}
      >
        Replace {label}
      </button>
      <button type="button" onClick={() => onSourceUrlChange("https://example.test/new.png")}>
        New URL {label}
      </button>
    </div>
  ),
}));
const collection = {
  id: "c",
  title: "Original",
  collection_type: "smart",
  library_id: 7,
  library_ids: [7],
  query_definition: { library_ids: [7] },
  poster_url: "https://example.test/poster",
  backdrop_url: "https://example.test/backdrop",
} as LibraryCollection;
beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});
afterEach(cleanup);
function show() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <SmartCollectionWizard
          mode="admin"
          collection={collection}
          etag={'"original"'}
          libraries={[]}
          initialLibraryId={7}
          onClose={vi.fn()}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Next: Details" }));
}
describe("wizard artwork draft", () => {
  it("retains staged removals across filter navigation and saves with the original validator", () => {
    show();
    fireEvent.click(screen.getByRole("button", { name: "Remove Poster" }));
    expect(screen.queryByAltText("Poster")).toBeNull();
    expect(mocks.remove).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Back to Filters" }));
    fireEvent.click(screen.getByRole("button", { name: "Next: Details" }));
    expect(screen.queryByAltText("Poster")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));
    expect(mocks.update).toHaveBeenCalledWith(
      expect.objectContaining({ etag: '"original"', removeArtwork: ["poster"] }),
      expect.any(Object),
    );
  });
  it.each(["Replace", "New URL"])("%s cancels a staged removal before saving", (replacement) => {
    show();
    fireEvent.click(screen.getByRole("button", { name: "Remove Backdrop" }));
    fireEvent.click(screen.getByRole("button", { name: `${replacement} Backdrop` }));
    fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));
    expect(mocks.update).toHaveBeenCalledWith(
      expect.objectContaining({ removeArtwork: [] }),
      expect.any(Object),
    );
    const submitted = mocks.update.mock.calls[0]?.[0];
    if (replacement === "Replace") expect(submitted.backdrop).toBeInstanceOf(File);
    else expect(submitted.body.backdrop_source_url).toBe("https://example.test/new.png");
    expect(mocks.remove).not.toHaveBeenCalled();
  });
});
