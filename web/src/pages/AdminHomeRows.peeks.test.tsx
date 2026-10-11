import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PEEK_STALE_MS } from "@/components/calm/usePeekLimiter";
import { recipeCatalogFixture } from "@/lib/homeRows/recipeCatalogFixture.test-support";
import AdminHomeRows from "./AdminHomeRows";

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
  body?: Record<string, unknown>;
  onResponse?: (response: Response) => void;
};
type PreviewItem = {
  content_id: string;
  title: string;
  poster_url?: string;
  poster_thumbhash?: string;
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
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
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
    const scope = args.query?.scope ?? "home";
    const here = rows.filter((row) => row.scope === scope);
    if (operation === "GET /api/v2/admin/sections/capabilities")
      return { available: true, reset_profiles: false, preview: previewAvailable };
    if (operation === "GET /api/v2/admin/sections/order")
      return {
        scope,
        library_id: args.query?.library_id ?? null,
        ordered_ids: here.map((row) => row.id),
      };
    if (operation === "GET /api/v2/admin/sections")
      return { items: here.map((row) => ({ ...row })) };
    if (operation === "GET /api/v2/admin/sections/{id}")
      return { ...rows.find((row) => row.id === args.path?.id)! };
    if (operation === "POST /api/v2/admin/sections/preview") {
      previews.push(args);
      return answer(args);
    }
    if (operation === "PATCH /api/v2/admin/sections/{id}") {
      const changes = Object.fromEntries(
        Object.entries(args.body ?? {}).filter(([, value]) => value !== undefined),
      );
      rows = rows.map((row) => (row.id === args.path?.id ? { ...row, ...changes } : row));
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

const newClient = () =>
  new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });

async function setup(entry = "/admin/sections", client = newClient()) {
  const view = render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={client}>
        <AdminHomeRows />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  const add = await screen.findByRole("button", { name: "Add row" });
  await waitFor(() => expect(add).toBeEnabled());
  return { client, unmount: view.unmount };
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

  it("shows the blurred placeholder when a poster fails to load", async () => {
    answer = async () => ({
      items: [
        {
          content_id: "m1",
          title: "Past Lives",
          poster_url: "https://img.test/expired.jpg",
          poster_thumbhash: "past-hash",
        },
      ],
      total_count: 1,
    });
    await setup();
    reveal("Recently Added");
    await waitFor(() => expect(posterSources("Recently Added")).toHaveLength(1));
    fireEvent.error(rowLine("Recently Added").querySelector("img")!);
    expect(posterSources("Recently Added")).toEqual([]);
    expect(rowLine("Recently Added").querySelector('[style*="thumb=past-hash"]')).not.toBeNull();
  });

  it("leaves rows that have not reached the screen alone", async () => {
    await setup();
    reveal("Trending This Week");
    await waitFor(() => expect(peekRequests()).toHaveLength(1));
    expect(peekRequests()[0]!.body?.section_type).toBe("trending_on_server");
    expect(posterSources("Recently Added")).toEqual([]);
  });

  it("asks for the library's titles on a library page", async () => {
    rows.push(stored("lib", { scope: "library", library_id: "7", title: "Movies added" }));
    await setup("/admin/sections?page=7");
    reveal("Movies added");
    await waitFor(() => expect(peekRequests()).toHaveLength(1));
    expect(peekRequests()[0]!.body).toMatchObject({ library_id: "7", item_limit: 3 });
  });

  it("never has more than four peeks loading at once", async () => {
    rows = Array.from({ length: 7 }, (_, index) =>
      stored(`r${index}`, { position: index, title: `Row ${index}` }),
    );
    const held: Array<() => void> = [];
    answer = () =>
      new Promise((resolve) => held.push(() => resolve({ items: POSTERS, total_count: 3 })));
    await setup();
    reveal(...rows.map((row) => row.title));
    await waitFor(() => expect(peekRequests()).toHaveLength(4));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(peekRequests()).toHaveLength(4);

    await act(async () => held.shift()!());
    await waitFor(() => expect(peekRequests()).toHaveLength(5));
    while (held.length > 0) await act(async () => held.shift()!());
    await waitFor(() => expect(peekRequests()).toHaveLength(7));
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

  it("reloads stale peeks when the admin comes back to the page", async () => {
    const { client, unmount } = await setup();
    reveal("Recently Added");
    await waitFor(() => expect(posterSources("Recently Added")).toHaveLength(2));
    unmount();

    await setup("/admin/sections", client);
    reveal("Recently Added");
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(peekRequests()).toHaveLength(1);
    expect(posterSources("Recently Added")).toHaveLength(2);
    unmount();
    cleanup();

    outlivePeekFreshness();
    await setup("/admin/sections", client);
    reveal("Recently Added");
    await waitFor(() => expect(peekRequests()).toHaveLength(2));
  });

  it("reloads a row's peek after an edit changes what it shows", async () => {
    await setup();
    reveal("Trending This Week");
    await waitFor(() => expect(peekRequests()).toHaveLength(1));

    await userEvent.click(screen.getByRole("button", { name: "More for Trending This Week" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Edit row…" }));
    const dialog = await screen.findByRole("dialog", { name: "Edit row" });
    await userEvent.click(within(dialog).getByRole("radio", { name: /Last 30 days/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    reveal("Trending This Month");
    await waitFor(() => expect(peekRequests()).toHaveLength(2));
    expect(peekRequests()[1]!.body?.config).toEqual({ window: "30d" });
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

  it("falls back to the row's icon when a peek fails or finds nothing", async () => {
    answer = async (args) => {
      if (args.body?.section_type === "recently_added") throw new Error("boom");
      return { items: [], total_count: 0 };
    };
    await setup();
    reveal("Recently Added", "Trending This Week");
    await waitFor(() => expect(peekRequests()).toHaveLength(2));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    for (const title of ["Recently Added", "Trending This Week"]) {
      expect(posterSources(title)).toEqual([]);
      expect(rowLine(title).querySelector("[style*=background-image]")).toBeNull();
    }
  });
});

describe("Add row live preview", () => {
  it("shows the posters the preview sends, with each title", async () => {
    await setup();
    await userEvent.click(screen.getByRole("button", { name: "Add row" }));
    const picker = await screen.findByRole("dialog", { name: "Add a row to Home" });
    await userEvent.click(within(picker).getByRole("button", { name: "Hidden gems" }));
    const form = await screen.findByRole("dialog", { name: "Hidden gems" });
    const strip = await within(form).findByRole("region", { name: /^Preview of/ });
    await waitFor(() =>
      expect([...strip.querySelectorAll("img")].map((img) => img.getAttribute("src"))).toEqual([
        "https://img.test/past-lives.jpg",
        "https://img.test/oppenheimer.jpg",
      ]),
    );
    expect(within(strip).getByText("Dune: Part Two")).toBeInTheDocument();
    expect(strip.querySelector('[style*="thumb=dune-hash"]')).not.toBeNull();
  });
});
