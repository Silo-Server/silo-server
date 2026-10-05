import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, type ProfileRequestContextSnapshot } from "@/api/client";
import type { SectionOverride, SettingsSectionEntry } from "@/api/types";
import { v2Problem } from "@/api/v2/problems.test-support";
import { useProfileHomeRows } from "./useProfileHomeRows";

const mocks = vi.hoisted(() => ({ request: vi.fn(), error: vi.fn(), success: vi.fn() }));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({ toast: { success: mocks.success, error: mocks.error } }));

type Args = {
  profileContext?: ProfileRequestContextSnapshot;
  query?: { scope?: string; library_id?: string };
  body?: { overrides: SectionOverride[] };
};

function entry(id: string, overrides: Partial<SettingsSectionEntry> = {}): SettingsSectionEntry {
  return {
    id,
    section_type: "recently_added",
    title: `Title ${id}`,
    featured: false,
    item_limit: 20,
    hidden: false,
    is_custom: false,
    customized: false,
    position: 0,
    config: {},
    ...overrides,
  };
}

/** The server rows of each page and the overrides the profile saved for it. */
let pages: Record<string, { rows: SettingsSectionEntry[]; saved: SectionOverride[] }>;
let puts: Array<{ page: string; overrides: SectionOverride[]; profileId?: string }>;
let calls: string[];
/** Operations a test holds open; each call parks until the test settles it. */
let held: Map<string, Array<{ resolve: () => void; reject: (error: unknown) => void }>>;

function pageKey(args: Args) {
  return args.query?.scope === "library" ? `library:${args.query.library_id}` : "home";
}

/** The page as the settings route resolves it: server rows with the saved overrides applied. */
function resolve(key: string): SettingsSectionEntry[] {
  const { rows, saved } = pages[key]!;
  const byRow = new Map(saved.filter((o) => o.section_id).map((o) => [o.section_id!, o]));
  const resolved: SettingsSectionEntry[] = [];
  rows.forEach((row, index) => {
    const override = byRow.get(row.id);
    if (override?.removed) return;
    resolved.push({
      ...row,
      position: override?.position ?? index,
      hidden: override?.hidden ?? row.hidden,
      title: override?.title || row.title,
      customized: Boolean(override),
    });
  });
  for (const own of saved.filter((o) => !o.section_id)) {
    resolved.push(
      entry(own.id!, {
        section_type: own.section_type!,
        title: own.title!,
        hidden: Boolean(own.hidden),
        position: own.position ?? 0,
        is_custom: true,
        customized: true,
        config: own.config,
      }),
    );
  }
  return resolved.sort((a, b) => a.position - b.position);
}

function hold(operation: string) {
  held.set(operation, []);
}

async function settle(operation: string, error?: unknown) {
  await waitFor(() => expect(held.get(operation)?.length).toBeGreaterThan(0));
  const call = held.get(operation)!.shift()!;
  await act(async () => (error ? call.reject(error) : call.resolve()));
}

beforeEach(() => {
  vi.clearAllMocks();
  setAccessToken("token");
  setProfileId("parent");
  pages = {
    home: {
      rows: [entry("a"), entry("b", { position: 1 }), entry("c", { position: 2 })],
      saved: [],
    },
    "library:7": { rows: [entry("lib")], saved: [] },
  };
  puts = [];
  calls = [];
  held = new Map();
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    calls.push(operation);
    const queue = held.get(operation);
    if (queue) {
      await new Promise<void>((resolve, reject) => queue.push({ resolve, reject }));
    }
    const key = pageKey(args);
    if (operation === "GET /api/v2/profile/sections/settings") return { items: resolve(key) };
    if (operation === "GET /api/v2/profile/sections") return { items: pages[key]!.saved };
    if (operation === "PUT /api/v2/profile/sections") {
      puts.push({
        page: key,
        overrides: args.body!.overrides,
        profileId: args.profileContext?.profileId,
      });
      pages[key]!.saved = args.body!.overrides;
      return { items: args.body!.overrides };
    }
    if (operation === "DELETE /api/v2/profile/sections") {
      pages[key]!.saved = [];
      return undefined;
    }
    throw new Error(`Unexpected ${operation}`);
  });
});

afterEach(() => {
  setProfileId(null);
  setAccessToken(null);
});

function setup() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return renderHook(() => useProfileHomeRows(), { wrapper });
}

async function ready() {
  const view = setup();
  await waitFor(() => expect(view.result.current.canEdit).toBe(true));
  return view;
}

const hiddenIds = (overrides: SectionOverride[]) =>
  overrides.filter((o) => o.hidden).map((o) => o.section_id ?? o.id);
