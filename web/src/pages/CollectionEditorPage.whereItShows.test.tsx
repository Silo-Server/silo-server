/**
 * Where it shows on a server collection's editor: the Home and library page
 * rows that show it, Add as a row into Home rows (asking first about unsaved
 * changes), hiding a collection rows use, and deleting it with its rows.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  CHANGED_BEFORE_DELETE,
  DISCARD_KEEPS_IT_HIDDEN,
  ROWS_NOT_LISTED,
  ROWS_ONCE_CREATED,
  SAVE_AFTER_CONFLICTS,
  SHOW_IT_FIRST,
  SHOW_ON_TAB_LABEL,
} from "@/lib/collections/copy";
import { safeReturnPath, type AddedRowState } from "@/lib/homeRows/rowLinks";
import {
  adminCapabilities,
  adminCollection,
  adminCollectionList,
  personalCapabilities,
} from "@/test/fixtures/collectionAnswers";
import { uploadArtwork } from "@/test/collectionArtwork";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";
import CollectionEditorPage from "./CollectionEditorPage";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("@/hooks/useDebounce", () => ({ useDebounce: (value: string) => value }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner", name: "Sam" }, isLoading: false }),
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [
      { id: 1, name: "Movies", type: "movies" },
      { id: 2, name: "Kids", type: "movies" },
      { id: 3, name: "4K Movies", type: "movies" },
    ],
  }),
}));

installV2Recorder();

function problem(status: number, type: string) {
  return async () => {
    const { V2ProblemError } =
      await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
    throw new V2ProblemError("op", {
      type: `https://siloserver.org/docs/api/v2/problems/${type}`,
      title: type,
      status,
    } as never);
  };
}

const GHIBLI = { ...adminCollection, library_ids: ["1", "2"] };

function section(id: string, overrides: Record<string, unknown>) {
  return {
    id,
    scope: "home",
    library_id: null,
    section_type: "collection",
    title: id,
    featured: false,
    enabled: true,
    position: 0,
    page_row_count: 1,
    config: { library_collection_id: "c1" },
    ...overrides,
  };
}

const HOME_ROW = section("s-home", { title: "Studio Ghibli", position: 5, page_row_count: 9 });
const KIDS_ROW = section("s-kids", {
  scope: "library",
  library_id: "2",
  title: "Ghibli Favorites",
  position: 2,
  page_row_count: 7,
});

let rows: Array<ReturnType<typeof section>>;

/** Each page's rows in order: `count` rows, with `rows` at their stored positions. */
function pageOrder(call: RecordedCall) {
  const libraryId = call.query?.library_id ?? null;
  const count = libraryId === null ? 9 : 7;
  const ids = Array.from({ length: count }, (_, index) => `other-${index}`);
  for (const row of rows) {
    if (row.library_id === libraryId) ids[row.position] = row.id;
  }
  return { ordered_ids: ids };
}

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  URL.createObjectURL = () => "blob:artwork";
  HTMLElement.prototype.scrollIntoView = () => {};
  HTMLElement.prototype.hasPointerCapture = () => false;
  rows = [HOME_ROW, KIDS_ROW];
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", {
    ...adminCapabilities,
    section_references: true,
  });
  v2Recorder.answer("GET /api/v2/collections/capabilities", personalCapabilities);
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", GHIBLI);
  v2Recorder.answer("PATCH /api/v2/admin/collections/{id}", (call: RecordedCall) => ({
    ...GHIBLI,
    ...(call.body as object),
  }));
  v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(GHIBLI));
  v2Recorder.answer("GET /api/v2/admin/collections/{id}/items", {
    items: [],
    page: { has_more: false },
  });
  v2Recorder.answer("GET /api/v2/admin/collections/{id}/items/order", {
    ordered_ids: [],
    has_more: false,
  });
  v2Recorder.answer("POST /api/v2/catalog/query", {
    items: [],
    page: { has_more: false },
    total: 0,
    total_exact: true,
    effective_sort: { field: "relevance", order: "desc" },
  });
  v2Recorder.answer("GET /api/v2/admin/collections/{id}/sections", () => ({ items: rows }));
  v2Recorder.answer("GET /api/v2/admin/sections/order", pageOrder);
  v2Recorder.answer("GET /api/v2/admin/sections/{id}", (call: RecordedCall) =>
    rows.find((row) => call.path.endsWith(`/${row.id}`)),
  );
  v2Recorder.answer("DELETE /api/v2/admin/sections/{id}", (call: RecordedCall) => {
    rows = rows.filter((row) => !call.path.endsWith(`/${row.id}`));
  });
  v2Recorder.answer("DELETE /api/v2/admin/collections/{id}", undefined);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function Where() {
  const location = useLocation();
  return <output aria-label="Location">{`${location.pathname}${location.search}`}</output>;
}

