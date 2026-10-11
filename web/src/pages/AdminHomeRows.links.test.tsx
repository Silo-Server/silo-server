import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import golden from "@/lib/homeRows/payloads.golden.json";
import { recipeCatalogFixture } from "@/lib/homeRows/recipeCatalogFixture.test-support";
import { fetchAdminCollections } from "@/api/adminCollections";
import { adminKeys } from "@/hooks/queries/keys";
import { SERVER_SCOPE } from "@/lib/collections/scope";
import { adminCollection, adminCollectionList } from "@/test/fixtures/collectionAnswers";
import AdminHomeRows from "./AdminHomeRows";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({
  toast: { success: mocks.success, error: mocks.error, warning: vi.fn() },
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [{ id: 7, name: "Movies", type: "movies" }] }),
}));
vi.mock("@/hooks/queries/useAllUserCollections", () => ({
  useAllUserCollections: () => ({ collections: [], isLoading: false }),
}));
vi.mock("@/lib/recipes", async () => ({
  ...(await vi.importActual<typeof import("@/lib/recipes")>("@/lib/recipes")),
  fetchRecipeCatalog: async () => recipeCatalogFixture,
}));
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

type Row = Record<string, unknown> & { id: string; scope: string; position: number };
type Args = {
  query?: { scope?: string; library_id?: string };
  path?: { id: string };
  body?: Record<string, unknown>;
  onResponse?: (response: Response) => void;
};

function stored(id: string, overrides: Partial<Row> = {}): Row {
  return {
    id,
    title: `Row ${id}`,
    scope: "home",
    library_id: null,
    position: 0,
    section_type: "recently_added",
    item_limit: 20,
    featured: false,
    enabled: true,
    config: {},
    created_at: "2026-09-05T00:00:00Z",
    updated_at: "2026-09-05T00:00:00Z",
    ...overrides,
  };
}

function serverCollection(id: string, title: string, visibility = "visible") {
  return { ...adminCollection, id, title, library_id: "7", library_ids: ["7"], visibility };
}

let rows: Row[];
let collections: unknown[];
let creates: Args[];
/** Requests that wait for the test to answer them, by operation. */
let held: Record<string, Promise<void>>;
let failing: Set<string>;

function deferred() {
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

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
  rows = [stored("a", { position: 0 }), stored("b", { position: 4, title: "Trending" })];
  collections = [
    serverCollection("lib-1", "Studio Ghibli"),
    serverCollection("secret", "Staff only", "hidden"),
  ];
  creates = [];
  held = {};
  failing = new Set();
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    await held[operation];
    if (failing.has(operation)) throw new Error(`${operation} failed`);
    args.onResponse?.(new Response(null, { headers: { ETag: '"rev-1"' } }));
    const scope = args.query?.scope ?? "home";
    const here = rows.filter((row) => row.scope === scope);
    switch (operation) {
      case "GET /api/v2/admin/collections":
        return adminCollectionList(...collections);
      case "GET /api/v2/admin/sections/capabilities":
        return { available: true, reset_profiles: false, preview: false };
      case "GET /api/v2/admin/sections/order":
        return { scope, library_id: null, ordered_ids: here.map((row) => row.id) };
      case "GET /api/v2/admin/sections":
        return { items: here.map((row) => ({ ...row })) };
      case "GET /api/v2/admin/sections/{id}":
        return { ...rows.find((row) => row.id === args.path?.id)! };
      case "POST /api/v2/admin/sections": {
        creates.push(args);
        const created = stored(`new-${creates.length}`, args.body as Partial<Row>);
        rows = [...rows, created];
        return created;
      }
    }
    throw new Error(`Unexpected ${operation}`);
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function newClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
}

