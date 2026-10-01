import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ManualCollectionItemsEditor } from "./ManualCollectionItemsEditor";

const mocks = vi.hoisted(() => ({
  page: vi.fn(),
  mutate: vi.fn(),
  capabilities: vi.fn(),
  search: vi.fn(),
}));
vi.mock("@/hooks/useDebounce", () => ({ useDebounce: (v: string) => v }));
vi.mock("@/hooks/queries/catalog", async () => ({
  ...(await vi.importActual<typeof import("@/hooks/queries/catalog")>("@/hooks/queries/catalog")),
  fetchCatalogPage: mocks.search,
}));
vi.mock("@/hooks/queries/collections", () => ({
  useCollectionItems: mocks.page,
  useCollectionCapabilities: mocks.capabilities,
  useCollectionItemOrderSnapshot: () => ({
    data: { ordered_ids: ["first"], has_more: false, etag: '"order-one"' },
  }),
  useReorderCollectionItems: () => ({ mutate: mocks.mutate }),
  useRemoveCollectionItem: () => ({ mutate: mocks.mutate }),
  useAddItemToCollection: () => ({ mutate: mocks.mutate, isPending: false }),
}));
const item = (id: string) => ({
  collection_id: "c",
  media_item_id: id,
  position: 0,
  added_at: "2026-09-05T00:00:00Z",
});
function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ManualCollectionItemsEditor collectionId="c" />
    </QueryClientProvider>,
  );
}

describe("manual collection paging", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.capabilities.mockReturnValue({ data: { item_reorder: true } });
  });
  it("replaces the visible page and disables full-order dragging for partial membership", () => {
    mocks.page.mockImplementation((_id: string, cursor: string) => ({
      data: cursor
        ? { items: [item("second")], page: { has_more: false } }
        : { items: [item("first")], page: { has_more: true, next_cursor: "next" } },
      isLoading: false,
    }));
    show();
    expect(screen.queryByLabelText("Drag item first")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    expect(screen.queryByText("first")).toBeNull();
    expect(screen.getByText("second")).toBeTruthy();
    expect(screen.queryByLabelText("Drag item second")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "First page" }));
    expect(screen.getByText("first")).toBeTruthy();
    expect(mocks.mutate).not.toHaveBeenCalled();
  });
  it("hides reordering while preserving removals when the store lacks reorder support", () => {
    mocks.capabilities.mockReturnValue({ data: { item_reorder: false } });
    mocks.page.mockReturnValue({
      data: { items: [item("first")], page: { has_more: false } },
      isLoading: false,
    });
    show();
    expect(screen.queryByLabelText("Drag item first")).toBeNull();
    expect(screen.getByLabelText("Remove item first")).toBeTruthy();
  });
  it("retains dragging for collections whose complete membership fits in one page", () => {
    mocks.page.mockReturnValue({
      data: { items: [item("first")], page: { has_more: false } },
      isLoading: false,
    });
    show();
    expect(screen.getByLabelText("Drag item first")).toBeTruthy();
  });
});

it("shows the catalog title while keeping mutation identifiers stable", () => {
  mocks.page.mockReturnValue({
    data: { items: [{ ...item("first"), title: "Interstellar" }], page: { has_more: false } },
    isLoading: false,
  });
  show();
  expect(screen.getByText("Interstellar")).toBeTruthy();
  fireEvent.click(screen.getByLabelText("Remove item Interstellar"));
  expect(mocks.mutate).toHaveBeenCalledWith("first", expect.any(Object));
});

it("reports a failed manual item search without presenting it as no matches", async () => {
  mocks.page.mockReturnValue({ data: { items: [], page: { has_more: false } }, isLoading: false });
  mocks.search.mockRejectedValue(new Error("Invalid structured filters"));
  show();
  fireEvent.change(screen.getByPlaceholderText("Search the catalog to add titles…"), {
    target: { value: "Interstellar" },
  });
  expect(await screen.findByText("Could not load search results.")).toBeTruthy();
  expect(screen.queryByText("No matches.")).toBeNull();
  expect(screen.getByRole("button", { name: "Retry search" })).toBeTruthy();
});