function showPage(entry: string | { pathname: string; search: string; state: unknown }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const router = createMemoryRouter(
    [
      { path: "/admin/collections", element: <Where /> },
      { path: "/admin/sections", element: <Where /> },
      {
        element: <CollectionEditorPage scope="server" />,
        children: [{ path: "/admin/collections/new" }, { path: "/admin/collections/:id/edit" }],
      },
    ],
    { initialEntries: [entry] },
  );
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const EDITOR = "/admin/collections/c1/edit?libraryId=1";
const rowsGroup = () => screen.findByRole("group", { name: "Rows that show it" });
const location = () => screen.getByRole("status", { name: "Location" }).textContent ?? "";
const homeRowsParams = () => new URL(location(), "https://silo.test").searchParams;

async function openAddAsRow(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: /Add as a row/ }));
  return screen.findByRole("menu");
}

describe("rows that show a server collection", () => {
  it("lists each row with its page and place, opening it in Home rows", async () => {
    showPage(EDITOR);
    const group = await rowsGroup();
    const home = await within(group).findByRole("link", {
      name: "Studio Ghibli, Home · row 6 of 9",
    });
    expect(home).toHaveAttribute("href", "/admin/sections?page=home&edit=s-home");
    expect(
      within(group).getByRole("link", { name: "Ghibli Favorites, Kids page · row 3 of 7" }),
    ).toHaveAttribute("href", "/admin/sections?page=2&edit=s-kids");
    expect(group).toHaveTextContent("2 rows");
    expect(group).toHaveTextContent(ROWS_NOT_LISTED);
  });

  it("numbers each row by its place in the page order, not its stored position", async () => {
    // Positions skip numbers after a row is deleted: s-home is stored at 7 but is 2nd of 4.
    rows = [{ ...HOME_ROW, position: 7, page_row_count: 4 }];
    v2Recorder.answer("GET /api/v2/admin/sections/order", {
      ordered_ids: ["s-top", "s-home", "s-next", "s-last"],
    });
    showPage(EDITOR);
    const group = await rowsGroup();
    expect(
      await within(group).findByRole("link", { name: "Studio Ghibli, Home · row 2 of 4" }),
    ).toBeInTheDocument();
    expect(
      v2Recorder.callsOf("GET /api/v2/admin/sections/order").map((call) => call.query?.scope),
    ).toEqual(["home"]);
  });

  it("says in the header where rows show it", async () => {
    showPage(EDITOR);
    expect(await screen.findByText(/On Home and the Kids page/)).toBeInTheDocument();
  });

  it("highlights the row just added when Home rows hands back to the editor", async () => {
    const state: AddedRowState = {
      addedRow: {
        id: "s-kids",
        surface: "admin",
        page: { kind: "library", libraryId: 2 },
        position: 3,
      },
    };
    showPage({ pathname: "/admin/collections/c1/edit", search: "?libraryId=1", state });
    const group = await rowsGroup();
    const added = await within(group).findByRole("link", { name: /^Ghibli Favorites/ });
    await waitFor(() => expect(added.closest("li")).toHaveAttribute("data-highlighted", "true"));
    expect(
      within(group)
        .getByRole("link", { name: /^Studio Ghibli/ })
        .closest("li"),
    ).not.toHaveAttribute("data-highlighted");
  });

  it("offers Retry when the rows don't load, and Add as a row still works", async () => {
    let fail = true;
    v2Recorder.answer("GET /api/v2/admin/collections/{id}/sections", () => {
      if (fail) throw new Error("offline");
      return { items: rows };
    });
    const user = userEvent.setup();
    showPage(EDITOR);
    const group = await rowsGroup();
    expect(await within(group).findByRole("alert")).toHaveTextContent(
      "Couldn't load the rows that show it.",
    );
    fail = false;
    await user.click(within(group).getByRole("button", { name: "Retry" }));
    expect(await within(group).findByRole("link", { name: /^Studio Ghibli/ })).toBeInTheDocument();
  });

  it("keeps Add as a row but lists no rows when the server doesn't report them", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/capabilities", adminCapabilities);
    const user = userEvent.setup();
    showPage(EDITOR);
    const menu = await openAddAsRow(user);
    expect(within(menu).getByRole("menuitem", { name: "Home" })).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Rows that show it" })).toBeNull();
    expect(v2Recorder.callsOf("GET /api/v2/admin/collections/{id}/sections")).toHaveLength(0);
  });
});

