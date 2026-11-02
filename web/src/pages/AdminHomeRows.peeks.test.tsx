import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PEEK_STALE_MS } from "@/components/calm/usePeekLimiter";
import { recipeCatalogFixture } from "@/lib/homeRows/recipeCatalogFixture.test-support";
import {
  type Args,
  patchRow,
  readSections,
  renderAdminHomeRows,
  type Row,
  stored,
  stubResizeObserver,
  waitForAddRow,
} from "./adminHomeRows.test-support";

const mocks = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [{ id: 7, name: "Movies", type: "movies" }] }),
}));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollections: () => ({ data: [] }),
}));
vi.mock("@/hooks/queries/collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: vi.fn(),
}));
vi.mock("@/hooks/queries/useAllUserCollections", () => ({
  useAllUserCollections: () => ({ collections: [], isLoading: false }),
}));
vi.mock("@/lib/recipes", async () => ({
  ...(await vi.importActual<typeof import("@/lib/recipes")>("@/lib/recipes")),
  fetchRecipeCatalog: async () => recipeCatalogFixture,
}));
vi.mock("@/lib/thumbhash", async () => ({
  ...(await vi.importActual<typeof import("@/lib/thumbhash")>("@/lib/thumbhash")),
  decodeThumbhash: (hash: string) => `data:image/png;thumb=${hash}`,
}));

type PreviewItem = {
  content_id: string;
  title: string;
  poster_url?: string;
  poster_thumbhash?: string;
};

const POSTERS: PreviewItem[] = [
  { content_id: "m1", title: "Past Lives", poster_url: "https://img.test/past-lives.jpg" },
  { content_id: "m2", title: "Dune: Part Two", poster_thumbhash: "dune-hash" },
  { content_id: "m3", title: "Oppenheimer", poster_url: "https://img.test/oppenheimer.jpg" },
];

let rows: Row[];
let previews: Args[];
let previewAvailable: boolean;
/** What the preview route answers for a request; defaults to three titles. */
let answer: (args: Args) => Promise<{ items: PreviewItem[]; total_count: number }>;

// Peeks load only once their row is near the screen. This stand-in records what
// each observer watches; `reveal` reports chosen rows as on screen.
let observers: Array<{ callback: IntersectionObserverCallback; targets: Set<Element> }>;

function reveal(...titles: string[]) {
  act(() => {
    for (const title of titles) {
      const line = rowLine(title);
      for (const observer of observers) {
        const target = [...observer.targets].find((node) => line.contains(node));
        if (!target) continue;
        observer.callback(
          [{ isIntersecting: true, target } as IntersectionObserverEntry],
          {} as IntersectionObserver,
        );
      }
    }
  });
}

function rowLine(title: string): HTMLElement {
  const line = screen
    .getAllByRole("listitem")
    .find((item) => within(item).queryByText(title, { exact: true }));
  if (!line) throw new Error(`No row titled ${title}`);
  return line;
}

const peekRequests = () => previews.filter((args) => args.body?.item_limit === 3);

