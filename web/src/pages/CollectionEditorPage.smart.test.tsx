/**
 * The collection editor page for Smart collections, both scopes: the Home
 * rows rule sentence with the libraries in it, a live poster preview of what
 * the rules match, an Order block that respects a stored default sort, and
 * the same save bar and layout as every other editor.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { listReturnState } from "@/lib/collections/listReturn";
import {
  adminCapabilities,
  adminCollectionList,
  adminSmartCollection,
  emptyPreview,
  personalCapabilities,
  personalSmartCollection,
} from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import CollectionEditorPage from "./CollectionEditorPage";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("@/hooks/useDebounce", () => ({ useDebounce: (value: string) => value }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner", name: "Sam" }, isLoading: false }),
}));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p-owner", name: "Sam" }] }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: LIBRARIES }),
}));
vi.mock("@/hooks/queries/libraries", async () => ({
  ...(await vi.importActual<typeof import("@/hooks/queries/libraries")>(
    "@/hooks/queries/libraries",
  )),
  useUserLibraries: () => ({ data: LIBRARIES }),
}));

const LIBRARIES = [
  { id: 1, name: "Movies", type: "movies" },
  { id: 2, name: "Kids", type: "movies" },
  { id: 3, name: "4K Movies", type: "movies" },
];

const COMEDY = { field: "genre", op: "is", value: "Comedy" };

function rules(overrides: Record<string, unknown> = {}) {
  return {
    library_ids: [1, 3],
    match: "all",
    groups: [{ match: "all", rules: [COMEDY] }],
    sort: { field: "rating_imdb", order: "desc" },
    ...overrides,
  };
}

function previewOf(total: number, titles: string[]) {
  return {
    items: titles.map((title, index) => ({
      content_id: `movie:${index}`,
      title,
      type: "movie",
      poster_url: `https://images.example/${index}.jpg`,
    })),
    page: { has_more: false },
    total,
  };
}

installV2Recorder();

beforeAll(() => {
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
});

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
  showServerSmart(rules());
  showPersonalSmart(rules({ library_ids: [] }));
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", adminCapabilities);
  v2Recorder.answer("GET /api/v2/collections/capabilities", personalCapabilities);
  v2Recorder.answer("POST /api/v2/admin/collections/preview", emptyPreview);
  v2Recorder.answer("POST /api/v2/collections/preview", emptyPreview);
});

describe("the Look card of a Smart collection", () => {
  it("a server Smart collection has no collage to fall back to", async () => {
    showPage("/admin/collections/c1/edit?libraryId=1");
    const look = await screen.findByRole("region", { name: "Look" });
    const toggle = await within(look).findByRole("button", { name: "Look" });
    expect(toggle).toHaveAccessibleDescription("Poster: none · Backdrop: none");
    fireEvent.click(toggle);
    const poster = within(look).getByRole("group", { name: "Poster" });
    expect(poster).toHaveTextContent("No poster");
    fireEvent.pointerDown(within(poster).getByRole("button", { name: "Change poster" }), {
      button: 0,
      ctrlKey: false,
      pointerType: "mouse",
    });
    const menu = await screen.findByRole("menu");
    expect(within(menu).getByRole("menuitem", { name: "Remove poster" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  it("a personal Smart collection gets a collage of its first matches", async () => {
    showPage("/collections/c1/edit");
    const look = await screen.findByRole("region", { name: "Look" });
    expect(await within(look).findByRole("button", { name: "Look" })).toHaveAccessibleDescription(
      "Poster: a collage once its titles have posters",
    );
  });
});

/** The server collection `c1` is Smart with these rules (and this sort_config). */
function showServerSmart(
  query: Record<string, unknown>,
  sortConfig: Record<string, unknown> = {},
  fields: Record<string, unknown> = {},
) {
  const smart = {
    ...adminSmartCollection(query),
    library_ids: (query.library_ids as number[]).map(String),
    sort_config: sortConfig,
    ...fields,
  };
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", smart);
  v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(smart));
}

