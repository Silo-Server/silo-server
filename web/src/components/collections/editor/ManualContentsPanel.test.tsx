import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PERSONAL_SCOPE, SERVER_SCOPE, type CollectionScope } from "@/lib/collections/scope";
import { ManualContentsPanel, type ManualContentsPanelProps } from "./ManualContentsPanel";

const mocks = vi.hoisted(() => ({
  page: vi.fn(),
  mutate: vi.fn(),
  capabilities: vi.fn(),
  adminCapabilities: vi.fn(),
  reloadCapabilities: vi.fn(),
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
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollectionCapabilities: mocks.adminCapabilities,
}));
const item = (id: string, position = 0) => ({
  collection_id: "c",
  media_item_id: id,
  position,
  added_at: "2026-09-05T00:00:00Z",
});
function show(
  staged: string[] = [],
  itemCount?: number | null,
  scope: CollectionScope = PERSONAL_SCOPE,
  overrides: Partial<ManualContentsPanelProps> = {},
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const props: ManualContentsPanelProps = {
    scope,
    collectionId: "c",
    searchLibraries: [],
    staged,
    itemCount,
    onStagedChange: vi.fn(),
    onItemsChanged: vi.fn(),
    ...overrides,
  };
  const panel = () => (
    <QueryClientProvider client={client}>
      <ManualContentsPanel {...props} />
    </QueryClientProvider>
  );
  const view = render(panel());
  return { ...view, refresh: () => view.rerender(panel()) };
}

const readyCapabilities = (itemAppend = false) => ({
  data: { item_reorder: true, item_append: itemAppend },
  isSuccess: true,
  isPending: false,
  isError: false,
  isFetching: false,
  refetch: mocks.reloadCapabilities,
});
const capabilityMock = (scope: CollectionScope) =>
  scope.kind === "personal" ? mocks.capabilities : mocks.adminCapabilities;

