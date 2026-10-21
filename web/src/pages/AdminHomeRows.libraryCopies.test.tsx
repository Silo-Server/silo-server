import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import golden from "@/lib/homeRows/payloads.golden.json";
import { recipeCatalogFixture } from "@/lib/homeRows/recipeCatalogFixture.test-support";
import {
  type Args,
  readSections,
  renderAdminHomeRows,
  type Row,
  stored,
  stubResizeObserver,
  waitForAddRow,
  type Write,
} from "./adminHomeRows.test-support";

const THREE_LIBRARIES = [
  { id: 7, name: "Movies", type: "movies" },
  { id: 8, name: "TV Shows", type: "shows" },
  { id: 9, name: "Kids", type: "movies" },
];

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  error: vi.fn(),
  success: vi.fn(),
  libraries: [] as Array<{ id: number; name: string; type: string }>,
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({
  toast: { success: mocks.success, error: mocks.error, warning: vi.fn() },
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: mocks.libraries }),
}));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollections: () => ({
    data: [
      {
        id: "lib-1",
        title: "Studio Ghibli",
        library_id: 7,
        library_ids: [7],
        collection_type: "manual",
        visibility: "visible",
        item_count: 23,
        poster_url: "",
      },
    ],
  }),
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
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

/** A row on the Movies page (library 7). */
const onMovies = (id: string, overrides: Partial<Row> = {}) =>
  stored(id, { scope: "library", library_id: "7", ...overrides });

let rows: Row[];
let writes: Write[];
let holdBulk: Promise<void> | null;
let nextId: number;