describe("Add as a row", () => {
  it("offers Home, the collection's library pages, then the other libraries", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    const menu = await openAddAsRow(user);
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((item) => item.textContent),
    ).toEqual(["Home", "Movies page", "Kids page", "4K Movies page"]);
    expect(menu).toHaveTextContent("Other libraries");
  });

  it("opens Add row on the chosen page, with a way back to the editor", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    const menu = await openAddAsRow(user);
    await user.click(within(menu).getByRole("menuitem", { name: "Kids page" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/sections\?/));
    const params = homeRowsParams();
    expect(params.get("page")).toBe("2");
    expect(params.get("add")).toBe("collection:library:c1");
    expect(params.get("return")).toBe(EDITOR);
    expect(safeReturnPath(params.get("return"))).toBe(EDITOR);
  });

  it("waits for a new collection to be created, and says so in one line", async () => {
    showPage("/admin/collections/new?type=manual&libraryId=1");
    const group = await rowsGroup();
    expect(group).toHaveTextContent(ROWS_ONCE_CREATED);
    expect(within(group).queryByRole("button")).toBeNull();
    expect(screen.queryByRole("button", { name: /Add as a row/ })).toBeNull();
  });

  it("waits for a hidden collection to be shown on its Collections tab, and says so", async () => {
    rows = [];
    showPage(EDITOR);
    await screen.findByText("No Home or library page row shows it yet.");
    fireEvent.click(await screen.findByRole("switch", { name: SHOW_ON_TAB_LABEL }));
    const button = screen.getByRole("button", { name: /Add as a row/ });
    expect(button).toBeDisabled();
    expect(button).toHaveAccessibleDescription(SHOW_IT_FIRST);
  });

  it("asks first about unsaved changes: Cancel stays, sending nothing", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    fireEvent.change(await screen.findByRole("textbox", { name: "Name" }), {
      target: { value: "Studio Ghibli Films" },
    });
    const menu = await openAddAsRow(user);
    await user.click(within(menu).getByRole("menuitem", { name: "Home" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Save changes first?" });
    expect(dialog).toHaveTextContent(
      "You're about to add Original as a row on Home. Name not saved yet. Titles are already saved.",
    );
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("Studio Ghibli Films");
    expect(v2Recorder.writes()).toHaveLength(0);
  });

  it("asks first about unsaved changes: Discard changes goes on without saving", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    fireEvent.change(await screen.findByRole("textbox", { name: "Name" }), {
      target: { value: "Studio Ghibli Films" },
    });
    const menu = await openAddAsRow(user);
    await user.click(within(menu).getByRole("menuitem", { name: "Kids page" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Save changes first?" });
    expect(dialog).toHaveTextContent("as a row on the Kids page.");
    await user.click(within(dialog).getByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/sections\?/));
    expect(homeRowsParams().get("page")).toBe("2");
    expect(v2Recorder.writes()).toHaveLength(0);
  });

  it("takes the list it was opened from along, so it can go back there afterwards", async () => {
    const returnTo = "/admin/collections?view=list&q=ghibli";
    const user = userEvent.setup();
    const router = showPage({
      pathname: "/admin/collections/c1/edit",
      search: "?libraryId=1",
      state: { returnTo },
    });
    const menu = await openAddAsRow(user);
    await user.click(within(menu).getByRole("menuitem", { name: "Home" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/sections\?/));
    expect(router.state.location.state).toEqual({ returnTo });
  });

  it("can't discard into Home rows when it's saved as hidden: only saving shows it", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", { ...GHIBLI, visibility: "hidden" });
    rows = [];
    const user = userEvent.setup();
    showPage(EDITOR);
    await screen.findByText("No Home or library page row shows it yet.");
    fireEvent.click(await screen.findByRole("switch", { name: SHOW_ON_TAB_LABEL }));
    const menu = await openAddAsRow(user);
    await user.click(within(menu).getByRole("menuitem", { name: "Home" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Save changes first?" });
    const discard = within(dialog).getByRole("button", { name: "Discard changes" });
    expect(discard).toBeDisabled();
    expect(discard).toHaveAccessibleDescription(DISCARD_KEEPS_IT_HIDDEN);
    await user.click(within(dialog).getByRole("button", { name: "Save and continue" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/sections\?/));
    const [patch] = v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}");
    expect(patch!.body).toMatchObject({ visibility: "visible" });
  });

  it("stays when Save and continue couldn't save the poster, showing why", async () => {
    v2Recorder.answer("PUT /api/v2/admin/collections/{id}/poster", () => {
      throw new Error("too large");
    });
    const user = userEvent.setup();
    showPage(EDITOR);
    await rowsGroup();
    await uploadArtwork("poster");
    const menu = await openAddAsRow(user);
    await user.click(within(menu).getByRole("menuitem", { name: "Home" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Save changes first?" });
    await user.click(within(dialog).getByRole("button", { name: "Save and continue" }));
    expect(await screen.findByText(/Couldn't save the poster/)).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(v2Recorder.callsOf("PUT /api/v2/admin/collections/{id}/poster")).toHaveLength(1);
    expect(screen.queryByRole("status", { name: "Location" })).toBeNull();
  });

  it("won't Save and continue while a field changed in both places waits for a choice", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    await rowsGroup();
    fireEvent.change(await screen.findByRole("textbox", { name: "Name" }), {
      target: { value: "Mine" },
    });
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", { ...GHIBLI, title: "Theirs" });
    v2Recorder.bump("/api/v2/admin/collections/c1");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("This collection changed since you opened it.");
    const menu = await openAddAsRow(user);
    await user.click(within(menu).getByRole("menuitem", { name: "Home" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Save changes first?" });
    const saveAndGo = within(dialog).getByRole("button", { name: "Save and continue" });
    expect(saveAndGo).toBeDisabled();
    expect(saveAndGo).toHaveAccessibleDescription(SAVE_AFTER_CONFLICTS);
    expect(v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}")).toHaveLength(1);
  });

  it("asks first about unsaved changes: Save and continue saves, then goes on", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    fireEvent.change(await screen.findByRole("textbox", { name: "Name" }), {
      target: { value: "Studio Ghibli Films" },
    });
    const menu = await openAddAsRow(user);
    await user.click(within(menu).getByRole("menuitem", { name: "Home" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Save changes first?" });
    await user.click(within(dialog).getByRole("button", { name: "Save and continue" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/sections\?/));
    const [patch] = v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}");
    expect(patch!.body).toMatchObject({ title: "Studio Ghibli Films" });
    expect(homeRowsParams().get("add")).toBe("collection:library:c1");
  });
});

describe("hiding a collection rows show", () => {
  it("asks first and names the rows' pages; Cancel keeps it on the tab", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    await screen.findByText(/On Home and the Kids page/);
    const toggle = screen.getByRole("switch", { name: SHOW_ON_TAB_LABEL });
    await user.click(toggle);
    const dialog = await screen.findByRole("alertdialog", {
      name: "Hide Original from Collections tabs?",
    });
    expect(dialog).toHaveTextContent(
      "It leaves Movies › Collections and Kids › Collections. 2 rows on Home and the Kids page still show it, but their See all won't open while it's hidden.",
    );
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(toggle).toBeChecked();
    await user.click(toggle);
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Hide" }),
    );
    expect(toggle).not.toBeChecked();
    // The switch is part of the draft: nothing saves until Save.
    expect(v2Recorder.writes()).toHaveLength(0);
  });

  it("still asks from the list's row count while the rows don't load", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}/sections", () => {
      throw new Error("offline");
    });
    v2Recorder.answer(
      "GET /api/v2/admin/collections",
      adminCollectionList({ ...GHIBLI, row_count: 2 }),
    );
    const user = userEvent.setup();
    showPage(EDITOR);
    await within(await rowsGroup()).findByRole("alert");
    await user.click(screen.getByRole("switch", { name: SHOW_ON_TAB_LABEL }));
    const dialog = await screen.findByRole("alertdialog", {
      name: "Hide Original from Collections tabs?",
    });
    expect(dialog).toHaveTextContent(
      "2 rows still show it, but their See all won't open while it's hidden.",
    );
  });

  it("hides at once when no row shows it", async () => {
    rows = [];
    showPage(EDITOR);
    await screen.findByText("No Home or library page row shows it yet.");
    const toggle = screen.getByRole("switch", { name: SHOW_ON_TAB_LABEL });
    fireEvent.click(toggle);
    expect(toggle).not.toBeChecked();
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});

describe("deleting a collection rows show", () => {
  async function openDelete(user: ReturnType<typeof userEvent.setup>) {
    await screen.findByText(/On Home and the Kids page/);
    await user.click(screen.getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete…" }));
    return screen.findByRole("alertdialog", { name: 'Delete "Original"?' });
  }

  it("lists the rows, each with Open row", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    const dialog = await openDelete(user);
    expect(dialog).toHaveTextContent(
      "It's removed from Movies and Kids for everyone. 2 rows show it:",
    );
    expect(within(dialog).getByRole("link", { name: "Open row: Studio Ghibli" })).toHaveAttribute(
      "href",
      "/admin/sections?page=home&edit=s-home",
    );
    expect(
      within(dialog).getByRole("link", { name: "Open row: Ghibli Favorites" }),
    ).toHaveAttribute("href", "/admin/sections?page=2&edit=s-kids");
  });

  it("deletes each row with a fresh token, then the collection", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    const dialog = await openDelete(user);
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/collections(\?|$)/));
    const sequence = v2Recorder.calls
      .filter((call) =>
        /^(GET|DELETE) \/api\/v2\/admin\/(sections\/\{id\}|collections\/\{id\})$/.test(
          call.operation,
        ),
      )
      .filter((call) => !(call.operation.startsWith("GET") && call.path.includes("/collections/")))
      .map((call) => `${call.operation.split(" ")[0]} ${call.path}`);
    expect(sequence).toEqual([
      "GET /api/v2/admin/sections/s-home",
      "DELETE /api/v2/admin/sections/s-home",
      "GET /api/v2/admin/sections/s-kids",
      "DELETE /api/v2/admin/sections/s-kids",
      "DELETE /api/v2/admin/collections/c1",
    ]);
    const rowDeletes = v2Recorder.callsOf("DELETE /api/v2/admin/sections/{id}");
    expect(rowDeletes[0]!.headers["If-Match"]).toBe('"/api/v2/admin/sections/s-home#1"');
  });

  it("leaves a row that now shows another collection, and still deletes the collection", async () => {
    v2Recorder.answer("GET /api/v2/admin/sections/{id}", (call: RecordedCall) => {
      const row = rows.find((candidate) => call.path.endsWith(`/${candidate.id}`))!;
      return row.id === "s-kids" ? { ...row, config: { library_collection_id: "c2" } } : row;
    });
    const user = userEvent.setup();
    showPage(EDITOR);
    const dialog = await openDelete(user);
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/collections(\?|$)/));
    expect(
      v2Recorder.callsOf("DELETE /api/v2/admin/sections/{id}").map((call) => call.path),
    ).toEqual(["/api/v2/admin/sections/s-home"]);
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(1);
  });

  it("counts a row that's already gone as done, and still deletes the collection", async () => {
    v2Recorder.answer("GET /api/v2/admin/sections/{id}", async (call: RecordedCall) => {
      if (call.path.endsWith("/s-home")) await problem(404, "not_found")();
      return rows.find((row) => call.path.endsWith(`/${row.id}`));
    });
    const user = userEvent.setup();
    showPage(EDITOR);
    const dialog = await openDelete(user);
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/collections(\?|$)/));
    expect(
      v2Recorder.callsOf("DELETE /api/v2/admin/sections/{id}").map((call) => call.path),
    ).toEqual(["/api/v2/admin/sections/s-kids"]);
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(1);
  });

  it("still deletes when the rows don't load and the list counts none", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}/sections", () => {
      throw new Error("offline");
    });
    const user = userEvent.setup();
    showPage(EDITOR);
    await within(await rowsGroup()).findByRole("alert");
    await user.click(screen.getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: 'Delete "Original"?' });
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/collections(\?|$)/));
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(1);
  });

  it("waits for the rows when they don't load but the list counts some", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}/sections", () => {
      throw new Error("offline");
    });
    v2Recorder.answer(
      "GET /api/v2/admin/collections",
      adminCollectionList({ ...GHIBLI, row_count: 2 }),
    );
    const user = userEvent.setup();
    showPage(EDITOR);
    await within(await rowsGroup()).findByRole("alert");
    await user.click(screen.getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: 'Delete "Original"?' });
    expect(within(dialog).getByRole("button", { name: "Retry" })).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Delete" })).toBeDisabled();
  });

  it("deletes no row when the collection changed since the editor read it", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    const dialog = await openDelete(user);
    v2Recorder.bump("/api/v2/admin/collections/c1");
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(CHANGED_BEFORE_DELETE);
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/sections/{id}")).toHaveLength(0);
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(0);
    // The editor read it again, so the next Delete sends the new token.
    const changed = v2Recorder.etag("/api/v2/admin/collections/c1");
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/collections(\?|$)/));
    const [remove] = v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}");
    expect(remove!.headers["If-Match"]).toBe(changed);
  });

  it("stops before the collection when a row can't be deleted, and names what's left", async () => {
    v2Recorder.answer("DELETE /api/v2/admin/sections/{id}", (call: RecordedCall) => {
      if (call.path.endsWith("/s-kids")) throw new Error("Server error");
      rows = rows.filter((row) => !call.path.endsWith(`/${row.id}`));
    });
    const user = userEvent.setup();
    showPage(EDITOR);
    const dialog = await openDelete(user);
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "Couldn't delete every row, so the collection was kept. Still showing it: Ghibli Favorites (Kids page).",
    );
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(0);
    expect(
      await within(dialog).findByRole("button", { name: "Delete it and its 1 row" }),
    ).toBeEnabled();
  });
});