/** The personal collection `c1` is Smart with these rules. */
function showPersonalSmart(query: Record<string, unknown>) {
  const smart = personalSmartCollection(query);
  v2Recorder.answer("GET /api/v2/collections/{id}", smart);
  v2Recorder.answer("GET /api/v2/collections", { items: [smart] });
}

function showPage(url: string | { pathname: string; search?: string; state?: unknown }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const router = createMemoryRouter(
    [
      { path: "/admin/collections", element: <p>Admin collections page</p> },
      {
        element: <CollectionEditorPage scope="server" />,
        children: [{ path: "/admin/collections/new" }, { path: "/admin/collections/:id/edit" }],
      },
      { path: "/collections", element: <p>Collections page</p> },
      {
        element: <CollectionEditorPage scope="personal" />,
        children: [{ path: "/collections/new" }, { path: "/collections/:id/edit" }],
      },
      { path: "/catalog", element: <p>Collection page</p> },
    ],
    { initialEntries: [url] },
  );
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

const writes = () => v2Recorder.writes();
const nameField = () => screen.findByRole("textbox", { name: "Name" });
const sentence = () => screen.findByRole("group", { name: "What the collection shows" });
const bar = () => screen.getByRole("region", { name: "Unsaved changes" });

function previews(scope: "server" | "personal") {
  return v2Recorder.callsOf(
    scope === "server"
      ? "POST /api/v2/admin/collections/preview"
      : "POST /api/v2/collections/preview",
  );
}

async function rename(name = "Renamed") {
  fireEvent.change(await nameField(), { target: { value: name } });
}

async function save(count: number) {
  fireEvent.click(within(bar()).getByRole("button", { name: "Save" }));
  await vi.waitFor(() => expect(writes()).toHaveLength(count));
}

function patchBody(index = 0) {
  return writes().filter((call) => call.operation.startsWith("PATCH"))[index]!.body as {
    query_definition: { groups: Array<{ rules: unknown[] }>; library_ids: number[] };
    sort_config: Record<string, unknown>;
  };
}

/** The sentence's libraries control, by the summary it shows. */
function libraries(summary: string) {
  return new RegExp(`^Libraries:\\s*${summary}$`);
}

/** Opens a Radix select and picks an option. */
function choose(combobox: HTMLElement, option: string) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
  fireEvent.click(screen.getByRole("option", { name: option }));
}

function optionsOf(combobox: HTMLElement) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
  const names = screen.getAllByRole("option").map((option) => option.textContent);
  fireEvent.keyDown(screen.getByRole("listbox"), { key: "Escape" });
  return names;
}

