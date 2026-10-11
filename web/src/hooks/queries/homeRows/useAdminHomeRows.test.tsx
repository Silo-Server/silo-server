import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { MemoryRouter, useSearchParams } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { v2Problem } from "@/api/v2/problems.test-support";
import { useDeleteSection } from "@/hooks/queries/sections";
import { RowChangedError } from "@/lib/homeRows/types";
import { useAdminHomeRows } from "./useAdminHomeRows";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  error: vi.fn(),
  libraries: vi.fn(),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: mocks.error, warning: vi.fn() } }));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => mocks.libraries(),
}));
vi.mock("@/hooks/queries/collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: vi.fn(),
}));

type Row = {
  id: string;
  title: string;
  scope: string;
  library_id: string | null;
  position: number;
  section_type: string;
  item_limit: number;
  featured: boolean;
  enabled: boolean;
  config: Record<string, unknown>;
  created_at: string;
  updated_at: string;
};
type Args = {
  query?: { scope?: string; library_id?: string };
  path?: { id: string };
  headers?: Record<string, string>;
  body?: Record<string, unknown>;
  onResponse?: (response: Response) => void;
};

function row(id: string, overrides: Partial<Row> = {}): Row {
  return {
    id,
    title: `Title ${id}`,
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

let rows: Row[];
let libraryRows: Row[];
let revision: number;
let calls: Array<{ operation: string; args: Args }>;
/** Lets a test hold one operation open until it resolves it. */
let hold: { operation: string; release?: () => void } | null;

beforeEach(() => {
  vi.clearAllMocks();
  rows = [row("a"), row("b")];
  libraryRows = [row("lib", { scope: "library", library_id: "7", title: "Library row" })];
  revision = 1;
  calls = [];
  hold = null;
  mocks.libraries.mockReturnValue({ data: [{ id: 7, name: "Movies", type: "movies" }] });
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    calls.push({ operation, args });
    if (hold && hold.operation === operation) {
      await new Promise<void>((resolve) => {
        hold!.release = resolve;
      });
    }
    args.onResponse?.(new Response(null, { headers: { ETag: `"rev-${revision}"` } }));
    const scopeRows =
      args.query?.scope === "library"
        ? libraryRows.filter((entry) => entry.library_id === args.query?.library_id)
        : rows;
    if (operation === "GET /api/v2/admin/sections/capabilities")
      return { available: true, reset_profiles: false, preview: true };
    if (operation === "GET /api/v2/admin/sections/order")
      return { scope: "home", library_id: null, ordered_ids: scopeRows.map((entry) => entry.id) };
    if (operation === "GET /api/v2/admin/sections")
      return { items: scopeRows.map((entry) => ({ ...entry })) };
    if (operation === "GET /api/v2/admin/sections/{id}")
      return { ...[...rows, ...libraryRows].find((entry) => entry.id === args.path?.id)! };
    if (operation === "POST /api/v2/admin/sections/bulk") {
      // Unguarded: one new row at the bottom of each library page named.
      const { library_ids: libraryIds, ...fields } = args.body as Record<string, unknown>;
      for (const libraryId of libraryIds as string[]) {
        revision++;
        libraryRows.push(
          row(`copy-${revision}`, {
            ...(fields as Partial<Row>),
            scope: "library",
            library_id: libraryId,
          }),
        );
      }
      return { created: (libraryIds as string[]).length };
    }
    if (args.headers?.["If-Match"] !== `"rev-${revision}"`)
      throw v2Problem(412, "precondition_failed", "Changed on another client");
    if (operation === "PATCH /api/v2/admin/sections/{id}") {
      revision++;
      const changes = Object.fromEntries(
        Object.entries(args.body ?? {}).filter(([, value]) => value !== undefined),
      );
      rows = rows.map((entry) => (entry.id === args.path?.id ? { ...entry, ...changes } : entry));
      return rows.find((entry) => entry.id === args.path?.id);
    }
    if (operation === "DELETE /api/v2/admin/sections/{id}") {
      revision++;
      rows = rows.filter((entry) => entry.id !== args.path?.id);
      return undefined;
    }
    if (operation === "PUT /api/v2/admin/sections/order") {
      revision++;
      const ids = args.body?.ordered_ids as string[];
      rows = ids.map((id) => rows.find((entry) => entry.id === id)!);
      return { scope: "home", library_id: null, ordered_ids: ids };
    }
    throw new Error(`Unexpected ${operation}`);
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function setup(initialEntry = "/admin/sections") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={[initialEntry]}>
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
  const hook = renderHook(
    () => ({
      adapter: useAdminHomeRows(),
      params: useSearchParams()[0],
      deleteRow: useDeleteSection(),
    }),
    { wrapper },
  );
  return { ...hook, client };
}

async function ready(result: ReturnType<typeof setup>["result"]) {
  await waitFor(() => expect(result.current.adapter.status).toBe("ready"));
  await waitFor(() => expect(result.current.adapter.canEdit).toBe(true));
}

const writes = () =>
  calls.filter(
    (call) => !call.operation.startsWith("GET ") && call.operation !== "POST /api/v2/sections",
  );

describe("useAdminHomeRows", () => {
  it("maps the page's rows in server order", async () => {
    rows = [
      row("a", { featured: true }),
      row("b", { enabled: false, config: { source_provider: "trakt" } }),
    ];
    const { result } = setup();
    await ready(result);
    expect(result.current.adapter.rows).toEqual([
      expect.objectContaining({ id: "a", title: "Title a", hero: true, shown: true }),
      expect.objectContaining({ id: "b", shown: false, legacyTrakt: true, own: false }),
    ]);
    expect(result.current.adapter.pages).toEqual([
      { ref: { kind: "home" }, label: "Home" },
      { ref: { kind: "library", libraryId: 7 }, label: "Movies", libraryType: "movies" },
    ]);
  });

  it("reads the page from ?page= and falls back to Home for an unknown page", async () => {
    const library = setup("/admin/sections?page=7");
    await ready(library.result);
    expect(library.result.current.adapter.page).toEqual({ kind: "library", libraryId: 7 });
    expect(library.result.current.adapter.rows.map((entry) => entry.id)).toEqual(["lib"]);
    library.unmount();

    const unknown = setup("/admin/sections?page=99");
    await ready(unknown.result);
    expect(unknown.result.current.adapter.page).toEqual({ kind: "home" });
  });

  it("writes the page to ?page= when the admin switches pages", async () => {
    const { result } = setup("/admin/sections?keep=1");
    await ready(result);
    act(() => result.current.adapter.setPage({ kind: "library", libraryId: 7 }));
    await waitFor(() => expect(result.current.params.get("page")).toBe("7"));
    expect(result.current.params.get("keep")).toBe("1");
    await waitFor(() =>
      expect(result.current.adapter.rows.map((entry) => entry.id)).toEqual(["lib"]),
    );
  });

  it("reports a failed first read as an error, not an empty page", async () => {
    const implementation = mocks.request.getMockImplementation()!;
    mocks.request.mockImplementation((operation: string, args: Args) =>
      operation === "GET /api/v2/admin/sections"
        ? Promise.reject(new Error("Read unavailable"))
        : implementation(operation, args),
    );
    const { result } = setup();
    await waitFor(() => expect(result.current.adapter.status).toBe("error"));
    expect(result.current.adapter.error).toBe("Read unavailable");
    expect(result.current.adapter.canEdit).toBe(false);
  });

  it("turns a row off with the version it read, and stays pending until the list refetches", async () => {
    const { result } = setup();
    await ready(result);
    hold = { operation: "GET /api/v2/admin/sections" };
    let done!: Promise<void>;
    act(() => {
      done = result.current.adapter.setShown("a", false);
    });
    expect(result.current.adapter.rows[0]!.shown).toBe(false);
    expect(result.current.adapter.pending).toBe(true);
    await waitFor(() => expect(hold?.release).toBeTypeOf("function"));
    expect(writes()).toEqual([
      expect.objectContaining({ operation: "PATCH /api/v2/admin/sections/{id}" }),
    ]);
    expect(writes()[0]!.args.headers?.["If-Match"]).toBe('"rev-1"');
    expect(writes()[0]!.args.body).toEqual({ enabled: false });
    expect(result.current.adapter.pending).toBe(true);
    expect(result.current.adapter.canReorder).toBe(false);
    hold.release!();
    hold = null;
    await act(async () => done);
    expect(result.current.adapter.pending).toBe(false);
    expect(result.current.adapter.rows[0]!.shown).toBe(false);
    expect(result.current.adapter.canReorder).toBe(true);
  });

  it("refuses a quick action when the row changed since the page loaded", async () => {
    const { result } = setup();
    await ready(result);
    rows = rows.map((entry) =>
      entry.id === "a" ? { ...entry, title: "Renamed elsewhere" } : entry,
    );
    revision = 2;
    await act(async () => result.current.adapter.setHero("a", true));
    expect(writes()).toEqual([]);
    expect(result.current.adapter.conflict).toEqual({ scope: "row", rowId: "a" });
    expect(result.current.adapter.rows[0]!.hero).toBe(false);
  });

  it("rereads the page when a quick action's response is lost after the server applied it", async () => {
    const { result } = setup();
    await ready(result);
    const implementation = mocks.request.getMockImplementation()!;
    mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
      const response = await implementation(operation, args);
      if (operation === "PATCH /api/v2/admin/sections/{id}") throw new TypeError("Failed to fetch");
      return response;
    });
    await act(async () => result.current.adapter.setShown("a", false));
    expect(mocks.error).toHaveBeenCalledWith("Failed to fetch");
    expect(result.current.adapter.rows[0]!.shown).toBe(false);
    expect(result.current.adapter.conflict).toBeNull();

    mocks.request.mockImplementation(implementation);
    await act(async () => result.current.adapter.setHero("a", true));
    expect(result.current.adapter.conflict).toBeNull();
    expect(rows[0]).toEqual(expect.objectContaining({ enabled: false, featured: true }));
  });

  it("runs writes one at a time, each against the version after the last", async () => {
    const { result } = setup();
    await ready(result);
    let first!: Promise<void>;
    let second!: Promise<void>;
    act(() => {
      first = result.current.adapter.setShown("a", false);
      second = result.current.adapter.setHero("b", true);
    });
    await act(async () => Promise.all([first, second]));
    expect(writes().map((call) => [call.args.path?.id, call.args.headers?.["If-Match"]])).toEqual([
      ["a", '"rev-1"'],
      ["b", '"rev-2"'],
    ]);
    expect(result.current.adapter.conflict).toBeNull();
    expect(result.current.adapter.rows.map((entry) => [entry.shown, entry.hero])).toEqual([
      [false, false],
      [true, true],
    ]);
  });

  it("keeps the attempted order after a 412 and drops it on reload", async () => {
    const { result } = setup();
    await ready(result);
    const token = result.current.adapter.orderToken;
    revision = 2;
    await act(async () => result.current.adapter.reorder(["b", "a"], token));
    expect(writes()[0]!.args.headers?.["If-Match"]).toBe('"rev-1"');
    expect(result.current.adapter.conflict).toEqual({ scope: "page" });
    expect(result.current.adapter.rows.map((entry) => entry.id)).toEqual(["b", "a"]);
    expect(result.current.adapter.canReorder).toBe(false);
    await act(async () => result.current.adapter.reload());
    expect(result.current.adapter.conflict).toBeNull();
    expect(result.current.adapter.rows.map((entry) => entry.id)).toEqual(["a", "b"]);
  });

  it("rereads the page when a reorder's response is lost after the server applied it", async () => {
    const { result } = setup();
    await ready(result);
    const implementation = mocks.request.getMockImplementation()!;
    mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
      const response = await implementation(operation, args);
      if (operation === "PUT /api/v2/admin/sections/order") throw new TypeError("Failed to fetch");
      return response;
    });
    await act(async () =>
      result.current.adapter.reorder(["b", "a"], result.current.adapter.orderToken),
    );
    expect(mocks.error).toHaveBeenCalledWith("Failed to fetch");
    expect(result.current.adapter.rows.map((entry) => entry.id)).toEqual(["b", "a"]);
    expect(result.current.adapter.conflict).toBeNull();

    mocks.request.mockImplementation(implementation);
    await waitFor(() => expect(result.current.adapter.canReorder).toBe(true));
    await act(async () =>
      result.current.adapter.reorder(["a", "b"], result.current.adapter.orderToken),
    );
    expect(result.current.adapter.conflict).toBeNull();
    expect(rows.map((entry) => entry.id)).toEqual(["a", "b"]);
  });

  it("ignores a page switch while a write is pending", async () => {
    const { result } = setup();
    await ready(result);
    hold = { operation: "PUT /api/v2/admin/sections/order" };
    let done!: Promise<void>;
    act(() => {
      done = result.current.adapter.reorder(["b", "a"]);
    });
    act(() => result.current.adapter.setPage({ kind: "library", libraryId: 7 }));
    expect(result.current.adapter.page).toEqual({ kind: "home" });
    await waitFor(() => expect(hold?.release).toBeTypeOf("function"));
    hold.release!();
    hold = null;
    await act(async () => done);
    expect(result.current.adapter.rows.map((entry) => entry.id)).toEqual(["b", "a"]);
  });

  it("holds reorders while another row write and its refetch are in flight", async () => {
    const { result } = setup();
    await ready(result);
    hold = { operation: "DELETE /api/v2/admin/sections/{id}" };
    act(() => result.current.deleteRow.mutate({ id: "b", etag: '"rev-1"' }));
    await waitFor(() => expect(hold?.release).toBeTypeOf("function"));
    expect(result.current.adapter.pending).toBe(true);
    expect(result.current.adapter.canReorder).toBe(false);

    const releaseDelete = hold.release!;
    hold = { operation: "GET /api/v2/admin/sections" };
    releaseDelete();
    await waitFor(() => expect(result.current.deleteRow.isSuccess).toBe(true));
    // The delete landed but the list still holds the old version: a reorder now would 412.
    await waitFor(() => expect(hold?.release).toBeTypeOf("function"));
    expect(result.current.adapter.orderToken).toBe('"rev-1"');
    expect(result.current.adapter.pending).toBe(true);
    expect(result.current.adapter.canReorder).toBe(false);

    hold.release!();
    hold = null;
    await waitFor(() => expect(result.current.adapter.pending).toBe(false));
    expect(result.current.adapter.orderToken).toBe('"rev-2"');
    expect(result.current.adapter.canReorder).toBe(true);
  });

  it("keeps showing the latest switch state while an earlier toggle of the row finishes", async () => {
    const { result } = setup();
    await ready(result);
    hold = { operation: "PATCH /api/v2/admin/sections/{id}" };
    let off!: Promise<void>;
    let on!: Promise<void>;
    act(() => {
      off = result.current.adapter.setShown("a", false);
      on = result.current.adapter.setShown("a", true);
    });
    expect(result.current.adapter.rows[0]!.shown).toBe(true);
    await waitFor(() => expect(hold?.release).toBeTypeOf("function"));
    const releaseOff = hold.release!;
    hold.release = undefined;
    releaseOff();
    await act(async () => off);
    // The first write landed (server: off) while the second is still queued.
    await waitFor(() => expect(hold?.release).toBeTypeOf("function"));
    expect(rows[0]!.enabled).toBe(false);
    expect(result.current.adapter.rows[0]!.shown).toBe(true);
    hold.release!();
    hold = null;
    await act(async () => on);
    expect(result.current.adapter.rows[0]!.shown).toBe(true);
    expect(writes().map((call) => call.args.body)).toEqual([{ enabled: false }, { enabled: true }]);
  });

  describe("turning several rows on or off", () => {
    /** Row versions like the server's: each row has its own. */
    function perRowVersions() {
      const implementation = mocks.request.getMockImplementation()!;
      const versions = new Map<string, number>();
      let inFlight = 0;
      const stats = { maxInFlight: 0 };
      mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
        const id = args.path?.id;
        if (operation === "GET /api/v2/admin/sections/{id}" || operation.startsWith("PATCH ")) {
          calls.push({ operation, args });
          inFlight++;
          stats.maxInFlight = Math.max(stats.maxInFlight, inFlight);
          await new Promise((resolve) => setTimeout(resolve, 5));
          inFlight--;
          const version = `"row-${id}-${versions.get(id!) ?? 1}"`;
          if (operation.startsWith("GET ")) {
            args.onResponse?.(new Response(null, { headers: { ETag: version } }));
            return { ...rows.find((entry) => entry.id === id)! };
          }
          if (args.headers?.["If-Match"] !== version)
            throw v2Problem(412, "precondition_failed", "Changed on another client");
          versions.set(id!, (versions.get(id!) ?? 1) + 1);
          rows = rows.map((entry) => (entry.id === id ? { ...entry, ...args.body } : entry));
          return rows.find((entry) => entry.id === id);
        }
        return implementation(operation, args);
      });
      return stats;
    }

    it("writes up to four rows at a time, reports the row that changed, and refetches once", async () => {
      rows = ["a", "b", "c", "d", "e", "f"].map((id, index) => row(id, { position: index }));
      const { result } = setup();
      await ready(result);
      const stats = perRowVersions();
      rows = rows.map((entry) =>
        entry.id === "b" ? { ...entry, title: "Renamed elsewhere" } : entry,
      );
      calls = [];
      let outcome!: Awaited<ReturnType<typeof result.current.adapter.setShownMany>>;
      await act(async () => {
        outcome = await result.current.adapter.setShownMany(["a", "b", "c", "d", "e", "f"], false);
      });
      expect(outcome.changedIds.sort()).toEqual(["a", "c", "d", "e", "f"]);
      expect(outcome.failures).toEqual([
        expect.objectContaining({ id: "b", title: "Title b", reason: "changed" }),
      ]);
      const patched = writes().map((call) => [call.args.path?.id, call.args.body]);
      expect(patched).toHaveLength(5);
      expect(patched).not.toContainEqual(["b", expect.anything()]);
      expect(patched).toContainEqual(["a", { enabled: false }]);
      expect(stats.maxInFlight).toBe(4);
      // One refetch fence for the whole batch, not one per row.
      expect(calls.filter((call) => call.operation === "GET /api/v2/admin/sections")).toHaveLength(
        1,
      );
      expect(result.current.adapter.pending).toBe(false);
      expect(result.current.adapter.rows.find((entry) => entry.id === "b")).toMatchObject({
        title: "Renamed elsewhere",
        shown: true,
      });
    });

    it("skips rows already in that state and refuses to turn a legacy Trakt row back on", async () => {
      rows = [
        row("a", { enabled: false }),
        row("b", { enabled: true }),
        row("t", { enabled: false, config: { source_provider: "trakt" } }),
      ];
      const { result } = setup();
      await ready(result);
      perRowVersions();
      let outcome!: Awaited<ReturnType<typeof result.current.adapter.setShownMany>>;
      await act(async () => {
        outcome = await result.current.adapter.setShownMany(["a", "b", "t"], true);
      });
      expect(outcome.changedIds).toEqual(["a"]);
      expect(outcome.failures).toEqual([expect.objectContaining({ id: "t", reason: "legacy" })]);
      expect(writes().map((call) => call.args.path?.id)).toEqual(["a"]);
    });

    it("reports a row that looks already done here but was changed on another client", async () => {
      rows = [row("a", { enabled: false }), row("b", { enabled: false })];
      const { result } = setup();
      await ready(result);
      perRowVersions();
      rows = rows.map((entry) => (entry.id === "a" ? { ...entry, enabled: true } : entry));
      let outcome!: Awaited<ReturnType<typeof result.current.adapter.setShownMany>>;
      await act(async () => {
        outcome = await result.current.adapter.setShownMany(["a", "b"], false);
      });
      expect(outcome.changedIds).toEqual([]);
      expect(outcome.failures).toEqual([
        expect.objectContaining({ id: "a", title: "Title a", reason: "changed" }),
      ]);
      expect(writes()).toHaveLength(0);
    });

    it("stays pending from the first write until the refetch after the batch", async () => {
      rows = [row("a"), row("b")];
      const { result } = setup();
      await ready(result);
      perRowVersions();
      hold = { operation: "GET /api/v2/admin/sections" };
      let done!: Promise<unknown>;
      act(() => {
        done = result.current.adapter.setShownMany(["a", "b"], false);
      });
      expect(result.current.adapter.pending).toBe(true);
      await waitFor(() => expect(hold?.release).toBeTypeOf("function"));
      expect(writes()).toHaveLength(2);
      expect(result.current.adapter.pending).toBe(true);
      expect(result.current.adapter.canReorder).toBe(false);
      hold.release!();
      hold = null;
      await act(async () => done);
      expect(result.current.adapter.pending).toBe(false);
    });
  });

  it("reports a library link as an error with a retry when the libraries fail to load", async () => {
    const refetch = vi.fn(async () => undefined);
    mocks.libraries.mockReturnValue({
      data: undefined,
      isError: true,
      error: new Error("Libraries unavailable"),
      refetch,
    });
    const { result } = setup("/admin/sections?page=7");
    await waitFor(() => expect(result.current.adapter.status).toBe("error"));
    expect(result.current.adapter.error).toBe("Libraries unavailable");
    expect(result.current.adapter.canEdit).toBe(false);
    await act(async () => result.current.adapter.reload());
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  describe("library page copies", () => {
    const draft = {
      sectionType: "trending_on_server",
      title: "Trending This Week",
      titleFollowsVariant: true,
      config: { window: "7d" },
      itemLimit: 20,
      hero: false,
    };

    beforeEach(() => {
      mocks.libraries.mockReturnValue({
        data: [
          { id: 7, name: "Movies", type: "movies" },
          { id: 8, name: "TV Shows", type: "shows" },
          { id: 9, name: "Kids", type: "movies" },
        ],
      });
    });

    it("are offered on library pages only", async () => {
      const home = setup();
      await ready(home.result);
      expect(home.result.current.adapter.capabilities.libraryCopies).toBe(false);
      home.unmount();

      const library = setup("/admin/sections?page=7");
      await ready(library.result);
      expect(library.result.current.adapter.capabilities.libraryCopies).toBe(true);
    });

    it("adds a new row to this page and the others in one request, and finds it here", async () => {
      const { result } = setup("/admin/sections?page=7");
      await ready(result);
      let created!: { newIds: string[] };
      await act(async () => {
        created = await result.current.adapter.create({ ...draft, extraLibraryIds: [8, 9, 7] });
      });
      expect(writes()).toEqual([
        expect.objectContaining({ operation: "POST /api/v2/admin/sections/bulk" }),
      ]);
      expect(writes()[0]!.args.body).toEqual({
        scope: "library",
        library_ids: ["7", "8", "9"],
        section_type: "trending_on_server",
        title: "Trending This Week",
        item_limit: 20,
        featured: false,
        enabled: true,
        config: { window: "7d" },
      });
      const here = libraryRows.filter((entry) => entry.library_id === "7");
      expect(here.map((entry) => entry.id)).toEqual(["lib", created.newIds[0]]);
      expect(created.newIds).toHaveLength(1);
      expect(result.current.adapter.rows.map((entry) => entry.id)).toEqual([
        "lib",
        ...created.newIds,
      ]);
      expect(result.current.adapter.pending).toBe(false);
    });

    it("adds a hero row to this page only", async () => {
      const { result } = setup("/admin/sections?page=7");
      await ready(result);
      mocks.request.mockImplementationOnce(async (operation: string, args: Args) => {
        calls.push({ operation, args });
        return row("hero", { ...(args.body as Partial<Row>), scope: "library" });
      });
      await act(async () => {
        await result.current.adapter.create({ ...draft, hero: true, extraLibraryIds: [8] });
      });
      expect(writes().map((call) => call.operation)).toEqual(["POST /api/v2/admin/sections"]);
      expect(writes()[0]!.args.body).toMatchObject({ library_id: "7", featured: true });
    });

    it("copies an existing row to the other pages only, never as a hero banner", async () => {
      libraryRows = [
        row("lib", {
          scope: "library",
          library_id: "7",
          title: "Trending This Week",
          section_type: "trending_on_server",
          featured: true,
          enabled: false,
          item_limit: 30,
          config: { window: "7d" },
        }),
      ];
      const { result } = setup("/admin/sections?page=7");
      await ready(result);
      await act(async () => {
        await result.current.adapter.copyToLibraries("lib", [7, 8, 9]);
      });
      expect(writes()).toHaveLength(1);
      expect(writes()[0]!.args.body).toEqual({
        scope: "library",
        library_ids: ["8", "9"],
        section_type: "trending_on_server",
        title: "Trending This Week",
        item_limit: 30,
        featured: false,
        enabled: false,
        config: { window: "7d" },
      });
      expect(result.current.adapter.rows.map((entry) => entry.id)).toEqual(["lib"]);
      expect(libraryRows.map((entry) => entry.library_id)).toEqual(["7", "8", "9"]);
    });

    it("refuses to copy a row that changed since the page loaded", async () => {
      const { result } = setup("/admin/sections?page=7");
      await ready(result);
      libraryRows = libraryRows.map((entry) => ({ ...entry, title: "Renamed elsewhere" }));
      await act(async () => {
        await expect(result.current.adapter.copyToLibraries("lib", [8])).rejects.toBeInstanceOf(
          RowChangedError,
        );
      });
      expect(writes()).toEqual([]);
      expect(result.current.adapter.conflict).toEqual({ scope: "row", rowId: "lib" });
    });

    it("refuses to copy a row whose settings name a library", async () => {
      libraryRows = [
        row("lib", {
          scope: "library",
          library_id: "7",
          config: { filter_library_ids: [7] },
        }),
      ];
      const { result } = setup("/admin/sections?page=7");
      await ready(result);
      await act(async () => {
        await expect(result.current.adapter.copyToLibraries("lib", [8])).rejects.toThrow(
          "can't be added to other libraries",
        );
      });
      expect(writes()).toEqual([]);
    });
  });
});