function setup(
  entry: string | { pathname: string; search: string; state: unknown },
  client = newClient(),
) {
  const router = createMemoryRouter(
    [
      { path: "/admin/sections", element: <AdminHomeRows /> },
      { path: "/admin/collections/:id/edit", element: <h1>Collection editor</h1> },
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

const searchOf = (router: ReturnType<typeof setup>) => router.state.location.search;

describe("?add= on admin Home rows", () => {
  it("opens Add row on the collection and adds it with the gallery's body", async () => {
    const router = setup("/admin/sections?add=collection:library:lib-1");
    const form = await screen.findByRole("dialog", { name: "A collection" });
    expect(within(form).getByRole("radio", { name: "Studio Ghibli" })).toBeChecked();
    expect(within(form).getByLabelText("Row name")).toHaveValue("Studio Ghibli");
    // Read once: a reload doesn't open it again.
    expect(searchOf(router)).toBe("");
    // With no page to return to, the back link is the picker's.
    expect(within(form).getByRole("button", { name: "All rows" })).toBeInTheDocument();

    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(creates).toHaveLength(1));
    expect(creates[0]!.body).toEqual({
      ...(golden.adminCreate as Record<string, object>)["collection/picked/home"],
      position: 5,
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(router.state.location.pathname).toBe("/admin/sections");
  });

  it("keeps the page it opens on and adds the row there", async () => {
    rows.push(stored("m", { scope: "library", library_id: "7" }));
    const router = setup("/admin/sections?page=7&add=collection:library:lib-1");
    const form = await screen.findByRole("dialog", { name: "A collection" });
    expect(searchOf(router)).toBe("?page=7");
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(creates).toHaveLength(1));
    expect(creates[0]!.body).toMatchObject({
      scope: "library",
      library_id: "7",
      config: { library_collection_id: "lib-1" },
    });
  });

  it.each([
    ["an unknown", "collection:library:nope"],
    ["a hidden", "collection:library:secret"],
    ["a personal", "collection:user:mine"],
    ["a malformed", "trending_on_server"],
  ])("says %s collection can't be added and opens nothing", async (_name, value) => {
    const router = setup(`/admin/sections?add=${value}`);
    await waitFor(() =>
      expect(mocks.error).toHaveBeenCalledWith("This collection can't be added here."),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(searchOf(router)).toBe("");
  });

  it("finds a collection made in the editor once its options refresh", async () => {
    const client = newClient();
    // The refresh answers only after the rest of the page is ready.
    const refresh = deferred();
    // The Home rows options were read before the collection existed.
    await client.fetchQuery({
      queryKey: adminKeys.collections(undefined),
      queryFn: () => fetchAdminCollections(undefined),
    });
    collections = [...collections, serverCollection("fresh", "Made just now")];
    // What the editor runs after saving it.
    held["GET /api/v2/admin/collections"] = refresh.promise;
    await SERVER_SCOPE.invalidate(client);

    setup("/admin/sections?add=collection:library:fresh", client);
    // The rows and capabilities are in: Add row is ready.
    const add = await screen.findByRole("button", { name: "Add row" });
    await waitFor(() => expect(add).toBeEnabled());
    await act(async () => {});
    expect(mocks.error).not.toHaveBeenCalled();

    refresh.resolve();
    const form = await screen.findByRole("dialog", { name: "A collection" });
    expect(within(form).getByRole("radio", { name: "Made just now" })).toBeChecked();
    expect(mocks.error).not.toHaveBeenCalled();
  });

  it("says a collection deleted since its options were read can't be added", async () => {
    const client = newClient();
    const refresh = deferred();
    collections = [...collections, serverCollection("doomed", "Deleted just now")];
    // The Home rows options were read while the collection still existed.
    await client.fetchQuery({
      queryKey: adminKeys.collections(undefined),
      queryFn: () => fetchAdminCollections(undefined),
    });
    collections = collections.filter((c) => (c as { id: string }).id !== "doomed");
    // What deleting it runs; the refresh answers after the page is ready.
    held["GET /api/v2/admin/collections"] = refresh.promise;
    await SERVER_SCOPE.invalidate(client);

    setup("/admin/sections?add=collection:library:doomed", client);
    const add = await screen.findByRole("button", { name: "Add row" });
    await waitFor(() => expect(add).toBeEnabled());
    await act(async () => {});
    // The stale list still holds it: nothing opens until the refresh answers.
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    refresh.resolve();
    await waitFor(() =>
      expect(mocks.error).toHaveBeenCalledWith("This collection can't be added here."),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("doesn't open on a cached collection when its refresh fails", async () => {
    const client = newClient();
    collections = [...collections, serverCollection("doomed", "Hidden since")];
    // The Home rows options were read while the collection was still visible.
    await client.fetchQuery({
      queryKey: adminKeys.collections(undefined),
      queryFn: () => fetchAdminCollections(undefined),
    });
    // It was hidden since, and the refresh after that fails.
    collections = [
      ...collections.slice(0, -1),
      serverCollection("doomed", "Hidden since", "hidden"),
    ];
    failing.add("GET /api/v2/admin/collections");
    await SERVER_SCOPE.invalidate(client);

    setup("/admin/sections?add=collection:library:doomed", client);
    await waitFor(() =>
      expect(mocks.error).toHaveBeenCalledWith(
        "Collections didn't load, so Add row couldn't open.",
      ),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("says the collections didn't load rather than that this one can't be added", async () => {
    failing.add("GET /api/v2/admin/collections");
    setup("/admin/sections?add=collection:library:lib-1");
    await waitFor(() =>
      expect(mocks.error).toHaveBeenCalledWith(
        "Collections didn't load, so Add row couldn't open.",
      ),
    );
    expect(mocks.error).not.toHaveBeenCalledWith("This collection can't be added here.");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("says the page can't change when its capabilities don't load", async () => {
    failing.add("GET /api/v2/admin/sections/capabilities");
    setup("/admin/sections?add=collection:library:lib-1");
    await waitFor(() =>
      expect(mocks.error).toHaveBeenCalledWith("This page can't change right now."),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("?return= on admin Home rows", () => {
  const link = (returnTo: string) =>
    `/admin/sections?add=collection:library:lib-1&return=${encodeURIComponent(returnTo)}`;

  it("goes back to the collection after adding, with a toast that can move the row", async () => {
    const router = setup(link("/admin/collections/lib-1/edit"));
    const form = await screen.findByRole("dialog", { name: "A collection" });
    expect(within(form).getByRole("button", { name: "Back to Studio Ghibli" })).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));

    expect(await screen.findByRole("heading", { name: "Collection editor" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/collections/lib-1/edit");
    // Home rows was a detour: Back from the collection doesn't return to it.
    expect(router.state.historyAction).toBe("REPLACE");
    // The collection's page is told which row is new, to show it.
    expect(router.state.location.state).toEqual({
      addedRow: {
        id: "new-1",
        surface: "admin",
        page: { kind: "home" },
        position: 3,
      },
    });
    expect(mocks.success).toHaveBeenCalledWith(
      "Added to Home as row 3 of 3",
      expect.objectContaining({ action: expect.objectContaining({ label: "Move it" }) }),
    );

    // Move it opens the new row on Home rows.
    const [, options] = mocks.success.mock.calls[0]!;
    await act(async () => {
      await options.action.onClick();
    });
    expect(await screen.findByRole("dialog", { name: "Edit row" })).toBeInTheDocument();
    expect(screen.getByLabelText("Row name")).toHaveValue("Studio Ghibli");
    expect(searchOf(router)).toBe("?page=home");
  });

  it("hands back the history state the link arrived with, after adding or not", async () => {
    const arrived = { returnTo: "/admin/collections?view=list&q=ghibli" };
    const entry = (): { pathname: string; search: string; state: unknown } => {
      const url = new URL(link("/admin/collections/lib-1/edit"), "https://silo.test");
      return { pathname: url.pathname, search: url.search, state: arrived };
    };
    const added = setup(entry());
    let form = await screen.findByRole("dialog", { name: "A collection" });
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await screen.findByRole("heading", { name: "Collection editor" });
    expect(added.state.location.state).toEqual({
      ...arrived,
      addedRow: expect.objectContaining({ id: "new-1" }),
    });
    cleanup();

    const back = setup(entry());
    form = await screen.findByRole("dialog", { name: "A collection" });
    await userEvent.click(within(form).getByRole("button", { name: "Back to Studio Ghibli" }));
    await screen.findByRole("heading", { name: "Collection editor" });
    expect(back.state.location.state).toEqual(arrived);
  });

  it("counts rows another admin added while Add row was open", async () => {
    const client = newClient();
    setup(link("/admin/collections/lib-1/edit"), client);
    const form = await screen.findByRole("dialog", { name: "A collection" });
    rows = [...rows, stored("c", { position: 6, title: "Someone else's" })];
    await act(() => client.invalidateQueries());
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));

    expect(await screen.findByRole("heading", { name: "Collection editor" })).toBeInTheDocument();
    expect(mocks.success).toHaveBeenCalledWith("Added to Home as row 4 of 4", expect.anything());
  });

  it("follows a new link while Add row is open", async () => {
    const router = setup(link("/admin/collections/lib-1/edit"));
    await screen.findByRole("dialog", { name: "A collection" });
    await act(() => router.navigate("/admin/sections?edit=a"));
    const dialog = await screen.findByRole("dialog", { name: "Edit row" });
    expect(within(dialog).getByLabelText("Row name")).toHaveValue("Row a");
  });

  it("goes back to the collection without adding from the back link", async () => {
    const router = setup(link("/admin/collections/lib-1/edit"));
    const form = await screen.findByRole("dialog", { name: "A collection" });
    await userEvent.click(within(form).getByRole("button", { name: "Back to Studio Ghibli" }));
    expect(await screen.findByRole("heading", { name: "Collection editor" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/collections/lib-1/edit");
    expect(router.state.historyAction).toBe("REPLACE");
    expect(router.state.location.state).toBeNull();
    expect(creates).toEqual([]);
  });

  it.each([
    "//evil.example",
    "https://evil.example/admin/collections/x",
    "/\\evil.example",
    "/%5Cevil.example",
    "/admin/collections/%09x",
    "/settings",
  ])("ignores return=%s and stays on Home rows", async (returnTo) => {
    const router = setup(link(returnTo));
    const form = await screen.findByRole("dialog", { name: "A collection" });
    expect(within(form).getByRole("button", { name: "All rows" })).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(creates).toHaveLength(1));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(router.state.location.pathname).toBe("/admin/sections");
    expect(mocks.success).not.toHaveBeenCalled();
  });
});

describe("?edit= on admin Home rows", () => {
  it("opens the row in Edit row", async () => {
    const router = setup("/admin/sections?edit=b");
    const dialog = await screen.findByRole("dialog", { name: "Edit row" });
    expect(within(dialog).getByLabelText("Row name")).toHaveValue("Trending");
    expect(searchOf(router)).toBe("");
  });

  it("opens the linked row once a row the page was already reading is done", async () => {
    const router = setup("/admin/sections");
    const read = deferred();
    held["GET /api/v2/admin/sections/{id}"] = read.promise;
    await userEvent.click(await screen.findByRole("button", { name: "More for Row a" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Edit row…" }));
    await act(async () => {
      await router.navigate("/admin/sections?edit=b");
    });
    expect(searchOf(router)).toBe("");

    // That read fails, so nothing else is open when the link's row is ready.
    delete held["GET /api/v2/admin/sections/{id}"];
    await act(async () => read.reject(new Error("Could not read Row a")));
    const dialog = await screen.findByRole("dialog", { name: "Edit row" });
    expect(within(dialog).getByLabelText("Row name")).toHaveValue("Trending");
  });

  it("says a row that is gone no longer exists and opens nothing", async () => {
    setup("/admin/sections?edit=gone");
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("That row no longer exists."));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