describe("the rules", () => {
  it("server: reads as the Home rows sentence with its libraries, and has no rule-rows note", async () => {
    showServerSmart(rules(), {}, { item_count: 40 });
    showPage("/admin/collections/c1/edit");
    const words = within(await sentence());
    expect(words.getByText("from the")).toBeTruthy();
    expect(words.getByRole("button", { name: libraries("Movies, 4K Movies") })).toBeTruthy();
    expect(words.getByText("libraries")).toBeTruthy();
    expect(screen.getByText("Smart")).toBeTruthy();
    const meta = screen.getByText(
      (_, element) =>
        element?.tagName === "P" &&
        /Updates itself as titles are added$/.test(element.textContent ?? ""),
    );
    expect(meta.textContent?.replace(/\s+/g, " ")).toMatch(
      /^Movies, 4K Movies\W+40 titles\W+Updates itself as titles are added$/,
    );
    expect(screen.queryByText(/rule rows/i)).toBeNull();
  });

  it("personal: an empty library list reads as all my libraries, with no rule-rows note", async () => {
    showPage("/collections/c1/edit");
    const words = within(await sentence());
    expect(words.getByRole("button", { name: libraries("all my libraries") })).toBeTruthy();
    expect(screen.queryByText(/rule rows/i)).toBeNull();
  });

  it("offers rules about the viewer only on a personal collection", async () => {
    showPage("/collections/c1/edit");
    await sentence();
    expect(optionsOf(screen.getByRole("combobox", { name: "Field" }))).toContain("Watched");
  });

  it("does not offer rules about the viewer on a server collection", async () => {
    showPage("/admin/collections/c1/edit");
    await sentence();
    expect(optionsOf(screen.getByRole("combobox", { name: "Field" }))).not.toContain("Watched");
  });

  it("keeps a rule it can't show as a locked line, and saves it unchanged", async () => {
    const watched = { field: "watched", op: "is", value: true };
    showServerSmart(rules({ groups: [{ match: "all", rules: [COMEDY, watched] }] }));
    showPage("/admin/collections/c1/edit");
    expect(await screen.findByRole("group", { name: "Rule not editable here" })).toBeTruthy();
    await rename();
    await save(1);
    expect(patchBody().query_definition.groups[0]!.rules).toEqual([COMEDY, watched]);
  });

  it("names Rules in the save bar and says the preview already shows them", async () => {
    showPage("/admin/collections/c1/edit");
    await sentence();
    const max = screen.getByRole("spinbutton", { name: "Max titles" });
    fireEvent.change(max, { target: { value: "50" } });
    fireEvent.blur(max);
    expect(within(bar()).getByRole("status")).toHaveTextContent("Rules not saved");
    expect(bar()).toHaveTextContent("The preview already shows them.");
    await save(1);
    expect(patchBody().query_definition).toMatchObject({ limit: 50 });
  });

  it("server: can't save once every library is unticked, and says why", async () => {
    const user = userEvent.setup();
    showPage("/admin/collections/c1/edit");
    await sentence();
    await user.click(screen.getByRole("button", { name: libraries("Movies, 4K Movies") }));
    await user.click(screen.getByRole("menuitemcheckbox", { name: "Movies" }));
    await user.click(screen.getByRole("menuitemcheckbox", { name: "4K Movies" }));
    await user.keyboard("{Escape}");
    expect(within(bar()).getByRole("button", { name: "Save" })).toBeDisabled();
    expect(bar()).toHaveTextContent("Pick at least one library.");
    const pane = screen.getByText(/Live preview/).closest("section")!;
    expect(pane).toHaveTextContent("From every library until you pick some");
    fireEvent.click(within(bar()).getByRole("button", { name: "Save" }));
    expect(writes()).toEqual([]);
  });

  it("moves focus to the libraries in the sentence from Where it shows", async () => {
    showPage("/admin/collections/c1/edit");
    await sentence();
    const where = screen.getByRole("region", { name: "Where it shows" });
    const row = within(where).getByRole("group", { name: "Libraries" });
    expect(row).toHaveTextContent("Movies, 4K Movies");
    fireEvent.click(within(row).getByRole("button", { name: "Change libraries" }));
    expect(document.activeElement).toBe(
      screen.getByRole("button", { name: libraries("Movies, 4K Movies") }),
    );
  });
});

describe("the live preview", () => {
  it("previews the full rules across every chosen library, with posters", async () => {
    v2Recorder.answer(
      "POST /api/v2/admin/collections/preview",
      previewOf(40, ["Clueless", "Babe"]),
    );
    showPage("/admin/collections/c1/edit");
    expect(await screen.findByText("40 titles match")).toBeTruthy();
    expect(screen.getByText("showing 2")).toBeTruthy();
    const request = previews("server").at(-1)!.body as {
      query_definition: Record<string, unknown>;
      limit: number;
    };
    expect(request.limit).toBe(24);
    expect(request.query_definition).toMatchObject({
      library_ids: [1, 3],
      groups: [{ match: "all", rules: [COMEDY] }],
      sort: { field: "rating_imdb", order: "desc" },
    });
    const posters = within(screen.getByRole("list", { name: "Matching titles" }));
    expect(posters.getByText("Clueless")).toBeTruthy();
    const list = screen.getByRole("list", { name: "Matching titles" });
    expect([...list.querySelectorAll("img")].map((img) => img.getAttribute("src"))).toEqual([
      "https://images.example/0.jpg",
      "https://images.example/1.jpg",
    ]);
    expect(screen.getByText("40 titles match").closest("[aria-live]")).toHaveAttribute(
      "aria-live",
      "polite",
    );
  });

  it("follows the rules as they change", async () => {
    showPage("/collections/c1/edit");
    await sentence();
    const before = previews("personal").length;
    choose(screen.getByRole("combobox", { name: "Kind of titles" }), "shows");
    await vi.waitFor(() => expect(previews("personal").length).toBeGreaterThan(before));
    expect(previews("personal").at(-1)!.body).toMatchObject({
      query_definition: { media_scope: "series", library_ids: [] },
    });
  });

  it("with no matches shows ghost posters and still saves", async () => {
    showPage("/admin/collections/c1/edit");
    expect(await screen.findByText("No titles match yet")).toBeTruthy();
    expect(screen.getByText(/You can still save/)).toBeTruthy();
    expect(screen.getByText("0 titles match")).toBeTruthy();
    // The count is read out once; the empty message is not a second live region.
    expect(
      screen.getByText("No titles match yet").closest('[role="status"], [aria-live]'),
    ).toBeNull();
    await rename();
    await save(1);
  });
});

