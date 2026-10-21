import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminJob, Library, LibraryCollection } from "@/api/types";
import { useListReturnPath } from "@/lib/collections/listReturn";
import { coreApplied, starterPackBundles } from "@/test/fixtures/starterPacks";

import AdminCollections from "./AdminCollections";

const { state, idle } = vi.hoisted(() => ({
  state: {
    collections: [] as LibraryCollection[],
    jobs: [] as AdminJob[],
    bundlesLoaded: true,
    snapshot: vi.fn(),
    prepareDeletes: vi.fn(),
    setVisibility: vi.fn(async () => undefined),
  },
  idle: () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("@/api/adminCollections", () => ({
  fetchAdminCollectionSnapshot: state.snapshot,
  fetchAdminGroupSnapshot: vi.fn(),
  fetchAdminBoardOrderSnapshot: vi.fn(),
  prepareAdminCollectionDeletes: state.prepareDeletes,
  adminMutationMessage: (_: unknown, fallback: string) => fallback,
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [
      { id: 1, name: "Movies" },
      { id: 2, name: "Kids" },
      { id: 3, name: "4K" },
    ] as Library[],
  }),
}));
vi.mock("@/hooks/queries/admin/collectionGroups", () => ({
  useAdminCollectionsBoard: (libraryId: number | undefined) => ({
    isLoading: false,
    data:
      libraryId === undefined
        ? undefined
        : { groups: [], ungrouped: state.collections, ungroupedSortOrder: 0 },
  }),
  useCreateCollectionGroup: idle,
  useUpdateCollectionGroup: idle,
  useDeleteCollectionGroup: idle,
  useReorderCollectionGroups: idle,
  useReorderCollectionsInGroup: idle,
}));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollectionCapabilities: () => ({
    data: { groups: false, imports: true, import_sources: ["mdblist", "tmdb", "tmdb_list"] },
  }),
  useAdminCollections: () => ({
    data: state.collections,
    isLoading: false,
    refetch: async () => ({ data: state.collections }),
  }),
  useDeleteAdminCollections: () => ({ ...idle(), progress: null }),
  useSetAdminCollectionVisibility: () => ({ mutateAsync: state.setVisibility }),
  useSetAdminCollectionPin: idle,
  useTemplateBundleApplyJobs: () => ({ data: state.jobs }),
}));
vi.mock("@/lib/collectionTemplates", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/collectionTemplates")>()),
  useCollectionTemplateBundles: () => ({
    data: state.bundlesLoaded ? starterPackBundles : undefined,
  }),
}));
vi.mock("@/components/realtimeEventsContext", () => ({ useEventChannel: vi.fn() }));
vi.mock("@/components/collections/StarterPacksDialog", () => ({
  StarterPacksDialog: ({
    initialLibraryId,
    onClose,
  }: {
    initialLibraryId: number | null;
    onClose: () => void;
  }) => (
    <div role="dialog" aria-label="Starter packs">
      Opened on {String(initialLibraryId)}
      <button type="button" onClick={onClose}>
        Close
      </button>
    </div>
  ),
}));

function collection(
  id: string,
  collection_type: LibraryCollection["collection_type"],
  library_ids = [1],
): LibraryCollection {
  return {
    id,
    title: id,
    collection_type,
    library_id: library_ids[0]!,
    library_ids,
    item_count: 0,
    sort_order: 0,
    featured: false,
    visibility: "visible",
  } as LibraryCollection;
}

async function openStarterPacksFromMore() {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "More" }));
  await user.click(await screen.findByRole("menuitem", { name: "Starter packs…" }));
  // More runs a dialog-opening item only once the menu has closed.
  return screen.findByRole("dialog", { name: "Starter packs" });
}

function Where() {
  const location = useLocation();
  return <p data-testid="location">{location.pathname + location.search}</p>;
}

/** Stands in for the editor: where its Back would go. */
function EditorBack() {
  return <p data-testid="editor-back">{useListReturnPath("/fallback")}</p>;
}

function renderPage(path: string) {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/admin/collections" element={<AdminCollections />} />
          <Route path="/admin/collections/new" element={<EditorBack />} />
        </Routes>
        <Where />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  state.snapshot.mockImplementation(async (id: string) => ({
    collection: state.collections.find((entry) => entry.id === id),
    etag: '"rev-1"',
  }));
  state.prepareDeletes.mockImplementation(async (ids: string[]) =>
    ids.map((id) => ({
      id,
      etag: '"rev-1"',
      collection: state.collections.find((entry) => entry.id === id),
    })),
  );
});
afterEach(() => {
  state.jobs = [];
  state.bundlesLoaded = true;
  cleanup();
  vi.clearAllMocks();
});

describe("AdminCollections Arrange actions", () => {
  it("leaves the page on Back after Starter packs closes, instead of reopening it", async () => {
    state.collections = [collection("Top Rated", "mdblist")];
    function BackButton() {
      const navigate = useNavigate();
      return (
        <button type="button" onClick={() => void navigate(-1)}>
          Back
        </button>
      );
    }
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={["/admin", "/admin/collections"]} initialIndex={1}>
          <Routes>
            <Route path="/admin" element={<p>Admin home</p>} />
            <Route
              path="/admin/collections"
              element={
                <>
                  <BackButton />
                  <AdminCollections />
                </>
              }
            />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await openStarterPacksFromMore();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByText("Admin home")).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "Starter packs" })).toBeNull();
  });

  it("refreshes the collections and Home rows when a starter pack job ends", () => {
    const invalidate = vi.spyOn(QueryClient.prototype, "invalidateQueries");
    state.jobs = [
      {
        id: "job-1",
        job_type: "template_bundle_apply",
        status: "completed",
        requested_at: new Date().toISOString(),
        completed_at: new Date().toISOString(),
        result_payload: coreApplied,
      } as unknown as AdminJob,
    ];
    renderPage("/admin/collections");
    const keys = invalidate.mock.calls.map(([filters]) => filters?.queryKey);
    expect(keys).toContainEqual(["admin", "collections"]);
    expect(keys).toContainEqual(["sections"]);
    invalidate.mockRestore();
  });
});

describe("AdminCollections New collection", () => {
  it("sends the editor back to the list as it was, without the picker", async () => {
    state.collections = [collection("Staff Picks", "smart")];
    renderPage("/admin/collections?type=smart&dialog=new");
    const dialog = await screen.findByRole("dialog", { name: "New collection" });
    await userEvent.click(within(dialog).getByRole("link", { name: "Smart" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/admin/collections/new?type=smart");
    expect(screen.getByTestId("editor-back")).toHaveTextContent(
      /^\/admin\/collections\?type=smart$/,
    );
  });
});
