import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";

import listCollections from "../../../contracts/api/v2/fixtures/list_collections_ok.json";
import libraryCollections from "../../../contracts/api/v2/fixtures/get_library_collections_ok.json";
import catalogItems from "../../../contracts/api/v2/fixtures/query_catalog_items_ok.json";
import type { CatalogResponse } from "@/api/types";
import { personalCapabilities } from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import Collections from "./Collections";
import LibraryCollections from "./LibraryCollections";
import Catalog from "./Catalog";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner" }, hasSelectedProfile: true }),
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "medium", caption: "title" } }),
}));
vi.mock("@/hooks/queries/sidebarPins", () => ({
  useToggleSidebarPin: () => ({ togglePin: vi.fn(), isPinned: () => false, canToggle: false }),
}));
vi.mock("@/hooks/queries/libraries", async () => ({
  ...(await vi.importActual<typeof import("@/hooks/queries/libraries")>(
    "@/hooks/queries/libraries",
  )),
  useUserLibraries: () => ({ data: [] }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/queries/shuffles", () => ({
  useStartShuffle: () => ({ startShuffle: vi.fn(), isStarting: false }),
}));
vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => ({ discoveryEnabled: false, isResolving: false }),
}));
vi.mock("@/components/catalog/CatalogFiltersPanel", () => ({ default: () => null }));
// Keep the actual catalog query and page; avoid virtual-grid geometry in jsdom.
vi.mock("@/components/ItemGrid", () => ({
  default: ({ pages }: { pages: Map<number, CatalogResponse["items"]> }) => (
    <ul aria-label="Collection members">
      {[...pages.values()].flat().map((item) => (
        <li key={item.content_id}>{item.title}</li>
      ))}
    </ul>
  ),
}));

installV2Recorder();

beforeEach(() => {
  v2Recorder.answer("GET /api/v2/settings/values/effective", { items: [] });
  // Deliberately no count_availability capability: decoding must stand alone.
  v2Recorder.answer("GET /api/v2/collections/capabilities", personalCapabilities);
  v2Recorder.answer("GET /api/v2/collections/server", { libraries: [] });
});

function show(path = "/collections") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/collections" element={<Collections />} />
          <Route path="/library/1" element={<LibraryCollections libraryId={1} />} />
          <Route path="/catalog" element={<Catalog />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return client;
}

it.each([
  [undefined, 7, "7 titles"],
  ["available", 0, "0 titles"],
  ["unavailable", 0, "Count unavailable"],
  ["future-status", 999, "Count unavailable"],
])(
  "renders a personal list count with status %s and keeps its actions",
  async (status, count, label) => {
    v2Recorder.answer("GET /api/v2/collections", {
      items: [{ ...listCollections.items[0], item_count: count, item_count_status: status }],
    });
    show();
    const link = await screen.findByRole("link", { name: "Rainy days" });
    const card = within(link.closest("li")!);
    expect(card.getByText(`Manual · ${label}`)).toBeInTheDocument();
    expect(link).toHaveAttribute("href", expect.stringContaining("source=user_collection"));
    await userEvent.click(card.getByRole("button", { name: "More for Rainy days" }));
    expect(await screen.findByRole("menuitem", { name: "Edit collection" })).toBeEnabled();
    expect(screen.getByRole("menuitem", { name: "Delete…" })).toBeEnabled();
  },
);

it("replaces an unavailable list count on refetch, including a recovered zero", async () => {
  let status = "unavailable";
  v2Recorder.answer("GET /api/v2/collections", () => ({
    items: [{ ...listCollections.items[0], item_count: 0, item_count_status: status }],
  }));
  const client = show();
  expect(await screen.findByText("Manual · Count unavailable")).toBeInTheDocument();
  status = "available";
  await act(() => client.invalidateQueries({ queryKey: ["collections"] }));
  expect(await screen.findByText("Manual · 0 titles")).toBeInTheDocument();
  expect(screen.queryByText(/Count unavailable/)).not.toBeInTheDocument();
});

it.each(["unavailable", "future-status"])(
  "keeps a library card with %s count and fetches its actual members",
  async (status) => {
    const card = {
      ...libraryCollections.ungrouped.collections[0]!,
      item_count: 999,
      item_count_status: status,
    };
    v2Recorder.answer("GET /api/v2/library/{id}/collections", {
      ...libraryCollections,
      groups: [],
      ungrouped: { sort_order: 0, collections: [card] },
    });
    v2Recorder.answer("POST /api/v2/catalog/query", {
      ...catalogItems,
      title: card.title,
      items: [{ ...catalogItems.items[0], title: "Synthetic member" }],
      total: 1,
    });
    show("/library/1");
    const link = await screen.findByRole("link", { name: new RegExp(card.title) });
    expect(within(link).getByText("Count unavailable")).toBeInTheDocument();
    expect(screen.queryByText("999")).not.toBeInTheDocument();
    await userEvent.click(link);
    expect(await screen.findByText("Synthetic member")).toBeInTheDocument();
    expect(v2Recorder.callsOf("POST /api/v2/catalog/query")[0]?.body).toMatchObject({
      source: "library_collection",
      collection_id: card.id,
    });
  },
);
