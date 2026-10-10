import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { v2Problem } from "@/api/v2/problems.test-support";
import { sectionKeys } from "@/hooks/queries/keys";
import AdminHomeRows from "./AdminHomeRows";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: mocks.error, warning: mocks.warning },
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: libraries }),
}));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollections: () => ({ data: collections }),
}));
vi.mock("@/hooks/queries/collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: vi.fn(),
}));
vi.mock("@/hooks/queries/useAllUserCollections", () => ({
  useAllUserCollections: () => ({
    collections: [
      { id: "private", source: "user" },
      { id: "public", source: "library" },
    ],
    isLoading: false,
  }),
}));
vi.mock("@/lib/recipes", () => ({
  fetchRecipeCatalog: async () => ({ categories: {} }),
  previewSection: async () => ({ items: [], total_count: 0 }),
}));
// The real list and sortable rows render; only the drag gesture is replaced by
// two buttons, so a test can start a drag, refetch, and then drop.
vi.mock("@dnd-kit/core", async () => {
  const actual = await vi.importActual<typeof import("@dnd-kit/core")>("@dnd-kit/core");
  return {
    ...actual,
    DndContext: ({
      children,
      onDragStart,
      onDragEnd,
      ...rest
    }: Parameters<typeof actual.DndContext>[0] & { children: ReactNode }) => (
      <actual.DndContext {...rest}>
        <button onClick={() => onDragStart?.({ active: { id: "a" } } as never)}>Start drag</button>
        <button onClick={() => onDragEnd?.({ active: { id: "a" }, over: { id: "b" } } as never)}>
          End drag
        </button>
        {children}
      </actual.DndContext>
    ),
  };
});
const libraries = [{ id: 7, name: "Movies", type: "movies" }];
const collections = [
  {
    id: "public",
    title: "Public picks",
    library_id: 7,
    library_ids: [7],
    collection_type: "manual",
    visibility: "visible",
    item_count: 3,
  },
  {
    id: "hidden",
    title: "Hidden picks",
    library_id: 7,
    library_ids: [7],
    collection_type: "manual",
    visibility: "hidden",
    item_count: 3,
  },
];
const initial = (id: string) => ({
  id,
  title: `Original ${id.toUpperCase()}`,
  scope: "home",
  library_id: null,
  position: id === "a" ? 0 : 1,
  section_type: "recently_added",
  item_limit: 20,
  featured: false,
  enabled: true,
  config: {},
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
});
let rows: ReturnType<typeof initial>[];
let revision: number;
let resetSupported: boolean;
let failID: string | null;
let writes: Array<{ operation: string; args: Args }>;
type Args = {
  query?: { scope?: string; library_id?: string };
  path?: { id: string };
  headers?: Record<string, string>;
  body?: Record<string, unknown>;
  onResponse?: (response: Response) => void;
};
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
  rows = [initial("a"), initial("b")];
  revision = 1;
  resetSupported = false;
  failID = null;
  writes = [];
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    args.onResponse?.(new Response(null, { headers: { ETag: `"rev-${revision}"` } }));
    if (operation === "GET /api/v2/admin/sections/capabilities")
      return { available: true, reset_profiles: resetSupported, preview: true };
    if (operation === "GET /api/v2/admin/sections/order")
      return { scope: "home", library_id: null, ordered_ids: rows.map((row) => row.id) };
    if (operation === "GET /api/v2/admin/sections")
      return { items: rows.map((row) => ({ ...row })) };
    if (operation === "GET /api/v2/admin/sections/{id}")
      return { ...rows.find((row) => row.id === args.path?.id)! };
    writes.push({ operation, args });
    if (args.headers?.["If-Match"] !== `"rev-${revision}"` || args.path?.id === failID)
      throw v2Problem(412, "precondition_failed", "Changed on another client");
    if (operation === "PATCH /api/v2/admin/sections/{id}") {
      const changes = Object.fromEntries(
        Object.entries(args.body ?? {}).filter(([, value]) => value !== undefined),
      );
      rows = rows.map((row) => (row.id === args.path?.id ? { ...row, ...changes } : row));
      return rows.find((row) => row.id === args.path?.id);
    }
    if (operation === "DELETE /api/v2/admin/sections/{id}") {
      rows = rows.filter((row) => row.id !== args.path?.id);
      return undefined;
    }
    if (operation === "PUT /api/v2/admin/sections/order")
      return { scope: "home", library_id: null, ordered_ids: args.body?.ordered_ids };
    if (operation === "PUT /api/v2/admin/sections/defaults") return { items: rows };
    throw new Error(`Unexpected ${operation}`);
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
async function setup(waitForRows = true, entry = "/admin/sections") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={client}>
        <AdminHomeRows />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  if (waitForRows) {
    await screen.findByRole("button", { name: "More for Original A" });
    await waitFor(() => expect(screen.getByRole("button", { name: "Add row" })).toBeEnabled());
  }
  return client;
}
async function chooseRowAction(title: string, action: string) {
  await userEvent.click(screen.getByRole("button", { name: `More for ${title}` }));
  await userEvent.click(await screen.findByRole("menuitem", { name: action }));
}
async function chooseMoreAction(action: string) {
  await userEvent.click(screen.getByRole("button", { name: "More" }));
  await userEvent.click(await screen.findByRole("menuitem", { name: action }));
}
const rowOrder = () => screen.getAllByRole("listitem").map((item) => item.dataset.rowId);
async function refresh(client: QueryClient) {
  await act(async () => {
    await client.invalidateQueries({ queryKey: sectionKeys.all });
  });
}

