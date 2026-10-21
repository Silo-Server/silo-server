import type { ReactNode } from "react";
import type { QueryClient } from "@tanstack/react-query";
import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { v2Problem } from "@/api/v2/problems.test-support";
import { sectionKeys } from "@/hooks/queries/keys";
import {
  type Args,
  patchRow,
  readSections,
  renderAdminHomeRows,
  type Row,
  stored,
  stubResizeObserver,
  type Write,
} from "./adminHomeRows.test-support";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({
  toast: { success: mocks.success, error: mocks.error, warning: mocks.warning },
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
const initial = (id: string) =>
  stored(id, { title: `Original ${id.toUpperCase()}`, position: id === "a" ? 0 : 1 });
let rows: Row[];
let revision: number;
let resetSupported: boolean;
let failID: string | null;
let writes: Write[];
beforeEach(() => {
  vi.clearAllMocks();
  stubResizeObserver();
  rows = [initial("a"), initial("b")];
  revision = 1;
  resetSupported = false;
  failID = null;
  writes = [];
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    args.onResponse?.(new Response(null, { headers: { ETag: `"rev-${revision}"` } }));
    const read = readSections(operation, args, rows, {
      here: rows,
      preview: true,
      resetProfiles: resetSupported,
    });
    if (read) return read;
    writes.push({ operation, args });
    if (args.headers?.["If-Match"] !== `"rev-${revision}"` || args.path?.id === failID)
      throw v2Problem(412, "precondition_failed", "Changed on another client");
    if (operation === "PATCH /api/v2/admin/sections/{id}") {
      rows = patchRow(rows, args);
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
  const { client } = renderAdminHomeRows(entry);
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
/** Opens Restore defaults on Home, with "Also restore every library page" on if asked. */
async function openRestore(libraryPages = false) {
  await chooseMoreAction("Restore defaults…");
  const dialog = await screen.findByRole("dialog", { name: "Restore Home to the default rows?" });
  if (libraryPages)
    fireEvent.click(
      within(dialog).getByRole("switch", { name: "Also restore every library page" }),
    );
  return dialog;
}
/** Makes the Movies page's restore fail with `error`, still recording the write. */
function failMoviesRestore(error: Error) {
  const implementation = mocks.request.getMockImplementation()!;
  mocks.request.mockImplementation((operation: string, args: Args = {}) => {
    if (operation === "PUT /api/v2/admin/sections/defaults" && args.query?.library_id === "7") {
      writes.push({ operation, args });
      return Promise.reject(error);
    }
    return implementation(operation, args);
  });
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
    const dialog = await openRestore();
    expect(
      within(dialog).getByRole("switch", { name: "Also reset every profile's Home" }),
    ).toBeDisabled();
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
  it("also restores every library page, each over its current version", async () => {
    resetSupported = true;
    await setup();
    const dialog = await openRestore(true);
    fireEvent.click(
      within(dialog).getByRole("switch", {
        name: "Also reset every profile's Home and library pages",
      }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore defaults" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(
      writes.map(({ operation, args }) => [operation, args.query, args.body?.reset_profiles]),
    ).toEqual([
      ["PUT /api/v2/admin/sections/defaults", { scope: "home", library_id: undefined }, true],
      ["PUT /api/v2/admin/sections/defaults", { scope: "library", library_id: "7" }, true],
    ]);
    expect(mocks.success).toHaveBeenCalledWith(
      "Restored the default rows on Home and 1 library page.",
    );
  });
  it("closes the restore dialog when the page changes while it is open", async () => {
    await setup();
    await openRestore(true);
    // Back or Forward moves ?page= while the dialog is open.
    fireEvent.click(screen.getByRole("button", { name: "Movies", hidden: true }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(writes).toEqual([]);
    // Opening it again on the new page starts with the switches off.
    await screen.findByRole("button", { name: "Movies", pressed: true });
    await chooseMoreAction("Restore defaults…");
    const again = await screen.findByRole("dialog", {
      name: "Restore the Movies page to the default rows?",
    });
    expect(within(again).queryByRole("switch", { checked: true })).not.toBeInTheDocument();
  });
  it("leaves the library pages alone when this page changed since the dialog opened", async () => {
    await setup();
    const dialog = await openRestore(true);
    revision = 2;
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore defaults" }));
    await within(dialog).findByText(/These rows changed since you opened this/);
    expect(writes).toHaveLength(1);
    expect(writes[0]!.args.query?.scope).toBe("home");
  });
  it("names the library pages that kept their rows", async () => {
    libraries.push({ id: 8, name: "TV Shows", type: "tv" });
    failMoviesRestore(v2Problem(404, "not_found", "Library not found"));
    try {
      await setup();
      const dialog = await openRestore(true);
      fireEvent.click(within(dialog).getByRole("button", { name: "Restore defaults" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      expect(writes.map(({ args }) => args.query?.library_id)).toEqual([undefined, "7", "8"]);
      expect(mocks.warning).toHaveBeenCalledWith(
        "Restored the default rows on Home and 1 library page. 1 library page kept its rows.",
        { description: "Movies: Library not found" },
      );
    } finally {
      libraries.pop();
    }
  });
  it("says a library page may have been restored when its restore got no answer", async () => {
    failMoviesRestore(new TypeError("Failed to fetch"));
    await setup();
    const dialog = await openRestore(true);
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore defaults" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(mocks.warning).toHaveBeenCalledWith(
      "Restored the default rows on Home. 1 library page may have been restored.",
      {
        description:
          "Movies: it may have been restored, but Silo couldn't confirm it. Reload it to check.",
      },
    );
  });
  it("names the collection rows as of the version Restore replaces", async () => {
    await setup();
    // Another admin adds a collection row after this page loaded.
    rows.push({ ...initial("c"), title: "Studio Ghibli", section_type: "collection" });
    revision = 2;
    const dialog = await openRestore();
    expect(dialog).toHaveTextContent("The 3 rows on Home are replaced by Silo's default rows.");
    expect(dialog).toHaveTextContent(
      "Studio Ghibli shows a collection and is removed. The collection itself stays in Collections.",
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore defaults" }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.args.headers?.["If-Match"]).toBe('"rev-2"');
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
});
