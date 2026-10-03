import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Library, LibraryCollection } from "@/api/types";

import { CollectionEditForm, CollectionForm, MDBListImportForm } from "./adminCollectionsShared";

const mocks = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p1", name: "Alex" }] }),
}));
vi.mock("@/hooks/queries/collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: vi.fn(),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

const libraries = [
  { id: 7, name: "Movies", type: "movies" },
  { id: 8, name: "Shows", type: "tv" },
] as Library[];

const stored = {
  id: "c",
  collection_type: "manual",
  library_id: 7,
  library_ids: [7],
  title: "Original",
  description: "",
  slug: "original",
  visibility: "visible",
  featured: false,
  sort_order: 0,
  group_id: null,
  poster_url: "",
  backdrop_url: "",
  source_url: "",
  query_definition: {
    library_ids: [7],
    match: "all",
    groups: [],
    sort: { field: "title", order: "asc" },
  },
  sort_config: {},
  source_config: {},
  last_sync_status: "idle",
  last_sync_message: "",
  item_count: 3,
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
} as LibraryCollection;

// A legacy Trakt row written before the provider key existed: rebuilding it
// from the form fields would not reproduce it byte for byte.
const trakt = {
  ...stored,
  collection_type: "trakt",
  source_url: "trakt://recommended/movie/p1",
  source_config: {
    mode: "trakt_preset",
    preset: "recommended",
    media_type: "movie",
    profile_id: "p1",
    limit: 40,
  },
} as LibraryCollection;

let writes: { operation: string; body?: Record<string, unknown> }[];
beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  writes = [];
  mocks.request.mockImplementation(
    async (operation: string, args: { body?: Record<string, unknown> }) => {
      if (operation === "GET /api/v2/admin/collections/capabilities")
        return { artwork: false, groups: true, imports: true, item_reorder: true };
      if (operation.startsWith("GET")) return { items: [], total: 0 };
      writes.push({ operation, body: args.body });
      return { ...stored, ...args.body, library_id: "7", library_ids: ["7"] };
    },
  );
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

function renderForm(form: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(<QueryClientProvider client={client}>{form}</QueryClientProvider>);
}

function collectionModeSelect() {
  const select = screen
    .getAllByRole("combobox")
    .find((element) => element.textContent === "Manual");
  if (!select) throw new Error("Collection Mode select not found");
  return select;
}

describe("admin manual collection type", () => {
  it("cannot be switched once the collection exists", () => {
    renderForm(
      <CollectionForm
        etag={'"rev-1"'}
        collection={stored}
        libraries={libraries}
        initialLibraryId={7}
        onClose={vi.fn()}
      />,
    );

    expect(collectionModeSelect()).toBeDisabled();
  });

  it("can still be chosen for a new collection", () => {
    renderForm(
      <CollectionForm
        collection={null}
        libraries={libraries}
        initialLibraryId={7}
        onClose={vi.fn()}
      />,
    );

    expect(collectionModeSelect()).toBeEnabled();
  });
});

describe("legacy Trakt collection editor", () => {
  it("shows the source read-only and saves it unchanged", async () => {
    const onClose = vi.fn();
    renderForm(
      <CollectionEditForm
        etag={'"rev-1"'}
        collection={trakt}
        libraries={libraries}
        initialLibraryId={7}
        onClose={onClose}
      />,
    );

    expect(screen.getByText(/Silo no longer supports Trakt sources/)).toBeInTheDocument();
    expect(screen.getByLabelText("Max Items")).toBeDisabled();
    expect(screen.getByLabelText("Max Items")).toHaveValue(40);
    expect(screen.getByRole("button", { name: "Movies" })).toBeDisabled();
    for (const name of ["Discovery feed", "Recommended", "Alex"]) {
      const select = screen
        .getAllByRole("combobox")
        .find((element) => element.textContent?.startsWith(name));
      expect(select, name).toBeDisabled();
    }

    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));

    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(writes).toHaveLength(1);
    const body = writes[0]!.body!;
    expect(body.title).toBe("Renamed");
    expect(body.source_config).toEqual(trakt.source_config);
    expect(body.source_url).toBeUndefined();
    expect(body.library_ids).toEqual(["7"]);
  });

  it("cannot turn a sync schedule on", () => {
    renderForm(
      <CollectionEditForm
        etag={'"rev-1"'}
        collection={trakt}
        libraries={libraries}
        initialLibraryId={7}
        onClose={vi.fn()}
      />,
    );

    const schedule = screen
      .getAllByRole("combobox")
      .find((element) => element.textContent === "No automatic sync");
    expect(schedule).toBeDisabled();
  });

  it("can still change or stop a schedule it already has", () => {
    renderForm(
      <CollectionEditForm
        etag={'"rev-1"'}
        collection={{ ...trakt, sync_schedule: "0 3 * * *" }}
        libraries={libraries}
        initialLibraryId={7}
        onClose={vi.fn()}
      />,
    );

    const schedule = screen
      .getAllByRole("combobox")
      .find((element) => element.textContent === "Daily at 3:00 AM");
    expect(schedule).toBeEnabled();
  });
});

describe("admin MDBList Max Items", () => {
  it("does not import with a limit sync can never fill", async () => {
    renderForm(<MDBListImportForm libraries={libraries} initialLibraryId={7} onClose={vi.fn()} />);

    fireEvent.change(screen.getByLabelText("Collection Title"), { target: { value: "Top" } });
    fireEvent.change(screen.getByLabelText("MDBList JSON URL"), {
      target: { value: "https://mdblist.com/lists/u/top" },
    });
    fireEvent.change(screen.getByLabelText("Max Items"), { target: { value: "501" } });
    fireEvent.click(screen.getByRole("button", { name: "Import MDBList Collection" }));
    await act(async () => {});
    expect(writes).toEqual([]);

    fireEvent.change(screen.getByLabelText("Max Items"), { target: { value: "500" } });
    fireEvent.click(screen.getByRole("button", { name: "Import MDBList Collection" }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.body).toMatchObject({ limit: 500 });
  });

  it("does not save an edited limit sync can never fill", async () => {
    renderForm(
      <CollectionEditForm
        etag={'"rev-1"'}
        collection={{
          ...stored,
          collection_type: "mdblist",
          source_url: "https://mdblist.com/lists/u/top",
          source_config: { mode: "mdblist_json", url: "https://mdblist.com/lists/u/top" },
        }}
        libraries={libraries}
        initialLibraryId={7}
        onClose={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByLabelText("Max Items"), { target: { value: "501" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));
    await act(async () => {});
    expect(writes).toEqual([]);
  });
});