beforeEach(() => {
  vi.clearAllMocks();
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
  stubResizeObserver();
  rows = [
    stored("a", { position: 0, title: "Recently Added" }),
    stored("b", {
      position: 1,
      title: "Trending This Week",
      section_type: "trending_on_server",
      config: { window: "7d" },
    }),
    stored("c", { position: 2, title: "Mind-Bending Sci-Fi", enabled: false }),
  ];
  previews = [];
  previewAvailable = true;
  answer = async () => ({ items: POSTERS, total_count: 3 });
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    args.onResponse?.(new Response(null, { headers: { ETag: '"rev-1"' } }));
    const read = readSections(operation, args, rows, { preview: previewAvailable });
    if (read) return read;
    if (operation === "POST /api/v2/admin/sections/preview") {
      previews.push(args);
      return answer(args);
    }
    if (operation === "PATCH /api/v2/admin/sections/{id}") {
      rows = patchRow(rows, args);
      return rows.find((row) => row.id === args.path?.id);
    }
    throw new Error(`Unexpected ${operation}`);
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function setup(entry = "/admin/sections") {
  renderAdminHomeRows(entry);
  await waitForAddRow();
}

/** Moves the clock past the time a loaded peek stays fresh. */
function outlivePeekFreshness() {
  const later = Date.now() + PEEK_STALE_MS + 1000;
  vi.spyOn(Date, "now").mockReturnValue(later);
}

async function turnOffAndOn(title: string) {
  await userEvent.click(screen.getByRole("switch", { name: `${title} is on for everyone` }));
  await waitFor(() =>
    expect(
      screen.getByRole("switch", { name: `${title} is off for everyone` }),
    ).toBeInTheDocument(),
  );
  await userEvent.click(screen.getByRole("switch", { name: `${title} is off for everyone` }));
  await waitFor(() =>
    expect(screen.getByRole("switch", { name: `${title} is on for everyone` })).toBeInTheDocument(),
  );
  await waitFor(() => expect(screen.getByRole("button", { name: "Add row" })).toBeEnabled());
}

const posterSources = (title: string) =>
  [...rowLine(title).querySelectorAll("img")].map((img) => img.getAttribute("src"));

describe("Home rows poster peeks", () => {
  it("loads a row's first three titles once it is on screen, and none for off rows", async () => {
    await setup();
    expect(peekRequests()).toEqual([]);

    reveal("Recently Added", "Trending This Week", "Mind-Bending Sci-Fi");
    await waitFor(() => expect(peekRequests()).toHaveLength(2));
    expect(peekRequests().map((args) => args.body)).toEqual([
      { section_type: "recently_added", config: {}, item_limit: 3 },
      { section_type: "trending_on_server", config: { window: "7d" }, item_limit: 3 },
    ]);
    await waitFor(() =>
      expect(posterSources("Trending This Week")).toEqual([
        "https://img.test/past-lives.jpg",
        "https://img.test/oppenheimer.jpg",
      ]),
    );
    // A title without a poster shows its blurred placeholder instead.
    expect(
      rowLine("Trending This Week").querySelector('[style*="thumb=dune-hash"]'),
    ).not.toBeNull();
    expect(posterSources("Mind-Bending Sci-Fi")).toEqual([]);
  });

  it("asks for the library's titles on a library page", async () => {
    rows.push(stored("lib", { scope: "library", library_id: "7", title: "Movies added" }));
    await setup("/admin/sections?page=7");
    reveal("Movies added");
    await waitFor(() => expect(peekRequests()).toHaveLength(1));
    expect(peekRequests()[0]!.body).toMatchObject({ library_id: "7", item_limit: 3 });
  });

  it("does not reload a fresh peek when its row comes back", async () => {
    await setup();
    reveal("Recently Added", "Trending This Week");
    await waitFor(() => expect(posterSources("Recently Added")).toHaveLength(2));
    const before = peekRequests().length;

    await turnOffAndOn("Recently Added");
    reveal("Recently Added");
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(peekRequests()).toHaveLength(before);
    expect(posterSources("Recently Added")).toHaveLength(2);
  });

  it("reloads a stale peek once its returning row is on screen", async () => {
    await setup();
    reveal("Recently Added");
    await waitFor(() => expect(posterSources("Recently Added")).toHaveLength(2));
    expect(peekRequests()).toHaveLength(1);

    await turnOffAndOn("Recently Added");
    outlivePeekFreshness();
    // The cached posters show straight away; the reload waits for the screen.
    expect(posterSources("Recently Added")).toHaveLength(2);
    expect(peekRequests()).toHaveLength(1);

    reveal("Recently Added");
    await waitFor(() => expect(peekRequests()).toHaveLength(2));
  });

  it("shows the row's icon, and asks nothing, when the server has no previews", async () => {
    previewAvailable = false;
    await setup();
    reveal("Recently Added", "Trending This Week");
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(previews).toEqual([]);
    expect(posterSources("Recently Added")).toEqual([]);
  });
});
