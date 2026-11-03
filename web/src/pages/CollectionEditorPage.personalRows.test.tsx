/**
 * Where it shows on a personal collection's editor: the rows on the viewer's
 * own Home and library pages that show it, read only once the panel is on
 * screen and never more than four pages at once, and Add as a row into
 * Settings > Home Screen (asking first about unsaved changes). The editor
 * itself never changes a row.
 */
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getCollectionOk from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import { ROWS_NOT_LISTED } from "@/lib/collections/copy";
import { safeReturnPath } from "@/lib/homeRows/rowLinks";
import { emptyPreview, personalSmartCollection } from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";
import {
  location,
  locationParams,
  prepareEditorPage,
  showEditorPage,
} from "./collectionEditorPage.test-support";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("@/hooks/useDebounce", () => ({ useDebounce: (value: string) => value }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner", name: "Sam" }, isLoading: false }),
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));

/** The libraries the profile sees, changeable per test. */
const viewer = vi.hoisted(() => ({
  libraries: [] as Array<{ id: number; name: string; type: string }>,
  /** True while the profile's hidden-library preferences are still loading. */
  loading: false,
}));
vi.mock("@/hooks/queries/libraries", async () => ({
  ...(await vi.importActual<typeof import("@/hooks/queries/libraries")>(
    "@/hooks/queries/libraries",
  )),
  useUserLibraries: () => ({
    data: viewer.libraries,
    isLoading: viewer.loading,
    isError: false,
    refetch: vi.fn(),
  }),
}));

installV2Recorder();

/** A row on one of the profile's pages, as Settings > Home Screen reads it. */
function entry(id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    section_type: "continue_watching",
    title: id,
    default_title: id,
    featured: false,
    item_limit: 20,
    hidden: false,
    is_custom: false,
    customized: false,
    position: 0,
    ...overrides,
  };
}

function collectionRow(id: string, title: string, overrides: Record<string, unknown> = {}) {
  return entry(id, {
    section_type: "collection",
    title,
    default_title: "",
    is_custom: true,
    config: { user_collection_id: "c1" },
    ...overrides,
  });
}

/** Each page's rows, by `home` or library id. */
let pages: Record<string, unknown[]>;

function pageKey(call: RecordedCall) {
  return call.query?.scope === "home" ? "home" : String(call.query?.library_id);
}

/** A personal Smart collection matching Movies and Kids. */
const MATCHED = personalSmartCollection({ library_ids: [1, 2] });

beforeEach(() => {
  observers = [];
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      readonly root = null;
      readonly rootMargin = "0px";
      readonly thresholds = [0];
      private readonly record: (typeof observers)[number];
      constructor(callback: IntersectionObserverCallback) {
        this.record = { callback, targets: new Set() };
        observers.push(this.record);
      }
      observe = (target: Element) => this.record.targets.add(target);
      unobserve = (target: Element) => this.record.targets.delete(target);
      disconnect = () => this.record.targets.clear();
      takeRecords = () => [];
    },
  );
  prepareEditorPage();
  viewer.loading = false;
  viewer.libraries = [
    { id: 1, name: "Movies", type: "movies" },
    { id: 2, name: "Kids", type: "movies" },
    { id: 3, name: "4K Movies", type: "movies" },
  ];
  pages = {
    home: [
      entry("s-1"),
      entry("s-2"),
      entry("s-3"),
      collectionRow("u-home", "My Comfort Shows"),
      entry("s-5"),
      entry("s-6"),
      entry("s-7"),
      entry("s-8"),
    ],
    "1": [entry("s-movies")],
    "2": [
      entry("s-kids"),
      collectionRow("u-kids", "Rainy afternoons", { hidden: true }),
      entry("s-kids-3"),
      entry("s-kids-4"),
      entry("s-kids-5"),
    ],
  };
  v2Recorder.answer("GET /api/v2/collections/{id}", MATCHED);
  v2Recorder.answer("GET /api/v2/collections", { items: [MATCHED] });
  v2Recorder.answer("POST /api/v2/collections/preview", emptyPreview);
  v2Recorder.answer("GET /api/v2/collections/{id}/items/order", {
    ordered_ids: [],
    has_more: false,
  });
  v2Recorder.answer("GET /api/v2/profile/sections/settings", (call: RecordedCall) => ({
    items: pages[pageKey(call)] ?? [],
  }));
});

afterEach(() => {
  vi.unstubAllGlobals();
});

let observers: Array<{ callback: IntersectionObserverCallback; targets: Set<Element> }>;

/** The panel comes on screen. */
function reveal() {
  act(() => {
    for (const observer of observers)
      for (const target of observer.targets)
        observer.callback(
          [{ isIntersecting: true, target } as IntersectionObserverEntry],
          {} as IntersectionObserver,
        );
  });
}

const EDITOR = "/collections/c1/edit";
const settingsReads = () => v2Recorder.callsOf("GET /api/v2/profile/sections/settings");

async function rowsGroup() {
  await screen.findByRole("textbox", { name: "Name" });
  reveal();
  return screen.findByRole("group", { name: "Rows that show it" });
}