const shownTitles = (sections: SettingsSectionEntry[]) =>
  sections.filter((s) => !s.hidden).map((s) => s.id);

describe("useProfileHomeRows", () => {
  it("sends two quick changes as two saves in order, and the second one wins", async () => {
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    act(() => result.current.setHidden("b", true));
    // Only one save is in flight; the second change waits for it.
    await waitFor(() => expect(held.get("PUT /api/v2/profile/sections")).toHaveLength(1));
    expect(result.current.pending).toBe(true);
    expect(shownTitles(result.current.sections)).toEqual(["c"]);

    await settle("PUT /api/v2/profile/sections");
    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(puts.map((put) => hiddenIds(put.overrides))).toEqual([["a"], ["a", "b"]]);
    expect(shownTitles(result.current.sections)).toEqual(["c"]);
  });

  it("merges changes made while a save is in flight into one next save", async () => {
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    act(() => result.current.setHidden("b", true));
    act(() => result.current.move("c", ["c", "a", "b"]));

    await settle("PUT /api/v2/profile/sections");
    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(puts).toHaveLength(2);
    expect(puts[1]!.overrides.map((o) => [o.section_id, o.position, o.hidden])).toEqual([
      ["c", 0, false],
      ["a", 1, true],
      ["b", 2, true],
    ]);
    expect(result.current.sections.map((s) => s.id)).toEqual(["c", "a", "b"]);
  });

  it("sends every merged change, including one to a legacy Trakt row with no saved override", async () => {
    pages.home!.rows = [
      entry("a"),
      entry("trakt", { position: 1, config: { source: "trakt", list: "trending" } }),
      entry("b", { position: 2 }),
    ];
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    // The Trakt row and another row change while the first save is in flight.
    act(() => result.current.saveSection({ ...result.current.sections[1]!, title: "Trakt" }));
    act(() => result.current.setHidden("b", true));

    await settle("PUT /api/v2/profile/sections");
    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));

    // The first save stores only the row it changed, so the shown Trakt row
    // is left out; the merged one names it.
    expect(puts[0]!.overrides.map((o) => o.section_id)).toEqual(["a"]);
    expect(puts[1]!.overrides).toContainEqual(
      expect.objectContaining({ section_id: "trakt", title: "Trakt" }),
    );
    expect(hiddenIds(puts[1]!.overrides)).toEqual(["a", "b"]);
  });

  it("does not pin an admin edit that lands between two saves of one burst", async () => {
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    // An admin changes row b's size and moves row c above it while the first
    // save is in flight, so the refetch after it reads the admin's new rows.
    pages.home!.rows = [
      entry("a"),
      entry("c", { position: 1 }),
      entry("b", { position: 2, item_limit: 30 }),
    ];
    act(() => result.current.setHidden("c", true));

    await settle("PUT /api/v2/profile/sections");
    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));

    // The second save stores only the rows the profile hid, with no positions
    // and nothing for row b, which keeps following the admin.
    expect(puts[1]!.overrides.map((o) => o.section_id)).toEqual(["a", "c"]);
    expect(puts[1]!.overrides.every((o) => o.position === undefined)).toBe(true);
    expect(puts[1]!.overrides.every((o) => o.item_limit === undefined)).toBe(true);
  });

  it("keeps an older edit when its save fails and a newer save is queued", async () => {
    pages.home!.rows = [
      entry("a"),
      entry("trakt", { position: 1, config: { source: "trakt", list: "trending" } }),
      entry("b", { position: 2 }),
    ];
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    // The Trakt row's edit is in flight when the next change is made.
    act(() => result.current.saveSection({ ...result.current.sections[1]!, title: "Trakt" }));
    act(() => result.current.setHidden("b", true));
    await settle("PUT /api/v2/profile/sections", v2Problem(500, "internal", "boom"));
    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(puts.at(-1)!.overrides).toContainEqual(
      expect.objectContaining({ section_id: "trakt", title: "Trakt" }),
    );
    expect(hiddenIds(puts.at(-1)!.overrides)).toEqual(["b"]);
  });

  it("sends each save as the profile that made it, and drops it after a profile switch", async () => {
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    act(() => result.current.setHidden("b", true));
    await waitFor(() => expect(held.get("PUT /api/v2/profile/sections")).toHaveLength(1));
    // Another household profile is picked while the first save is in flight.
    setProfileId("child");
    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(puts.map((put) => put.profileId)).toEqual(["parent"]);
    expect(calls.filter((c) => c === "PUT /api/v2/profile/sections")).toHaveLength(1);
  });

  it("starts the next profile's change from its own rows while the last profile's save is in flight", async () => {
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    await waitFor(() => expect(held.get("PUT /api/v2/profile/sections")).toHaveLength(1));
    setProfileId("child");
    act(() => result.current.setHidden("b", true));
    await settle("PUT /api/v2/profile/sections");
    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(puts.map((put) => [put.profileId, hiddenIds(put.overrides)])).toEqual([
      ["parent", ["a"]],
      ["child", ["b"]],
    ]);
  });

  it("leaves the last profile's queued rows out of the next profile's save", async () => {
    pages.home!.rows = [
      entry("a"),
      entry("trakt", { position: 1, config: { source: "trakt", list: "trending" } }),
      entry("b", { position: 2 }),
    ];
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    await waitFor(() => expect(held.get("PUT /api/v2/profile/sections")).toHaveLength(1));
    // Queued behind the save in flight, then dropped by the profile switch.
    act(() => result.current.saveSection({ ...result.current.sections[1]!, title: "Trakt" }));
    setProfileId("child");
    act(() => result.current.move("b", ["b", "a", "trakt"]));
    await settle("PUT /api/v2/profile/sections");
    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(puts.map((put) => put.profileId)).toEqual(["parent", "child"]);
    // The child never touched the legacy Trakt row, so its save leaves it out.
    expect(puts[1]!.overrides.map((o) => o.section_id)).not.toContain("trakt");
  });

  it("holds edits after a failed save until the page is read again", async () => {
    const { result } = await ready();
    // The server applies the save, but its answer never arrives.
    const implementation = mocks.request.getMockImplementation()!;
    mocks.request.mockImplementationOnce(async (operation: string, args: Args = {}) => {
      await implementation(operation, args);
      throw new TypeError("Failed to fetch");
    });
    hold("GET /api/v2/profile/sections/settings");

    act(() => result.current.setHidden("a", true));
    await waitFor(() => expect(held.get("GET /api/v2/profile/sections/settings")).toHaveLength(1));
    expect(result.current.canEdit).toBe(false);
    act(() => result.current.setHidden("b", true));
    await settle("GET /api/v2/profile/sections/settings");
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(puts).toHaveLength(1);
    expect(result.current.canEdit).toBe(true);
    expect(shownTitles(result.current.sections)).toEqual(["b", "c"]);
    act(() => result.current.setHidden("b", true));
    await waitFor(() => expect(puts).toHaveLength(2));
    expect(hiddenIds(puts[1]!.overrides)).toEqual(["a", "b"]);
  });

  it("keeps a saved edit to a legacy Trakt row when the overrides fail to reload before the next save", async () => {
    pages.home!.rows = [
      entry("a"),
      entry("trakt", { position: 1, config: { source: "trakt", list: "trending" } }),
      entry("b", { position: 2 }),
    ];
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.saveSection({ ...result.current.sections[1]!, title: "Trakt" }));
    act(() => result.current.setHidden("b", true));
    hold("GET /api/v2/profile/sections");
    await settle("PUT /api/v2/profile/sections");
    await settle("GET /api/v2/profile/sections", new TypeError("Failed to fetch"));
    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(puts).toHaveLength(2));

    expect(puts[1]!.overrides).toContainEqual(
      expect.objectContaining({ section_id: "trakt", title: "Trakt" }),
    );
    expect(hiddenIds(puts[1]!.overrides)).toEqual(["b"]);
  });

  it("places a moved row by the order the user saw, after the rows change underneath", async () => {
    const { result } = await ready();
    // The user drags c between a and b while a refetch brings a new row x.
    pages.home!.rows = [
      entry("x"),
      ...pages.home!.rows.map((row) => ({ ...row, position: row.position + 1 })),
    ];
    await act(async () => result.current.reload());
    await waitFor(() =>
      expect(result.current.sections.map((s) => s.id)).toEqual(["x", "a", "b", "c"]),
    );

    act(() => result.current.move("c", ["a", "c", "b"]));
    expect(result.current.sections.map((s) => s.id)).toEqual(["x", "a", "c", "b"]);
    await waitFor(() => expect(result.current.pending).toBe(false));
    expect(puts).toHaveLength(1);
  });

  it("refuses a page switch while the page still has saves to send", async () => {
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    let switched = true;
    act(() => {
      switched = result.current.setPage({ kind: "library", libraryId: 7 });
    });
    expect(switched).toBe(false);
    expect(result.current.page).toEqual({ kind: "home" });

    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));
    act(() => {
      switched = result.current.setPage({ kind: "library", libraryId: 7 });
    });
    expect(switched).toBe(true);
    await waitFor(() => expect(result.current.sections.map((s) => s.id)).toEqual(["lib"]));
    expect(puts.map((put) => put.page)).toEqual(["home"]);
  });

  it("puts the last saved state back and says why when a save fails", async () => {
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    expect(shownTitles(result.current.sections)).toEqual(["b", "c"]);
    await settle(
      "PUT /api/v2/profile/sections",
      v2Problem(403, "permission_denied", "This action is not available in demo mode."),
    );
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(shownTitles(result.current.sections)).toEqual(["a", "b", "c"]);
    expect(mocks.error).toHaveBeenCalledWith(
      "Could not save your rows: This action is not available in demo mode.",
    );
  });

  it("does not undo a newer change when an older save fails", async () => {
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    act(() => result.current.setHidden("b", true));
    await settle("PUT /api/v2/profile/sections", v2Problem(500, "internal", "boom"));
    // The newer state is still on screen while it is sent.
    expect(shownTitles(result.current.sections)).toEqual(["c"]);
    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(mocks.error).toHaveBeenCalledTimes(1);
    expect(hiddenIds(puts.at(-1)!.overrides)).toEqual(["a", "b"]);
    expect(shownTitles(result.current.sections)).toEqual(["c"]);
  });

  it("stays pending until the refetch after a save lands", async () => {
    const { result } = await ready();
    hold("GET /api/v2/profile/sections/settings");

    act(() => result.current.setHidden("a", true));
    await waitFor(() => expect(puts).toHaveLength(1));
    await waitFor(() => expect(held.get("GET /api/v2/profile/sections/settings")).toHaveLength(1));
    expect(result.current.pending).toBe(true);

    await settle("GET /api/v2/profile/sections/settings");
    await waitFor(() => expect(result.current.pending).toBe(false));
    expect(shownTitles(result.current.sections)).toEqual(["b", "c"]);
  });

  it("writes nothing until the saved overrides load", async () => {
    hold("GET /api/v2/profile/sections");
    const { result } = setup();
    await waitFor(() => expect(result.current.sections).toHaveLength(3));

    expect(result.current.ready).toBe(false);
    expect(result.current.canEdit).toBe(false);
    act(() => result.current.setHidden("a", true));
    act(() => result.current.move("c", ["c", "a", "b"]));
    act(() => result.current.reset());
    expect(calls).not.toContain("PUT /api/v2/profile/sections");
    expect(calls).not.toContain("DELETE /api/v2/profile/sections");
    expect(shownTitles(result.current.sections)).toEqual(["a", "b", "c"]);

    await settle("GET /api/v2/profile/sections");
    await waitFor(() => expect(result.current.canEdit).toBe(true));
  });

  it("removes a server row as a removed override and deletes an own row outright", async () => {
    pages.home!.saved = [
      { id: "own", section_type: "trending_on_server", title: "Mine", position: 3 },
    ];
    const { result } = await ready();

    act(() => result.current.remove("b"));
    act(() => result.current.remove("own"));
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(puts.at(-1)!.overrides).toContainEqual(
      expect.objectContaining({ section_id: "b", removed: true }),
    );
    expect(puts.at(-1)!.overrides.some((o) => o.id === "own")).toBe(false);
    expect(result.current.sections.map((s) => s.id)).toEqual(["a", "c"]);
  });

  it("records when this tab last wrote each changed row", async () => {
    const { result } = await ready();
    expect(result.current.lastWriteAt("a")).toBeUndefined();
    const before = Date.now();

    act(() => result.current.setHidden("a", true));
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(result.current.lastWriteAt("a")).toBeGreaterThanOrEqual(before);
    expect(result.current.lastWriteAt("b")).toBeUndefined();
  });

  it("resets the page after the save before it, and blocks edits until the reset lands", async () => {
    const { result } = await ready();
    hold("PUT /api/v2/profile/sections");

    act(() => result.current.setHidden("a", true));
    act(() => result.current.reset());
    expect(result.current.canEdit).toBe(false);
    // Busy, not loading: the page has its rows and saved overrides.
    expect(result.current.ready).toBe(true);
    act(() => result.current.setHidden("b", true));

    await settle("PUT /api/v2/profile/sections");
    await waitFor(() => expect(result.current.pending).toBe(false));

    expect(calls.filter((c) => c === "PUT /api/v2/profile/sections")).toHaveLength(1);
    expect(calls.indexOf("DELETE /api/v2/profile/sections")).toBeGreaterThan(
      calls.indexOf("PUT /api/v2/profile/sections"),
    );
    expect(mocks.success).toHaveBeenCalledWith("Reset to the server's rows.");
    expect(result.current.canEdit).toBe(true);
    expect(shownTitles(result.current.sections)).toEqual(["a", "b", "c"]);
  });
});