beforeEach(() => {
  vi.clearAllMocks();
  mocks.libraries = THREE_LIBRARIES;
  stubResizeObserver();
  rows = [
    stored("home-row", { title: "Trending on Home" }),
    onMovies("trend", {
      position: 0,
      title: "Trending This Week",
      section_type: "trending_on_server",
      config: { window: "7d" },
    }),
    onMovies("ghibli", {
      position: 1,
      title: "Studio Ghibli",
      section_type: "collection",
      config: { library_collection_id: "lib-1" },
    }),
    onMovies("hero", {
      position: 2,
      title: "Hidden Gems",
      section_type: "hidden_gems",
      featured: true,
      config: {},
    }),
  ];
  writes = [];
  holdBulk = null;
  nextId = 0;
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    args.onResponse?.(new Response(null, { headers: { ETag: '"rev-1"' } }));
    const read = readSections(operation, args, rows);
    if (read) return read;
    writes.push({ operation, args });
    if (operation === "POST /api/v2/admin/sections/bulk") {
      if (holdBulk) await holdBulk;
      const { library_ids: libraryIds, ...fields } = args.body as Record<string, unknown>;
      for (const libraryId of libraryIds as string[]) {
        rows.push(
          onMovies(`copy-${++nextId}`, {
            ...(fields as Partial<Row>),
            library_id: libraryId,
            position: 9,
          }),
        );
      }
      return { created: (libraryIds as string[]).length };
    }
    if (operation === "POST /api/v2/admin/sections") {
      const created = onMovies(`new-${++nextId}`, args.body as Partial<Row>);
      rows.push(created);
      return created;
    }
    throw new Error(`Unexpected ${operation}`);
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function setup(entry = "/admin/sections?page=7") {
  renderAdminHomeRows(entry);
  return waitForAddRow();
}

async function pickTrending(entry?: string) {
  await userEvent.click(await setup(entry));
  const picker = await screen.findByRole("dialog");
  await userEvent.click(
    within(picker).getByRole("button", { name: "Trending on this server, 7 days" }),
  );
  return screen.findByRole("dialog", { name: "Trending on this server" });
}

async function openRowMenu(title: string) {
  await userEvent.click(await screen.findByRole("button", { name: `More for ${title}` }));
  return screen.findByRole("menu");
}

const pagesGroup = (form: HTMLElement) =>
  within(form).getByRole("group", { name: "Add to these library pages" });

/** This page (Movies, 7) and `others` more movie libraries, 100 and up. */
function manyLibraries(others: number) {
  return [
    { id: 7, name: "Movies", type: "movies" },
    ...Array.from({ length: others }, (_, index) => ({
      id: 100 + index,
      name: `Library ${100 + index}`,
      type: "movies",
    })),
  ];
}

const LIMIT_NOTE = "You can add a row to up to 100 pages at once.";

describe("Add to these library pages", () => {
  it("keeps this page on, counts it, and adds every copy in one request", async () => {
    let release!: () => void;
    holdBulk = new Promise((resolve) => {
      release = resolve;
    });
    const form = await pickTrending();
    const group = pagesGroup(form);
    const here = within(group).getByRole("checkbox", { name: /Movies/ });
    expect(here).toBeChecked();
    expect(here).toHaveAttribute("aria-disabled", "true");
    expect(here).toHaveAccessibleName("Movies (this page)");
    // Keyboard users still reach it and hear that this page is included.
    here.focus();
    expect(here).toHaveFocus();
    await userEvent.keyboard(" ");
    await userEvent.click(here);
    expect(here).toBeChecked();
    expect(
      within(form).getByText(
        "Each page gets its own copy, so you can change or remove it there later.",
      ),
    ).toBeInTheDocument();

    await userEvent.click(within(group).getByRole("checkbox", { name: "Kids" }));
    expect(within(group).getByRole("checkbox", { name: "Kids" })).toBeChecked();
    expect(within(form).getByText("Goes to the bottom of each page")).toBeInTheDocument();

    const addButton = within(form).getByRole("button", { name: "Add to 2 pages" });
    act(() => {
      addButton.click();
      addButton.click();
    });
    await waitFor(() => expect(writes).toHaveLength(1));
    const { library_ids: libraryIds, ...body } = writes[0]!.args.body!;
    expect(writes[0]!.operation).toBe("POST /api/v2/admin/sections/bulk");
    expect(libraryIds).toEqual(["7", "9"]);
    const { library_id: _library, ...single } = (
      golden.adminCreate as Record<string, Record<string, unknown>>
    )["trending_on_server/tr_7d/library"]!;
    expect(JSON.parse(JSON.stringify(body))).toEqual(single);

    await act(async () => release());
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(writes).toHaveLength(1);
    await waitFor(() =>
      expect(
        screen.getAllByRole("listitem").find((item) => item.dataset.rowId === "copy-1"),
      ).toHaveAttribute("data-highlighted", "true"),
    );
  });

  it("stops at the 100 pages one request can add to, this page included", async () => {
    mocks.libraries = manyLibraries(100);
    const form = await pickTrending();
    const group = pagesGroup(form);
    // getByLabelText instead of getByRole: role queries over 100+ checkboxes
    // compute every accessible name each time and push this test past CI's timeout.
    for (let id = 100; id < 199; id++)
      fireEvent.click(within(group).getByLabelText(`Library ${id}`));
    expect(within(group).getByRole("checkbox", { name: "Library 199" })).toBeDisabled();
    expect(within(form).getByText(LIMIT_NOTE)).toBeInTheDocument();
    // Dropping one page frees a place again.
    fireEvent.click(within(group).getByRole("checkbox", { name: "Library 100" }));
    expect(within(group).getByRole("checkbox", { name: "Library 199" })).toBeEnabled();
    expect(within(form).queryByText(LIMIT_NOTE)).toBeNull();
    fireEvent.click(within(group).getByRole("checkbox", { name: "Library 100" }));

    await userEvent.click(within(form).getByRole("button", { name: "Add to 100 pages" }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]!.args.body!.library_ids).toHaveLength(100);
  }, 30_000);
});

describe("Add to other libraries…", () => {
  it("adds copies to the other pages only and leaves this page as it was", async () => {
    await setup();
    const menu = await openRowMenu("Trending This Week");
    await userEvent.click(within(menu).getByRole("menuitem", { name: "Add to other libraries…" }));
    const dialog = await screen.findByRole("dialog", {
      name: "Add Trending This Week to other libraries",
    });
    const group = within(dialog).getByRole("group", { name: "Add to these library pages" });
    const here = within(group).getByRole("checkbox", { name: "Movies (already here)" });
    expect(here).toBeChecked();
    expect(here).toHaveAttribute("aria-disabled", "true");
    here.focus();
    expect(here).toHaveFocus();
    await userEvent.keyboard(" ");
    expect(here).toBeChecked();
    expect(within(dialog).getByRole("button", { name: "Add to pages" })).toBeDisabled();

    await userEvent.click(within(group).getByRole("checkbox", { name: "TV Shows" }));
    await userEvent.click(within(group).getByRole("checkbox", { name: "Kids" }));
    const confirm = within(dialog).getByRole("button", { name: "Add to 2 pages" });
    act(() => {
      confirm.click();
      confirm.click();
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(writes).toHaveLength(1);
    expect(writes[0]!.args.body).toEqual({
      scope: "library",
      library_ids: ["8", "9"],
      section_type: "trending_on_server",
      title: "Trending This Week",
      item_limit: 20,
      featured: false,
      enabled: true,
      config: { window: "7d" },
    });
    expect(mocks.success).toHaveBeenCalledWith("Added to TV Shows and Kids.");
    const list = await screen.findByRole("list", { name: /rows/i });
    expect(within(list).getAllByRole("listitem")).toHaveLength(3);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "More for Trending This Week" })).toHaveFocus(),
    );
  });
});
