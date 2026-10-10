import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PERSONAL_SCOPE } from "@/lib/collections/scope";
import { ManualContentsPanel } from "./ManualContentsPanel";

const mocks = vi.hoisted(() => ({
  page: vi.fn(),
  mutate: vi.fn(),
  capabilities: vi.fn(),
  search: vi.fn(),
  v2: vi.fn(),
  orderedIds: ["first"],
}));
vi.mock("@/hooks/useDebounce", () => ({ useDebounce: (v: string) => v }));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.v2,
}));
vi.mock("@/hooks/queries/catalog", async () => ({
  ...(await vi.importActual<typeof import("@/hooks/queries/catalog")>("@/hooks/queries/catalog")),
  fetchCatalogPage: mocks.search,
}));
vi.mock("@/hooks/queries/collections", () => ({
  useCollectionItems: mocks.page,
  useCollectionCapabilities: mocks.capabilities,
  useCollectionItemOrderSnapshot: () => ({
    data: { ordered_ids: mocks.orderedIds, has_more: false, etag: '"order-one"' },
  }),
  useReorderCollectionItems: () => ({ mutate: mocks.mutate }),
}));
const item = (id: string, position = 0) => ({
  collection_id: "c",
  media_item_id: id,
  position,
  added_at: "2026-09-05T00:00:00Z",
});
function show(staged: string[] = []) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ManualContentsPanel
        scope={PERSONAL_SCOPE}
        collectionId="c"
        searchLibraries={[]}
        staged={staged}
        onStagedChange={vi.fn()}
        onItemsChanged={vi.fn()}
      />
    </QueryClientProvider>,
  );
}

describe("manual collection paging", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.orderedIds = ["first"];
    mocks.capabilities.mockReturnValue({ data: { item_reorder: true } });
    mocks.search.mockResolvedValue({ items: [] });
    mocks.v2.mockResolvedValue(undefined);
  });
  it("replaces the visible page and disables full-order dragging for partial membership", () => {
    mocks.page.mockImplementation((_id: string, cursor: string) => ({
      data: cursor
        ? { items: [item("second")], page: { has_more: false } }
        : { items: [item("first")], page: { has_more: true, next_cursor: "next" } },
      isLoading: false,
    }));
    show();
    expect(screen.queryByLabelText("Move first")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    expect(screen.queryByText("first")).toBeNull();
    expect(screen.getByText("second")).toBeTruthy();
    expect(screen.queryByLabelText("Move second")).toBeNull();
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
    expect(screen.queryByLabelText("Move first")).toBeNull();
    expect(screen.getByLabelText("Remove first")).toBeTruthy();
  });
  it("retains dragging for collections whose complete membership fits in one page", () => {
    mocks.page.mockReturnValue({
      data: { items: [item("first")], page: { has_more: false } },
      isLoading: false,
    });
    show();
    expect(screen.getByLabelText("Move first")).toBeTruthy();
  });
});

it("shows the catalog title while keeping mutation identifiers stable", async () => {
  mocks.search.mockResolvedValue({ items: [] });
  mocks.v2.mockResolvedValue(undefined);
  mocks.page.mockReturnValue({
    data: { items: [{ ...item("first"), title: "Interstellar" }], page: { has_more: false } },
    isLoading: false,
  });
  show();
  expect(screen.getByText("Interstellar")).toBeTruthy();
  fireEvent.click(screen.getByLabelText("Remove Interstellar"));
  await vi.waitFor(() =>
    expect(mocks.v2).toHaveBeenCalledWith("DELETE /api/v2/collections/{id}/items/{item_id}", {
      path: { id: "c", item_id: "first" },
    }),
  );
});

it("reports a failed manual item search without presenting it as no matches", async () => {
  mocks.page.mockReturnValue({ data: { items: [], page: { has_more: false } }, isLoading: false });
  mocks.search.mockRejectedValue(new Error("Invalid structured filters"));
  show();
  fireEvent.change(screen.getByRole("combobox", { name: "Add a title" }), {
    target: { value: "Interstellar" },
  });
  expect(await screen.findByText("The search didn't work.")).toBeTruthy();
  expect(screen.queryByText("No titles match.")).toBeNull();
  expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
});

it("sends each of two quick adds its own position, in the order they were picked", async () => {
  mocks.page.mockReturnValue({
    data: { items: [item("first")], page: { has_more: false } },
    isLoading: false,
  });
  const hit = (id: string, title: string) => ({ content_id: id, title, type: "movie" });
  mocks.search.mockResolvedValue({ items: [hit("alien", "Alien"), hit("heat", "Heat")] });
  // The first add is still on its way when the second is picked.
  let release!: () => void;
  mocks.v2.mockImplementationOnce(() => new Promise<void>((resolve) => (release = resolve)));
  mocks.v2.mockResolvedValue(undefined);
  show();
  fireEvent.change(screen.getByRole("combobox", { name: "Add a title" }), {
    target: { value: "a" },
  });
  fireEvent.click(await screen.findByRole("option", { name: /^Alien/ }));
  fireEvent.click(screen.getByRole("option", { name: /^Heat/ }));
  await act(async () => release());
  const adds = mocks.v2.mock.calls.filter(([operation]) => String(operation).startsWith("PUT"));
  expect(adds.map(([, options]) => [options.path.item_id, options.body.position])).toEqual([
    ["alien", 1],
    ["heat", 2],
  ]);
});

it("reorders only the saved titles when some couldn't be added", async () => {
  mocks.capabilities.mockReturnValue({ data: { item_reorder: true } });
  mocks.orderedIds = ["first", "second"];
  mocks.page.mockReturnValue({
    data: { items: [item("first", 0), item("second", 1)], page: { has_more: false } },
    isLoading: false,
  });
  const rects = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: Element,
  ) {
    const row = this.closest("li[data-title-id]");
    const top = row ? Array.from(row.parentElement!.children).indexOf(row) * 60 : 0;
    const height = row ? 60 : 0;
    return { x: 0, y: top, top, left: 0, right: 800, bottom: top + height, width: 800, height };
  } as () => DOMRect);
  show(["failed"]);
  expect(screen.getByText("Couldn't add")).toBeTruthy();
  const grip = screen.getByRole("button", { name: "Move second" });
  grip.focus();
  fireEvent.keyDown(grip, { code: "Space", key: " " });
  await screen.findByText(/Picked up .*, position 2 of 3/);
  fireEvent.keyDown(grip, { code: "ArrowUp", key: "ArrowUp" });
  await screen.findByText(/is now at position 1 of 3/);
  fireEvent.keyDown(grip, { code: "Space", key: " " });
  await vi.waitFor(() => expect(mocks.mutate).toHaveBeenCalled());
  expect(mocks.mutate.mock.calls[0]![0]).toEqual({
    orderedIds: ["second", "first"],
    etag: '"order-one"',
  });
  rects.mockRestore();
});
