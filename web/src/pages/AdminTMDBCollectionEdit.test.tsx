import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Library, LibraryCollection } from "@/api/types";
import { CollectionEditForm } from "./adminCollectionsShared";

const mocks = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: vi.fn(),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

const baseCollection = {
  id: "c",
  collection_type: "tmdb",
  library_id: 7,
  library_ids: [7],
  title: "Star Wars Saga",
  description: "",
  slug: "star-wars-saga",
  visibility: "visible",
  featured: false,
  sort_order: 0,
  group_id: null,
  query_definition: { library_ids: [7], match: "all", groups: [] },
  sort_config: {},
  last_sync_status: "success",
  last_sync_message: "",
  item_count: 9,
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
} as unknown as LibraryCollection;

// Radix Select scrolls and captures the pointer; jsdom implements neither.
if (!window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

let patches: Array<Record<string, unknown>>;

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  patches = [];
  mocks.request.mockImplementation(
    async (operation: string, args: { body?: Record<string, unknown> }) => {
      if (operation === "GET /api/v2/admin/collections/capabilities")
        return { artwork: false, groups: true, imports: true, item_reorder: true };
      if (operation === "PATCH /api/v2/admin/collections/{id}") {
        patches.push(args.body ?? {});
        return { ...baseCollection, ...args.body, library_id: "7", library_ids: ["7"] };
      }
      throw new Error(`Unexpected operation ${operation}`);
    },
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function renderEditor(overrides: Partial<LibraryCollection>) {
  const onClose = vi.fn();
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <CollectionEditForm
        etag={'"rev-1"'}
        collection={{ ...baseCollection, ...overrides }}
        libraries={[{ id: 7, name: "Movies", type: "movies" } as Library]}
        initialLibraryId={7}
        onClose={onClose}
      />
    </QueryClientProvider>,
  );
  return onClose;
}

describe("TMDB collection edit form", () => {
  it("keeps a franchise collection's source when other fields are saved", async () => {
    const franchise = { mode: "tmdb_collection", collection_id: 10 };
    const onClose = renderEditor({
      source_url: "tmdb://collection/10",
      source_config: franchise,
    });
    expect(screen.getByText(/can't be changed here/)).toBeInTheDocument();
    expect(screen.queryByText("Preset")).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Star Wars" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));

    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(patches).toHaveLength(1);
    expect(patches[0]).toMatchObject({ title: "Star Wars", source_config: franchise });
    expect(patches[0]?.source_url).toBeUndefined();
  });

  it("edits a TMDB list collection's URL and stores the canonical list URL", async () => {
    const onClose = renderEditor({
      title: "My list",
      source_url: "https://www.themoviedb.org/list/310",
      source_config: { mode: "tmdb_list", url: "https://www.themoviedb.org/list/310", limit: 40 },
    });
    const url = screen.getByLabelText("TMDB list URL");
    expect(url).toHaveValue("https://www.themoviedb.org/list/310");

    fireEvent.change(url, { target: { value: "https://www.themoviedb.org/list/8649937-marvel" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));

    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(patches[0]).toMatchObject({
      source_url: "https://www.themoviedb.org/list/8649937",
      source_config: {
        mode: "tmdb_list",
        url: "https://www.themoviedb.org/list/8649937",
        limit: 40,
      },
    });
  });

  it("starts the list URL empty when switching a preset collection to a list", () => {
    renderEditor({
      source_url: "tmdb://trending/all/day",
      source_config: { mode: "tmdb_preset", preset: "trending", media_type: "all" },
    });
    fireEvent.click(screen.getByRole("combobox", { name: /Source/ }));
    fireEvent.click(screen.getByRole("option", { name: /Public list/ }));
    const url = screen.getByLabelText("TMDB list URL");
    expect(url).toHaveValue("");
    expect(url).not.toHaveAttribute("aria-invalid");
  });

  it("blocks saving a TMDB list collection with a non-list URL", () => {
    renderEditor({
      source_url: "https://www.themoviedb.org/list/310",
      source_config: { mode: "tmdb_list", url: "https://www.themoviedb.org/list/310" },
    });
    fireEvent.change(screen.getByLabelText("TMDB list URL"), {
      target: { value: "https://www.themoviedb.org/movie/550" },
    });
    expect(screen.getByRole("button", { name: "Save Collection" })).toBeDisabled();
  });
});