async function openAddAsRow(user: ReturnType<typeof userEvent.setup>) {
  await rowsGroup();
  await user.click(await screen.findByRole("button", { name: /Add as a row/ }));
  return screen.findByRole("menu");
}

describe("rows that show a personal collection", () => {
  it("reads nothing until Where it shows comes on screen", async () => {
    showEditorPage(EDITOR, { where: true });
    await screen.findByRole("textbox", { name: "Name" });
    expect(settingsReads()).toHaveLength(0);
    expect(screen.queryByRole("group", { name: "Rows that show it" })).toBeNull();
  });

  it("reads no page until it knows which libraries the profile hides", async () => {
    // Until the preferences load, the library list still holds hidden libraries.
    viewer.loading = true;
    showEditorPage(EDITOR, { where: true });
    await rowsGroup();
    await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
    expect(settingsReads()).toHaveLength(0);
  });

  it("lists the rows on your Home and your library pages", async () => {
    showEditorPage(EDITOR, { where: true });
    const group = await rowsGroup();
    const home = await within(group).findByRole("link", {
      name: "My Comfort Shows, My Home · row 4 of 8",
    });
    expect(home).toHaveAttribute("href", "/settings/home-screen?page=home&edit=u-home");
    expect(
      within(group).getByRole("link", {
        name: "Rainy afternoons, My Kids page · row 2 of 5 · Hidden",
      }),
    ).toHaveAttribute("href", "/settings/home-screen?page=2&edit=u-kids");
    expect(group).toHaveTextContent("2 rows");
    expect(group).not.toHaveTextContent(ROWS_NOT_LISTED);
  });

  it("lists a row on the page of a library it doesn't match", async () => {
    // Home Screen offers every personal collection on every page, and a
    // collection's libraries can change after its rows were added.
    pages["3"] = [entry("s-4k"), collectionRow("u-4k", "Big screen")];
    showEditorPage(EDITOR, { where: true });
    const group = await rowsGroup();
    expect(
      await within(group).findByRole("link", {
        name: "Big screen, My 4K Movies page · row 2 of 2",
      }),
    ).toHaveAttribute("href", "/settings/home-screen?page=3&edit=u-4k");
    expect(group).toHaveTextContent("3 rows");
    expect(settingsReads().map(pageKey).sort()).toEqual(["1", "2", "3", "home"]);
  });

  it("reads every page you see when it matches every library, never more than 4 at once", async () => {
    v2Recorder.answer("GET /api/v2/collections/{id}", getCollectionOk);
    viewer.libraries = Array.from({ length: 6 }, (_, index) => ({
      id: index + 1,
      name: `Library ${index + 1}`,
      type: "movies",
    }));
    const gates: Array<() => void> = [];
    let active = 0;
    let peak = 0;
    v2Recorder.answer("GET /api/v2/profile/sections/settings", async () => {
      active += 1;
      peak = Math.max(peak, active);
      await new Promise<void>((resolve) => gates.push(resolve));
      active -= 1;
      return { items: [] };
    });
    showEditorPage(EDITOR, { where: true });
    await rowsGroup();
    await waitFor(() => expect(settingsReads()).toHaveLength(4));
    expect(active).toBe(4);
    // Each page that answers lets the next one start.
    while (settingsReads().length < 7 || gates.length > 0) {
      await act(async () => gates.shift()?.());
      await waitFor(() => expect(active).toBeLessThanOrEqual(4));
    }
    expect(await screen.findByText(/None of your Home or library page rows/)).toBeInTheDocument();
    expect(peak).toBe(4);
    expect(settingsReads().map(pageKey).sort()).toEqual(["1", "2", "3", "4", "5", "6", "home"]);
  });

  it("offers Retry when a page doesn't load", async () => {
    let fail = true;
    v2Recorder.answer("GET /api/v2/profile/sections/settings", (call: RecordedCall) => {
      if (fail && pageKey(call) === "2") throw new Error("offline");
      return { items: pages[pageKey(call)] ?? [] };
    });
    const user = userEvent.setup();
    showEditorPage(EDITOR, { where: true });
    const group = await rowsGroup();
    expect(await within(group).findByRole("alert")).toHaveTextContent(
      "Couldn't load the rows that show it.",
    );
    fail = false;
    await user.click(within(group).getByRole("button", { name: "Retry" }));
    expect(
      await within(group).findByRole("link", { name: /^Rainy afternoons/ }),
    ).toBeInTheDocument();
  });
});

describe("Add as a row on your own pages", () => {
  it("opens Add row in Settings > Home Screen on that page, with a way back, changing nothing", async () => {
    const user = userEvent.setup();
    showEditorPage(EDITOR, { where: true });
    const menu = await openAddAsRow(user);
    await user.click(within(menu).getByRole("menuitem", { name: "My Kids page" }));
    await waitFor(() => expect(location()).toMatch(/^\/settings\/home-screen\?/));
    const params = locationParams();
    expect(params.get("page")).toBe("2");
    expect(params.get("add")).toBe("collection:user:c1");
    expect(params.get("return")).toBe(EDITOR);
    expect(safeReturnPath(params.get("return"))).toBe(EDITOR);
    expect(v2Recorder.writes()).toEqual([]);
  });
});