describe("Order", () => {
  const stored = { field: "title", order: "asc", mode: "manual_pins" };

  it("sends a stored default sort back untouched", async () => {
    showServerSmart(rules(), stored);
    showPage("/admin/collections/c1/edit");
    expect(
      await screen.findByText("A saved default sort, Title A–Z, wins over this Order."),
    ).toBeTruthy();
    await rename();
    await save(1);
    expect(patchBody().sort_config).toEqual(stored);
  });

  it("clears the stored default sort when Order changes, and keeps its other settings", async () => {
    showServerSmart(rules(), stored);
    showPage("/admin/collections/c1/edit");
    await sentence();
    choose(screen.getByRole("combobox", { name: "Direction" }), "Lowest first");
    expect(screen.queryByText(/A saved default sort/)).toBeNull();
    await save(1);
    expect(patchBody().sort_config).toEqual({ mode: "manual_pins" });
    expect(patchBody().query_definition).toMatchObject({
      sort: { field: "rating_imdb", order: "asc" },
    });
  });

  it("clears the stored default sort with Clear", async () => {
    showServerSmart(rules(), stored);
    showPage("/admin/collections/c1/edit");
    fireEvent.click(
      await screen.findByRole("button", { name: "Clear the saved default sort, Title A–Z" }),
    );
    expect(within(bar()).getByRole("status")).toHaveTextContent("Order not saved");
    await save(1);
    expect(patchBody().sort_config).toEqual({ mode: "manual_pins" });
  });
});

describe("creating a Smart collection", () => {
  it("stays on the editor at the new collection's edit URL", async () => {
    const router = showPage("/collections/new?type=smart");
    expect((await screen.findAllByText("Not created yet")).length).toBeGreaterThan(0);
    await rename("Comfort");
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/collections/c1/edit"));
    expect(await sentence()).toBeTruthy();
    await vi.waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole("button", { name: libraries("all my libraries") }),
      ),
    );
    expect(writes()[0]!.body).toMatchObject({
      name: "Comfort",
      collection_type: "smart",
      query_definition: { library_ids: [] },
    });
  });

  it("server: waits for a library before it can be created, but previews every library meanwhile", async () => {
    v2Recorder.answer(
      "POST /api/v2/admin/collections/preview",
      previewOf(3, ["Alien", "Avatar", "Titanic"]),
    );
    showPage("/admin/collections/new?type=smart");
    await rename("Staff picks");
    expect(screen.getByRole("button", { name: "Create collection" })).toBeDisabled();
    expect(screen.getAllByText("Pick its libraries, then create it.").length).toBeGreaterThan(0);
    const pane = screen.getByText(/Live preview/).closest("section")!;
    expect(await within(pane).findByText("Avatar")).toBeInTheDocument();
    expect(pane).toHaveTextContent("From every library until you pick some");
    const request = previews("server").at(-1)!.body as {
      query_definition: { library_ids: number[] };
    };
    expect(request.query_definition.library_ids).toEqual([]);
  });
});

describe("who can open the editor", () => {
  it("sends someone who can't change a personal Smart collection to its page", async () => {
    v2Recorder.answer("GET /api/v2/collections/{id}", {
      ...personalSmartCollection(rules()),
      creator_profile_id: "p-other",
    });
    const router = showPage("/collections/c1/edit");
    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/catalog"));
    expect(router.state.location.search).toContain("notice=read-only");
    expect(writes()).toEqual([]);
  });
});