describe("admin section captured snapshots", () => {
  it("keeps the edit draft and its version through refetch and 412, then reloads explicitly", async () => {
    const client = await setup();
    await chooseRowAction("Original A", "Edit row…");
    const title = await screen.findByDisplayValue("Original A");
    fireEvent.change(title, { target: { value: "My draft" } });
    revision = 2;
    rows = rows.map((row) => ({ ...row, title: "Remote title", item_limit: 40 }));
    await refresh(client);
    expect(screen.getByDisplayValue("My draft")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText(/This row changed since you opened it/);
    expect(writes[0]!.args.headers?.["If-Match"]).toBe('"rev-1"');
    expect(writes).toHaveLength(1);
    expect(screen.getByDisplayValue("My draft")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Reload row" }));
    // The name the user typed stays; what they left alone takes the new value.
    await screen.findByText("Changed elsewhere: Row name, Number of titles");
    expect(screen.getByDisplayValue("My draft")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(writes).toHaveLength(2));
    expect(writes[1]!.args.headers?.["If-Match"]).toBe('"rev-2"');
    expect(writes[1]!.args.body).toMatchObject({ title: "My draft", item_limit: 40 });
  });
  it("freezes single-delete confirmation and requires explicit reload after 412", async () => {
    await setup();
    await chooseRowAction("Original A", "Delete row…");
    const dialog = await screen.findByRole("dialog", { name: "Delete Original A?" });
    revision = 2;
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete row" }));
    await screen.findByText(/Reload it before deleting/);
    expect(writes[0]!.args.headers?.["If-Match"]).toBe('"rev-1"');
    expect(writes).toHaveLength(1);
    expect(dialog).toBeInTheDocument();
  });
  it("captures bulk targets before confirmation and retains failed selection", async () => {
    const client = await setup();
    await chooseMoreAction("Select rows");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select Original A" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Select Original B" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete…" }));
    const dialog = await screen.findByRole("dialog", { name: "Delete 2 rows?" });
    rows.push(initial("c"));
    failID = "b";
    await refresh(client);
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete 2 rows" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(writes.map((write) => write.args.path?.id)).toEqual(["a", "b"]);
    expect(screen.getByRole("checkbox", { name: "Select Original B" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Select Original C" })).not.toBeChecked();
  });
  it("does not replace an active drag on refetch and preserves attempted order on 412", async () => {
    const client = await setup();
    fireEvent.click(screen.getByRole("button", { name: "Start drag" }));
    revision = 2;
    rows = [rows[1]!, rows[0]!];
    await refresh(client);
    expect(rowOrder()).toEqual(["a", "b"]);
    fireEvent.click(screen.getByRole("button", { name: "End drag" }));
    await screen.findByRole("button", { name: "Reload rows" });
    expect(writes[0]!.args.headers?.["If-Match"]).toBe('"rev-1"');
    expect(writes[0]!.args.body?.ordered_ids).toEqual(["b", "a"]);
    expect(rowOrder()).toEqual(["b", "a"]);
  });
  it("disables unsupported profile reset and keeps restore confirmation after stale save", async () => {
    await setup();
    await chooseMoreAction("Restore defaults…");
    const dialog = await screen.findByRole("dialog", { name: "Restore Home to the default rows?" });
    expect(within(dialog).getByRole("switch")).toBeDisabled();
    expect(
      within(dialog).getByText("Resetting profiles isn't available on this server."),
    ).toBeVisible();
    revision = 2;
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore defaults" }));
    await within(dialog).findByText(/These rows changed since you opened this/);
    expect(writes[0]!.args.headers?.["If-Match"]).toBe('"rev-1"');
    expect(writes[0]!.args.body?.reset_profiles).toBe(false);
    expect(dialog).toBeInTheDocument();
    // Reload reads the page again and takes a new version to restore over.
    fireEvent.click(within(dialog).getByRole("button", { name: "Reload rows" }));
    const restore = await within(dialog).findByRole("button", { name: "Restore defaults" });
    fireEvent.click(restore);
    await waitFor(() => expect(writes).toHaveLength(2));
    expect(writes[1]!.args.headers?.["If-Match"]).toBe('"rev-2"');
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
  it("restores with every profile reset when the server supports it, and names the collection rows", async () => {
    resetSupported = true;
    rows[1] = { ...rows[1]!, title: "Studio Ghibli", section_type: "collection" };
    await setup();
    await chooseMoreAction("Restore defaults…");
    const dialog = await screen.findByRole("dialog", { name: "Restore Home to the default rows?" });
    expect(dialog).toHaveTextContent("The 2 rows on Home are replaced by Silo's default rows.");
    expect(dialog).toHaveTextContent(
      "Studio Ghibli shows a collection and is removed. The collection itself stays in Collections.",
    );
    expect(dialog).toHaveTextContent("Library pages (Movies) don't change.");
    fireEvent.click(
      within(dialog).getByRole("switch", { name: "Also reset every profile's Home" }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore defaults" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(writes).toEqual([
      expect.objectContaining({ operation: "PUT /api/v2/admin/sections/defaults" }),
    ]);
    expect(writes[0]!.args.body?.reset_profiles).toBe(true);
  });
  it("names the collection rows as of the version Restore replaces", async () => {
    await setup();
    // Another admin adds a collection row after this page loaded.
    rows.push({ ...initial("c"), title: "Studio Ghibli", section_type: "collection" });
    revision = 2;
    await chooseMoreAction("Restore defaults…");
    const dialog = await screen.findByRole("dialog", { name: "Restore Home to the default rows?" });
    expect(dialog).toHaveTextContent("The 3 rows on Home are replaced by Silo's default rows.");
    expect(dialog).toHaveTextContent(
      "Studio Ghibli shows a collection and is removed. The collection itself stays in Collections.",
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore defaults" }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.args.headers?.["If-Match"]).toBe('"rev-2"');
  });
  it("names the other library pages when restoring a library page", async () => {
    libraries.push({ id: 8, name: "TV Shows", type: "tv" });
    const implementation = mocks.request.getMockImplementation()!;
    mocks.request.mockImplementation((operation: string, args: Args = {}) => {
      if (args.query?.scope === "library") {
        args.onResponse?.(new Response(null, { headers: { ETag: '"library-1"' } }));
        if (operation === "GET /api/v2/admin/sections/order")
          return Promise.resolve({ scope: "library", library_id: "7", ordered_ids: ["lib"] });
        if (operation === "GET /api/v2/admin/sections")
          return Promise.resolve({
            items: [{ ...initial("lib"), scope: "library", library_id: "7", title: "Movie row" }],
          });
      }
      return implementation(operation, args);
    });
    try {
      await setup(false, "/admin/sections?page=7");
      await screen.findByRole("button", { name: "More for Movie row" });
      await chooseMoreAction("Restore defaults…");
      const dialog = await screen.findByRole("dialog", {
        name: "Restore the Movies page to the default rows?",
      });
      expect(dialog).toHaveTextContent("The 1 row on the Movies page is replaced");
      expect(dialog).toHaveTextContent("Home and the other library pages (TV Shows) don't change.");
    } finally {
      libraries.pop();
    }
  });
  it("keeps Restore defaults off until a row write and its refetch land", async () => {
    await setup();
    const implementation = mocks.request.getMockImplementation()!;
    let releasePatch!: () => void;
    mocks.request.mockImplementation(async (operation: string, args: Args) => {
      if (operation === "PATCH /api/v2/admin/sections/{id}")
        await new Promise<void>((resolve) => {
          releasePatch = resolve;
        });
      return implementation(operation, args);
    });
    const restoreItem = async () => {
      await userEvent.click(screen.getByRole("button", { name: "More" }));
      return screen.findByRole("menuitem", { name: "Restore defaults…" });
    };
    expect(await restoreItem()).not.toHaveAttribute("data-disabled");
    await userEvent.keyboard("{Escape}");
    fireEvent.click(screen.getByRole("switch", { name: "Original A is on for everyone" }));
    await waitFor(() => expect(releasePatch).toBeTypeOf("function"));
    expect(await restoreItem()).toHaveAttribute("data-disabled");
    await userEvent.keyboard("{Escape}");
    releasePatch();
    await waitFor(() => expect(screen.getByRole("button", { name: "Home" })).toBeEnabled());
    expect(await restoreItem()).not.toHaveAttribute("data-disabled");
    expect(writes.map((write) => write.operation)).toEqual(["PATCH /api/v2/admin/sections/{id}"]);
  });
  it("turns selected rows off, reports the one that changed elsewhere, and keeps them selected", async () => {
    rows.push(initial("c"));
    await setup();
    await chooseMoreAction("Select rows");
    for (const title of ["Original A", "Original B", "Original C"])
      fireEvent.click(screen.getByRole("checkbox", { name: `Select ${title}` }));
    rows = rows.map((row) => (row.id === "b" ? { ...row, item_limit: 40 } : row));
    fireEvent.click(screen.getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(mocks.warning).toHaveBeenCalled());
    expect(mocks.warning).toHaveBeenCalledWith("Turned off 2 of 3 rows.", {
      description: "Original B changed since you opened this page.",
    });
    expect(writes.map((write) => [write.args.path?.id, write.args.body])).toEqual([
      ["a", { enabled: false }],
      ["c", { enabled: false }],
    ]);
    await screen.findByRole("switch", { name: "Original A is off for everyone" });
    expect(screen.getByRole("switch", { name: "Original B is on for everyone" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Select Original B" })).toBeChecked();
    expect(screen.getByRole("group", { name: "Selected rows" })).toHaveTextContent("3 selected");
  });
  it("keeps the selection bar's actions off until a row write and its refetch land", async () => {
    await setup();
    await chooseMoreAction("Select rows");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select Original A" }));
    const actions = ["Turn on", "Turn off", "Delete…"].map((name) =>
      screen.getByRole("button", { name }),
    );
    for (const action of actions) expect(action).toBeEnabled();
    const implementation = mocks.request.getMockImplementation()!;
    let releaseList: (() => void) | null = null;
    let patched = false;
    mocks.request.mockImplementation((operation: string, args: Args) => {
      if (operation === "PATCH /api/v2/admin/sections/{id}") patched = true;
      if (patched && operation === "GET /api/v2/admin/sections")
        return new Promise((resolve) => {
          releaseList = () => resolve(implementation(operation, args));
        });
      return implementation(operation, args);
    });
    fireEvent.click(screen.getByRole("switch", { name: "Original B is on for everyone" }));
    await waitFor(() => expect(releaseList).toBeTypeOf("function"));
    for (const action of actions) expect(action).toBeDisabled();
    await act(async () => releaseList!());
    await waitFor(() => {
      for (const action of actions) expect(action).toBeEnabled();
    });
    expect(writes.map((write) => write.operation)).toEqual(["PATCH /api/v2/admin/sections/{id}"]);
  });
  it("refuses to act on more than 100 selected rows", async () => {
    rows = Array.from({ length: 101 }, (_, index) => ({
      ...initial(index === 0 ? "a" : `r${index}`),
      position: index,
    }));
    await setup();
    await chooseMoreAction("Select rows");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select all" }));
    expect(screen.getByRole("group", { name: "Selected rows" })).toHaveTextContent(
      "Select up to 100 rows at a time.",
    );
    expect(screen.getByRole("button", { name: "Delete…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Turn off" })).toBeDisabled();
    expect(writes).toEqual([]);
  });
  it("drops the old header buttons", async () => {
    await setup();
    for (const name of ["Restore Defaults", "Delete All", "Delete Selected"])
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });
  it("restricts the admin collection picker to visible library collections", async () => {
    rows[0]!.section_type = "collection";
    rows[0]!.config = { library_collection_id: "public" };
    await setup();
    await chooseRowAction("Original A", "Edit row…");
    const dialog = await screen.findByRole("dialog", { name: "Edit row" });
    expect(
      within(dialog)
        .getAllByRole("radio")
        .map((radio) => radio.getAttribute("aria-label")),
    ).toEqual(["Public picks"]);
  });
  it("preserves collection recipe metadata on a title-only save", async () => {
    rows[0]!.section_type = "collection";
    rows[0]!.config = {
      library_collection_id: "public",
      source_provider: "trakt",
      source_preset: "popular",
      extra: { retained: true },
    };
    await setup();
    await chooseRowAction("Original A", "Edit row…");
    fireEvent.change(await screen.findByDisplayValue("Original A"), {
      target: { value: "Renamed" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.args.body?.config).toEqual({
      library_collection_id: "public",
      source_provider: "trakt",
      source_preset: "popular",
      extra: { retained: true },
    });
  });
  it("does not reopen an editor when a canceled reload completes", async () => {
    await setup();
    await chooseRowAction("Original A", "Edit row…");
    await screen.findByDisplayValue("Original A");
    revision = 2;
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText(/This row changed since you opened it/);
    const implementation = mocks.request.getMockImplementation()!;
    let finish!: () => void;
    mocks.request.mockImplementation((operation: string, args: Args) =>
      operation === "GET /api/v2/admin/sections/{id}"
        ? new Promise((resolve) => {
            finish = () => resolve(implementation(operation, args));
          })
        : implementation(operation, args),
    );
    fireEvent.click(screen.getByRole("button", { name: "Reload row" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await act(async () => {
      finish();
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
  it("retains pending reorder draft even when a background refresh finishes before rejection", async () => {
    const client = await setup();
    const implementation = mocks.request.getMockImplementation()!;
    let fail!: () => void;
    mocks.request.mockImplementation((operation: string, args: Args) =>
      operation === "PUT /api/v2/admin/sections/order"
        ? new Promise((_resolve, reject) => {
            fail = () => reject(v2Problem(412, "precondition_failed", "Stale order"));
          })
        : implementation(operation, args),
    );
    fireEvent.click(screen.getByRole("button", { name: "Start drag" }));
    fireEvent.click(screen.getByRole("button", { name: "End drag" }));
    await waitFor(() => expect(fail).toBeTypeOf("function"));
    revision = 2;
    await refresh(client);
    expect(rowOrder()).toEqual(["b", "a"]);
    await act(async () => {
      fail();
    });
    await screen.findByRole("button", { name: "Reload rows" });
    expect(rowOrder()).toEqual(["b", "a"]);
  });
  it("keeps scope controls fixed during a pending reorder and adopts the next scope after rejection", async () => {
    await setup();
    const implementation = mocks.request.getMockImplementation()!;
    let fail!: () => void;
    mocks.request.mockImplementation((operation: string, args: Args = {}) => {
      if (operation === "PUT /api/v2/admin/sections/order")
        return new Promise((_resolve, reject) => {
          fail = () => reject(v2Problem(412, "precondition_failed", "Stale home order"));
        });
      if (args.query?.scope === "library") {
        args.onResponse?.(new Response(null, { headers: { ETag: '"library-1"' } }));
        if (operation === "GET /api/v2/admin/sections/order")
          return { scope: "library", library_id: "7", ordered_ids: ["library-row"] };
        if (operation === "GET /api/v2/admin/sections")
          return {
            items: [
              {
                ...initial("library-row"),
                scope: "library",
                library_id: "7",
                title: "Library section",
              },
            ],
          };
      }
      return implementation(operation, args);
    });
    fireEvent.click(screen.getByRole("button", { name: "Start drag" }));
    fireEvent.click(screen.getByRole("button", { name: "End drag" }));
    await waitFor(() => expect(fail).toBeTypeOf("function"));
    fireEvent.click(screen.getByRole("button", { name: "Movies" }));
    expect(screen.getByRole("button", { name: "Home" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Movies" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Home" })).toHaveAttribute("aria-pressed", "true");
    await act(async () => {
      fail();
    });
    await screen.findByRole("button", { name: "Reload rows" });
    expect(screen.getByRole("button", { name: "Movies" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Movies" }));
    expect(screen.queryByRole("button", { name: "More for Original A" })).not.toBeInTheDocument();
    await screen.findByRole("button", { name: "More for Library section" });
    expect(screen.queryByRole("button", { name: "More for Original A" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reload rows" })).not.toBeInTheDocument();
  });
  it("shows a failed initial read instead of an empty editable scope", async () => {
    const implementation = mocks.request.getMockImplementation()!;
    mocks.request.mockImplementation((operation: string, args: Args) =>
      operation === "GET /api/v2/admin/sections"
        ? Promise.reject(new Error("Read unavailable"))
        : implementation(operation, args),
    );
    await setup(false);
    await screen.findByText("Read unavailable");
    expect(screen.queryByText(/No rows on/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add row" })).toBeDisabled();
  });
});

describe("admin Home rows list", () => {
  it("turns a row off with the version it read and holds reordering until the list refetches", async () => {
    await setup();
    const implementation = mocks.request.getMockImplementation()!;
    let releaseList: (() => void) | null = null;
    let patched = false;
    mocks.request.mockImplementation((operation: string, args: Args) => {
      if (operation === "PATCH /api/v2/admin/sections/{id}") patched = true;
      if (patched && operation === "GET /api/v2/admin/sections")
        return new Promise((resolve) => {
          releaseList = () => resolve(implementation(operation, args));
        });
      return implementation(operation, args);
    });
    const toggle = screen.getByRole("switch", { name: "Original A is on for everyone" });
    toggle.focus();
    await userEvent.keyboard(" ");
    expect(await screen.findByText(/is off\. Nobody sees this row\./)).toBeInTheDocument();
    await waitFor(() => expect(releaseList).toBeTypeOf("function"));
    expect(writes).toHaveLength(1);
    expect(writes[0]!.operation).toBe("PATCH /api/v2/admin/sections/{id}");
    expect(writes[0]!.args.headers?.["If-Match"]).toBe('"rev-1"');
    expect(writes[0]!.args.body).toEqual({ enabled: false });
    expect(screen.getByRole("button", { name: "Move Original B" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(screen.getByRole("button", { name: "Home" })).toBeDisabled();
    await act(async () => releaseList!());
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Move Original B" })).toHaveAttribute(
        "aria-disabled",
        "false",
      ),
    );
    expect(document.activeElement).toBe(
      screen.getByRole("switch", { name: "Original A is off for everyone" }),
    );
  });

  it("shows the conflict banner instead of writing when the row changed elsewhere", async () => {
    await setup();
    rows = rows.map((row) => (row.id === "a" ? { ...row, title: "Renamed elsewhere" } : row));
    fireEvent.click(screen.getByRole("switch", { name: "Original A is on for everyone" }));
    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent("These rows changed since you opened this page.");
    expect(writes).toEqual([]);
    expect(screen.getByRole("switch", { name: "Original A is on for everyone" })).toBeChecked();
    fireEvent.click(within(banner).getByRole("button", { name: "Reload rows" }));
    await screen.findByRole("switch", { name: "Renamed elsewhere is on for everyone" });
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
  });

  it("uses a row as the hero banner from its menu", async () => {
    await setup();
    await chooseRowAction("Original B", "Use as hero banner");
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.args.path?.id).toBe("b");
    expect(writes[0]!.args.body).toEqual({ featured: true });
    await waitFor(() =>
      expect(
        screen.getAllByRole("listitem").find((item) => item.dataset.rowId === "b"),
      ).toHaveTextContent("Hero banner"),
    );
  });

  it("moves focus to the next row's menu after a delete, or to Add row after the last", async () => {
    await setup();
    await chooseRowAction("Original A", "Delete row…");
    fireEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete row" }),
    );
    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole("button", { name: "More for Original B" }),
      ),
    );
    await chooseRowAction("Original B", "Delete row…");
    fireEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete row" }),
    );
    await screen.findByText("No rows on Home yet.");
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("button", { name: "Add row" })),
    );
  });

  it.each([
    ["Delete row…", "GET /api/v2/admin/sections/{id}"],
    ["Edit row…", "GET /api/v2/admin/sections/{id}"],
    ["Restore defaults…", "GET /api/v2/admin/sections/order"],
  ])("drops a %s read that finishes after switching pages", async (action, snapshotOperation) => {
    await setup();
    const implementation = mocks.request.getMockImplementation()!;
    let finish: (() => void) | null = null;
    mocks.request.mockImplementation((operation: string, args: Args = {}) => {
      if (args.query?.scope === "library") {
        args.onResponse?.(new Response(null, { headers: { ETag: '"library-1"' } }));
        if (operation === "GET /api/v2/admin/sections/order")
          return Promise.resolve({ scope: "library", library_id: "7", ordered_ids: ["lib"] });
        if (operation === "GET /api/v2/admin/sections")
          return Promise.resolve({
            items: [{ ...initial("lib"), scope: "library", library_id: "7", title: "Movie row" }],
          });
      }
      if (operation === snapshotOperation && finish === null)
        return new Promise((resolve) => {
          finish = () => resolve(implementation(operation, args));
        });
      return implementation(operation, args);
    });
    if (action === "Restore defaults…") await chooseMoreAction(action);
    else await chooseRowAction("Original A", action);
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    fireEvent.click(screen.getByRole("button", { name: "Movies" }));
    await screen.findByRole("button", { name: "More for Movie row" });
    await act(async () => finish!());
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(writes).toEqual([]);
  });

  it("opens the library page a ?page= link names", async () => {
    mocks.request.mockImplementation(
      (
        (implementation) =>
        (operation: string, args: Args = {}) => {
          if (args.query?.scope === "library") {
            args.onResponse?.(new Response(null, { headers: { ETag: '"library-1"' } }));
            if (operation === "GET /api/v2/admin/sections/order")
              return Promise.resolve({ scope: "library", library_id: "7", ordered_ids: ["lib"] });
            if (operation === "GET /api/v2/admin/sections")
              return Promise.resolve({
                items: [
                  { ...initial("lib"), scope: "library", library_id: "7", title: "Movie row" },
                ],
              });
          }
          return implementation(operation, args);
        }
      )(mocks.request.getMockImplementation()!),
    );
    await setup(false, "/admin/sections?page=7");
    await screen.findByRole("button", { name: "More for Movie row" });
    expect(screen.getByRole("button", { name: "Movies" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("These rows show above the full Movies grid.")).toBeInTheDocument();
  });
});