beforeEach(() => {
  vi.clearAllMocks();
  mocks.orderedIds = ["first"];
  mocks.capabilities.mockReturnValue(readyCapabilities());
  mocks.adminCapabilities.mockReturnValue(readyCapabilities());
  mocks.search.mockResolvedValue({ items: [] });
  mocks.v2.mockResolvedValue(undefined);
});
describe("manual collection paging", () => {
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
    mocks.capabilities.mockReturnValue({ ...readyCapabilities(), data: { item_reorder: false } });
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

it.each([PERSONAL_SCOPE, SERVER_SCOPE])(
  "appends from a partial page with an unavailable count in $kind scope",
  async (scope) => {
    capabilityMock(scope).mockReturnValue(readyCapabilities(true));
    mocks.page.mockReturnValue({
      data: {
        items: Array.from({ length: 200 }, (_, i) => item(`member-${i}`, i)),
        page: { has_more: true, next_cursor: "later-members" },
      },
      isLoading: false,
    });
    mocks.search.mockResolvedValue({
      items: [{ content_id: "new", title: "New title", type: "movie" }],
    });
    show([], null, scope);
    fireEvent.change(screen.getByRole("combobox", { name: "Add a title" }), {
      target: { value: "new" },
    });
    fireEvent.click(await screen.findByRole("option", { name: /^New title/ }));
    await vi.waitFor(() =>
      expect(mocks.v2).toHaveBeenCalledWith(
        scope.kind === "personal"
          ? "PUT /api/v2/collections/{id}/items/{item_id}"
          : "PUT /api/v2/admin/collections/{id}/items/{item_id}",
        { path: { id: "c", item_id: "new" }, query: { append: true }, body: { position: 0 } },
      ),
    );
  },
);

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
  mocks.capabilities.mockReturnValue(readyCapabilities());
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

it("loads and removes real members independently of an unavailable summary count", async () => {
  mocks.page.mockReturnValue({
    data: { items: [{ ...item("first"), title: "Synthetic member" }], page: { has_more: false } },
    isLoading: false,
  });
  show([], null);
  expect(screen.getByText("Synthetic member")).toBeInTheDocument();
  expect(screen.queryByText(/No titles/)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Remove Synthetic member" }));
  await vi.waitFor(() =>
    expect(mocks.v2).toHaveBeenCalledWith("DELETE /api/v2/collections/{id}/items/{item_id}", {
      path: { id: "c", item_id: "first" },
    }),
  );
});

it("restores the removed position when atomic append is supported", async () => {
  mocks.capabilities.mockReturnValue(readyCapabilities(true));
  mocks.page.mockReturnValue({
    data: {
      items: [{ ...item("first", 31), title: "Synthetic member" }],
      page: { has_more: false },
    },
    isLoading: false,
  });
  show([], null);
  fireEvent.click(screen.getByRole("button", { name: "Remove Synthetic member" }));
  fireEvent.click(await screen.findByRole("button", { name: "Undo" }));
  await vi.waitFor(() =>
    expect(mocks.v2).toHaveBeenCalledWith("PUT /api/v2/collections/{id}/items/{item_id}", {
      path: { id: "c", item_id: "first" },
      body: { position: 31 },
    }),
  );
});

describe.each([PERSONAL_SCOPE, SERVER_SCOPE])("append readiness in $kind scope", (scope) => {
  const putOperation =
    scope.kind === "personal"
      ? "PUT /api/v2/collections/{id}/items/{item_id}"
      : "PUT /api/v2/admin/collections/{id}/items/{item_id}";
  const unavailable = (phase: string) => ({
    data: undefined,
    isSuccess: false,
    isPending: phase === "pending",
    isError: phase === "failed",
    isFetching: phase === "pending",
    refetch: mocks.reloadCapabilities,
  });
  const partialPage = () => ({
    data: { items: [item("first", 0)], page: { has_more: true, next_cursor: "next" } },
    isLoading: false,
  });
  const search = async () => {
    mocks.search.mockResolvedValue({
      items: [{ content_id: "new", title: "New title", type: "movie" }],
    });
    fireEvent.change(screen.getByRole("combobox", { name: "Add a title" }), {
      target: { value: "new" },
    });
    return screen.findByRole("option", { name: /^New title/ });
  };
  it.each([
    { phase: "pending", complete: false, loading: false },
    { phase: "failed", complete: false, loading: false },
    { phase: "pending", complete: true, loading: false },
    { phase: "failed", complete: true, loading: false },
    { phase: "failed", complete: false, loading: true },
  ])(
    "blocks adding and staged retry while capabilities are $phase (complete membership: $complete)",
    ({ phase, complete, loading }) => {
      capabilityMock(scope).mockReturnValue(unavailable(phase));
      mocks.page.mockReturnValue(
        loading
          ? { isLoading: true }
          : complete
            ? { data: { items: [item("first", 31)], page: { has_more: false } }, isLoading: false }
            : partialPage(),
      );
      const retry = vi.fn();
      show(["failed-title"], null, scope, { onRetryStaged: retry });
      const input = screen.getByRole("combobox", { name: "Add a title" });
      expect(input).toBeDisabled();
      expect(screen.getByRole("button", { name: "Try again" })).toBeDisabled();
      expect(
        screen.getByText(
          phase === "pending"
            ? "Checking whether titles can be added…"
            : "Couldn't check whether titles can be added. Try loading again.",
        ),
      ).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Try loading again" }) !== null).toBe(
        phase === "failed",
      );
      expect(
        screen.getByText(
          phase === "pending"
            ? "Checking whether titles can be added…"
            : "Couldn't check whether titles can be added. Try loading again.",
        ).parentElement,
      ).toHaveAttribute("role", phase === "pending" ? "status" : "alert");
      fireEvent.keyDown(input, { key: "Enter" });
      fireEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(retry).not.toHaveBeenCalled();
      expect(mocks.v2).not.toHaveBeenCalled();
    },
  );
  it.each(["pending", "failed"])(
    "guards already-open search results when capabilities become %s",
    async (phase) => {
      capabilityMock(scope).mockReturnValue(readyCapabilities(true));
      mocks.page.mockReturnValue(partialPage());
      const view = show([], null, scope);
      const option = await search();
      capabilityMock(scope).mockReturnValue(unavailable(phase));
      view.refresh();
      expect(option).toHaveAttribute("aria-disabled", "true");
      fireEvent.click(option);
      fireEvent.keyDown(screen.getByRole("combobox", { name: "Add a title" }), { key: "Enter" });
      expect(mocks.v2).not.toHaveBeenCalled();
    },
  );
  it.each(["pending", "failed"])(
    "keeps removal and positional undo available when capabilities are %s",
    async (phase) => {
      capabilityMock(scope).mockReturnValue(unavailable(phase));
      mocks.page.mockReturnValue({
        data: { items: [item("first", 31)], page: { has_more: false } },
        isLoading: false,
      });
      show([], null, scope);
      fireEvent.click(screen.getByRole("button", { name: "Remove first" }));
      fireEvent.click(await screen.findByRole("button", { name: "Undo" }));
      await vi.waitFor(() =>
        expect(mocks.v2).toHaveBeenCalledWith(putOperation, {
          path: { id: "c", item_id: "first" },
          body: { position: 31 },
        }),
      );
    },
  );
  it.each(["pending", "failed"])(
    "keeps create-mode staging available when capabilities are %s",
    async (phase) => {
      capabilityMock(scope).mockReturnValue(unavailable(phase));
      mocks.page.mockReturnValue({ isLoading: false });
      const staged = vi.fn();
      show([], null, scope, { collectionId: undefined, onStagedChange: staged });
      expect(screen.getByRole("combobox", { name: "Add a title" })).toBeEnabled();
      fireEvent.click(await search());
      expect(staged).toHaveBeenCalledWith(["new"]);
      expect(mocks.v2).not.toHaveBeenCalled();
    },
  );
  it.each(["pending", "failed"])(
    "recovers add and staged retry after a real capability query reload from %s",
    async (phase) => {
      let rejectPending!: (error: Error) => void;
      const fetchCapabilities = vi
        .fn()
        .mockImplementationOnce(() =>
          phase === "pending"
            ? new Promise((_resolve, reject) => {
                rejectPending = reject;
              })
            : Promise.reject(new Error("Unavailable")),
        )
        .mockResolvedValue(readyCapabilities(true).data);
      capabilityMock(scope).mockImplementation(function useTestCapabilities() {
        return useQuery({
          queryKey: ["testCapabilities", scope.kind],
          queryFn: fetchCapabilities,
          retry: false,
        });
      });
      mocks.page.mockReturnValue(partialPage());
      const retry = vi.fn();
      show(["failed-title"], null, scope, { onRetryStaged: retry });
      expect(screen.getByRole("combobox", { name: "Add a title" })).toBeDisabled();
      if (phase === "pending") {
        expect(screen.getByText("Checking whether titles can be added…")).toBeInTheDocument();
        await act(async () => rejectPending(new Error("Unavailable")));
      }
      await screen.findByText("Couldn't check whether titles can be added. Try loading again.");
      expect(screen.getByRole("button", { name: "Try again" })).toBeDisabled();
      fireEvent.click(screen.getByRole("button", { name: "Try loading again" }));
      await vi.waitFor(() =>
        expect(screen.getByRole("combobox", { name: "Add a title" })).toBeEnabled(),
      );
      fireEvent.click(await search());
      await vi.waitFor(() =>
        expect(mocks.v2).toHaveBeenCalledWith(putOperation, {
          path: { id: "c", item_id: "new" },
          query: { append: true },
          body: { position: 0 },
        }),
      );
      fireEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(retry).toHaveBeenCalledExactlyOnceWith("append");
      expect(fetchCapabilities).toHaveBeenCalledTimes(2);
    },
  );
  it.each([null, 500])(
    "never guesses from a partial membership page with summary count %s",
    (count) => {
      mocks.page.mockImplementation((_id: string, cursor: string) => ({
        data: {
          items: [item("first", cursor ? 499 : 0)],
          page: { has_more: !cursor, next_cursor: "next" },
        },
        isLoading: false,
      }));
      const retry = vi.fn();
      show(["failed-title"], count, scope, { onRetryStaged: retry });
      expect(screen.getByRole("combobox", { name: "Add a title" })).toBeDisabled();
      expect(
        screen.getByText(
          /This server can't safely add titles while only part of the collection is loaded/,
        ),
      ).toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: "Next page" }));
      expect(screen.getByRole("combobox", { name: "Add a title" })).toBeDisabled();
      fireEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(retry).not.toHaveBeenCalled();
      expect(mocks.v2).not.toHaveBeenCalled();
    },
  );
  it("blocks stale successful capability data after a failed reload", () => {
    capabilityMock(scope).mockReturnValue({
      ...unavailable("failed"),
      data: readyCapabilities(true).data,
    });
    mocks.page.mockReturnValue({
      data: { items: [], page: { has_more: false } },
      isLoading: false,
    });
    show([], null, scope);
    expect(screen.getByRole("combobox", { name: "Add a title" })).toBeDisabled();
  });
  it("waits for complete legacy membership and allows recovery when it loads", async () => {
    mocks.page.mockReturnValue({ isLoading: true });
    const view = show([], 999, scope);
    expect(screen.getByRole("combobox", { name: "Add a title" })).toBeDisabled();
    expect(screen.getByText("Loading titles before adding more…").parentElement).toHaveAttribute(
      "role",
      "status",
    );
    expect(screen.queryByRole("button", { name: "Try loading again" })).not.toBeInTheDocument();
    mocks.page.mockReturnValue({
      data: { items: [item("first", 31)], page: { has_more: false } },
      isLoading: false,
    });
    view.refresh();
    fireEvent.click(await search());
    await vi.waitFor(() =>
      expect(mocks.v2).toHaveBeenCalledWith(putOperation, {
        path: { id: "c", item_id: "new" },
        body: { position: 32 },
      }),
    );
  });
  it.each([
    { label: "empty", positions: [], tail: 0 },
    { label: "contiguous", positions: [0, 1], tail: 2 },
    { label: "sparse", positions: [4, 31], tail: 32 },
  ])(
    "uses stored positions for legacy $label membership, including staged retry and quick adds",
    async ({ positions, tail }) => {
      mocks.page.mockReturnValue({
        data: {
          items: positions.map((position, i) => item(`member-${i}`, position)),
          page: { has_more: false },
        },
        isLoading: false,
      });
      const retry = vi.fn();
      mocks.search.mockResolvedValue({
        items: [
          { content_id: "new", title: "New title", type: "movie" },
          { content_id: "other", title: "Other title", type: "movie" },
          { content_id: "last", title: "Last title", type: "movie" },
        ],
      });
      let release!: () => void;
      mocks.v2.mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            release = resolve;
          }),
      );
      show(["failed-title", "another-failed"], 999, scope, { onRetryStaged: retry });
      fireEvent.change(screen.getByRole("combobox", { name: "Add a title" }), {
        target: { value: "title" },
      });
      fireEvent.click(await screen.findByRole("option", { name: /^New title/ }));
      fireEvent.click(screen.getByRole("option", { name: /^Other title/ }));
      fireEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(retry).toHaveBeenCalledExactlyOnceWith(tail + 2);
      fireEvent.click(screen.getByRole("option", { name: /^Last title/ }));
      await act(async () => release());
      expect(mocks.v2.mock.calls.filter(([op]) => String(op).startsWith("PUT"))).toEqual([
        [putOperation, { path: { id: "c", item_id: "new" }, body: { position: tail } }],
        [putOperation, { path: { id: "c", item_id: "other" }, body: { position: tail + 1 } }],
        [putOperation, { path: { id: "c", item_id: "last" }, body: { position: tail + 4 } }],
      ]);
    },
  );
  it.each([{ positions: [] }, { positions: [0, 1] }, { positions: [4, 31] }])(
    "uses atomic append for complete membership at positions $positions and quick adds",
    async ({ positions }) => {
      capabilityMock(scope).mockReturnValue(readyCapabilities(true));
      mocks.page.mockReturnValue({
        data: {
          items: positions.map((position, i) => item(`member-${i}`, position)),
          page: { has_more: false },
        },
        isLoading: false,
      });
      mocks.search.mockResolvedValue({
        items: [
          { content_id: "new", title: "New title", type: "movie" },
          { content_id: "other", title: "Other title", type: "movie" },
        ],
      });
      let release!: () => void;
      mocks.v2.mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            release = resolve;
          }),
      );
      const retry = vi.fn();
      show(["failed-title"], null, scope, { onRetryStaged: retry });
      fireEvent.change(screen.getByRole("combobox", { name: "Add a title" }), {
        target: { value: "title" },
      });
      fireEvent.click(await screen.findByRole("option", { name: /^New title/ }));
      fireEvent.click(screen.getByRole("option", { name: /^Other title/ }));
      fireEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(retry).toHaveBeenCalledExactlyOnceWith("append");
      await act(async () => release());
      expect(mocks.v2.mock.calls.filter(([op]) => String(op).startsWith("PUT"))).toEqual([
        [
          putOperation,
          { path: { id: "c", item_id: "new" }, query: { append: true }, body: { position: 0 } },
        ],
        [
          putOperation,
          { path: { id: "c", item_id: "other" }, query: { append: true }, body: { position: 0 } },
        ],
      ]);
    },
  );
});