describe("Back", () => {
  it("server: goes back to the list view the editor was opened from", async () => {
    showPage({
      pathname: "/admin/collections/c1/edit",
      search: "?libraryId=1",
      state: listReturnState("/admin/collections?view=list&libraryId=1&type=smart"),
    });
    expect(await screen.findByRole("link", { name: "Collections" })).toHaveAttribute(
      "href",
      "/admin/collections?view=list&libraryId=1&type=smart",
    );
  });

  it("server: still goes back to the list view it was opened from after Create", async () => {
    const view = "/admin/collections?view=list&libraryId=1&type=smart&q=fox";
    const router = showPage({
      pathname: "/admin/collections/new",
      search: "?type=smart&libraryId=1",
      state: listReturnState(view),
    });
    await rename("Fox classics");
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await vi.waitFor(() =>
      expect(router.state.location.pathname).toBe("/admin/collections/c1/edit"),
    );
    // Let the page render at its new address before reading the link.
    await act(async () => {});
    expect(screen.getByRole("link", { name: "Collections" })).toHaveAttribute("href", view);
  });

  it("server: goes back to the library's List when it wasn't opened from the list", async () => {
    showPage("/admin/collections/c1/edit?libraryId=1");
    expect(await screen.findByRole("link", { name: "Collections" })).toHaveAttribute(
      "href",
      "/admin/collections?libraryId=1&view=list",
    );
  });
});

/** `names` sorted into the order their regions come on the page. */
function inPageOrder(names: readonly string[]) {
  return names
    .map((name) => [name, screen.getByRole("region", { name })] as const)
    .sort(([, a], [, b]) =>
      a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1,
    )
    .map(([name]) => name);
}

describe("layout", () => {
  it("orders the smart editor sections with the preview under the rules", async () => {
    showPage("/admin/collections/c1/edit");
    await sentence();
    const order = ["Name and description", "Rules", "Live preview", "Where it shows", "Look"];
    expect(inPageOrder([...order].reverse())).toEqual(order);
  });
});

describe("the save bar before Create", () => {
  it("server: says what's still missing, name then libraries, then how many titles match", async () => {
    v2Recorder.answer("POST /api/v2/admin/collections/preview", previewOf(38, ["Alien"]));
    const user = userEvent.setup();
    showPage("/admin/collections/new?type=smart");
    const create = await screen.findByRole("button", { name: "Create collection" });
    const status = () => within(bar()).getByRole("status");
    expect(status()).toHaveTextContent("Not created yet Name it, then create it.");
    await rename("Fox classics");
    expect(status()).toHaveTextContent("Not created yet Pick its libraries, then create it.");
    expect(create).toBeDisabled();
    const where = screen.getByRole("region", { name: "Where it shows" });
    const row = within(where).getByRole("group", { name: "Libraries" });
    expect(row).toHaveTextContent("Not picked yet");
    await user.click(within(row).getByRole("button", { name: "Change libraries" }));
    const control = document.activeElement as HTMLElement;
    expect(control).toHaveAccessibleName(/^Libraries:/);
    await user.click(control);
    await user.click(screen.getByRole("menuitemcheckbox", { name: "Movies" }));
    await user.keyboard("{Escape}");
    expect(row).toHaveTextContent("Movies");
    await vi.waitFor(() =>
      expect(status()).toHaveTextContent(
        "Not created yet 38 titles match now. New ones join on their own.",
      ),
    );
    expect(create).toBeEnabled();
  });

  it("personal: goes from the name straight to how many titles match", async () => {
    v2Recorder.answer("POST /api/v2/collections/preview", previewOf(1, ["Alien"]));
    showPage("/collections/new?type=smart");
    await screen.findByRole("button", { name: "Create collection" });
    const status = () => within(bar()).getByRole("status");
    expect(status()).toHaveTextContent("Not created yet Name it, then create it.");
    await rename("Comfort");
    await vi.waitFor(() =>
      expect(status()).toHaveTextContent(
        "Not created yet 1 title matches now. New ones join on their own.",
      ),
    );
  });
});
