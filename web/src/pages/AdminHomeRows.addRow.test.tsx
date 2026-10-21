import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import golden from "@/lib/homeRows/payloads.golden.json";
import { recipeCatalogFixture } from "@/lib/homeRows/recipeCatalogFixture.test-support";
import { V2ProblemError } from "@/api/v2/request";
import seasonalThemes from "../../../contracts/api/v2/fixtures/list_seasonal_themes_ok.json";
import {
  type Args,
  patchRow,
  readSections,
  renderAdminHomeRows,
  type Row,
  stored,
  stubResizeObserver,
  waitForAddRow,
  type Write,
} from "./adminHomeRows.test-support";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  error: vi.fn(),
  collections: [] as Array<Record<string, unknown>>,
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: mocks.error, warning: vi.fn() } }));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [{ id: 7, name: "Movies", type: "movies" }] }),
}));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollections: () => ({ data: mocks.collections }),
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
vi.mock("@/hooks/queries/personSearch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/personSearch")>()),
  useExtendedQueryRules: () => true,
}));
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

let rows: Row[];
let writes: Write[];
let previews: Args[];
let preview: boolean;
let holdCreate: Promise<void> | null;
let failNextPatch: boolean;

beforeEach(() => {
  vi.clearAllMocks();
  stubResizeObserver();
  rows = [
    stored("a", { position: 0 }),
    stored("b", {
      position: 4,
      title: "Trending This Week",
      section_type: "trending_on_server",
      config: { window: "7d" },
    }),
  ];
  writes = [];
  previews = [];
  preview = true;
  holdCreate = null;
  failNextPatch = false;
  mocks.collections = [
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
    {
      id: "secret",
      title: "Staff only",
      library_id: 7,
      library_ids: [7],
      collection_type: "smart",
      visibility: "hidden",
      item_count: 4,
      poster_url: "",
    },
  ];
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    args.onResponse?.(new Response(null, { headers: { ETag: '"rev-1"' } }));
    if (operation === "GET /api/v2/sections/seasonal-themes") return seasonalThemes;
    const read = readSections(operation, args, rows, { preview });
    if (read) return read;
    if (operation === "POST /api/v2/admin/sections/preview") {
      previews.push(args);
      return {
        items: [
          { content_id: "m1", type: "movie", title: "Past Lives", genres: [], keywords: [] },
          { content_id: "m2", type: "movie", title: "Dune: Part Two", genres: [], keywords: [] },
        ],
        total_count: 2,
      };
    }
    writes.push({ operation, args });
    if (operation === "POST /api/v2/admin/sections") {
      if (holdCreate) await holdCreate;
      const created = stored(`new-${writes.length}`, {
        ...(args.body as Partial<Row>),
        library_id: (args.body?.library_id as string | undefined) ?? null,
      });
      rows = [...rows, created];
      return created;
    }
    if (operation === "PATCH /api/v2/admin/sections/{id}") {
      if (failNextPatch) {
        failNextPatch = false;
        throw new V2ProblemError("updateAdminSection", {
          type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
          title: "precondition_failed",
          status: 412,
          detail: "Changed elsewhere",
        } as never);
      }
      rows = patchRow(rows, args);
      return rows.find((row) => row.id === args.path?.id);
    }
    throw new Error(`Unexpected ${operation}`);
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function setup(entry = "/admin/sections") {
  renderAdminHomeRows(entry);
  return waitForAddRow();
}

async function openAddRow() {
  await userEvent.click(await setup());
  return screen.findByRole("dialog", { name: "Add a row to Home" });
}

async function editRow(title: string) {
  await userEvent.click(await screen.findByRole("button", { name: `More for ${title}` }));
  await userEvent.click(await screen.findByRole("menuitem", { name: "Edit row…" }));
  return screen.findByRole("dialog", { name: "Edit row" });
}

const creates = () => writes.filter((write) => write.operation === "POST /api/v2/admin/sections");

describe("Add row", () => {
  it("adds a picked variant once, at the bottom, with the gallery's body", async () => {
    let release!: () => void;
    holdCreate = new Promise((resolve) => {
      release = resolve;
    });
    const dialog = await openAddRow();
    expect(within(dialog).getByText("New rows go to the bottom of Home")).toBeInTheDocument();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Trending on this server, 30 days" }),
    );
    const form = await screen.findByRole("dialog", { name: "Trending on this server" });
    expect(within(form).getByRole("radio", { name: /Last 30 days/ })).toBeChecked();
    expect(within(form).getByLabelText("Row name")).toHaveValue("Trending This Month");
    expect(
      within(form).getByText("Follows the time window until you type your own name."),
    ).toBeInTheDocument();

    // Two clicks in one tick, before React re-renders the button as disabled:
    // only the dialog's own guard can stop the second.
    const addButton = within(form).getByRole("button", { name: "Add row" });
    act(() => {
      addButton.click();
      addButton.click();
    });
    await waitFor(() => expect(creates()).toHaveLength(1));
    const { position, ...body } = creates()[0]!.args.body!;
    expect(JSON.parse(JSON.stringify(body))).toEqual(
      (golden.adminCreate as Record<string, unknown>)["trending_on_server/tr_30d/home"],
    );
    expect(position).toBe(5);
    await act(async () => release());
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(creates()).toHaveLength(1);
    await waitFor(() =>
      expect(
        screen.getAllByRole("listitem").find((item) => item.dataset.rowId === "new-1"),
      ).toHaveAttribute("data-highlighted", "true"),
    );
  });

  it("adds to the library page it is opened on and hides the Home-only library filter", async () => {
    rows.push(stored("lib", { scope: "library", library_id: "7", position: 2 }));
    await userEvent.click(await setup("/admin/sections?page=7"));
    const dialog = await screen.findByRole("dialog", { name: "Add a row to Movies" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Recently added" }));
    const form = await screen.findByRole("dialog", { name: "Recently added" });
    expect(within(form).getByText("Goes to the bottom of Movies")).toBeInTheDocument();
    expect(within(form).queryByText("From")).not.toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(creates()).toHaveLength(1));
    expect(creates()[0]!.args.body).toMatchObject({
      scope: "library",
      library_id: "7",
      section_type: "recently_added",
      position: 3,
    });
  });

  it("says when the server has no previews", async () => {
    preview = false;
    const dialog = await openAddRow();
    await userEvent.click(within(dialog).getByRole("button", { name: "Hidden gems" }));
    expect(
      await screen.findByText("Previews aren't available on this server."),
    ).toBeInTheDocument();
    expect(previews).toEqual([]);
  });

  it("adds a collection row with the gallery's create body, offering only visible collections", async () => {
    const dialog = await openAddRow();
    await userEvent.click(within(dialog).getByRole("button", { name: "A collection" }));
    const form = await screen.findByRole("dialog", { name: "A collection" });
    expect(
      within(form)
        .getAllByRole("radio")
        .map((radio) => radio.getAttribute("aria-label")),
    ).toEqual(["Studio Ghibli"]);
    // The server refuses a collection row preview without a collection.
    expect(within(form).getByText("Pick a collection to see its titles here.")).toBeInTheDocument();
    expect(previews).toEqual([]);
    await userEvent.click(within(form).getByRole("radio", { name: "Studio Ghibli" }));
    await waitFor(() => expect(previews).toHaveLength(1));
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(creates()).toHaveLength(1));
    expect(creates()[0]!.args.body).toEqual({
      ...(golden.adminCreate as Record<string, object>)["collection/picked/home"],
      position: 5,
    });
  });
});

describe("Edit row", () => {
  it("labels a weekly worldwide trending row by its week (#586)", async () => {
    rows[1] = {
      ...rows[1]!,
      title: "Popular now",
      section_type: "trending_discover",
      config: { source: "tmdb", window: "week" },
    };
    await setup();
    const dialog = await editRow("Popular now");
    expect(within(dialog).getByText("Trending worldwide · This week")).toBeInTheDocument();
    expect(within(dialog).getByRole("radio", { name: /This week/ })).toBeChecked();
    expect(within(dialog).getByRole("radio", { name: /Today/ })).not.toBeChecked();
  });

  it("turns a row into a rule row and keeps that through a 412 and Reload row", async () => {
    await setup();
    const dialog = await editRow("Trending This Week");
    fireEvent.change(within(dialog).getByLabelText("Row name"), { target: { value: "My picks" } });
    await userEvent.click(within(dialog).getByRole("button", { name: /More options/ }));
    await userEvent.click(within(dialog).getByRole("switch", { name: "Hero banner" }));
    await userEvent.click(
      within(dialog).getByRole("button", {
        name: "Change what Trending on this server · 7 days shows",
      }),
    );
    const picker = await screen.findByRole("dialog", { name: "Change what this row shows" });
    await userEvent.click(within(picker).getByRole("button", { name: "Titles matching rules" }));
    let form = await screen.findByRole("dialog", { name: "Edit row" });
    expect(within(form).getByRole("group", { name: "What the row shows" })).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Add rule" }));

    failNextPatch = true;
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await userEvent.click(await within(form).findByRole("button", { name: "Reload row" }));
    await waitFor(() =>
      expect(within(form).queryByRole("button", { name: "Reload row" })).toBeNull(),
    );
    form = screen.getByRole("dialog", { name: "Edit row" });
    expect(within(form).getByRole("group", { name: "What the row shows" })).toBeInTheDocument();
    expect(within(form).getByLabelText("Row name")).toHaveValue("My picks");
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(writes).toHaveLength(2));
    expect(writes[1]!.args.body).toMatchObject({
      section_type: "custom_filter",
      title: "My picks",
      featured: true,
      config: { groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "" }] }] },
    });
  });
});
