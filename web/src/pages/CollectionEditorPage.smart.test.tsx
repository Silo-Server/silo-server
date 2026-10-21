/**
 * The collection editor page for Smart collections, both scopes: the Home
 * rows rule sentence with the libraries in it, a live poster preview of what
 * the rules match, an Order block that respects a stored default sort, and
 * the same save bar and layout as every other editor.
 */
import { act, fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { listReturnState } from "@/lib/collections/listReturn";
import {
  adminCollectionList,
  adminSmartCollection,
  emptyPreview,
  personalSmartCollection,
} from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import {
  choose,
  nameField,
  prepareEditorPage,
  saveBar,
  showEditorPage,
} from "./collectionEditorPage.test-support";

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

beforeEach(() => {
  prepareEditorPage();
  showServerSmart(rules());
  showPersonalSmart(rules({ library_ids: [] }));
  v2Recorder.answer("POST /api/v2/admin/collections/preview", emptyPreview);
  v2Recorder.answer("POST /api/v2/collections/preview", emptyPreview);
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

const writes = () => v2Recorder.writes();
const sentence = () => screen.findByRole("group", { name: "What the collection shows" });

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
  fireEvent.click(within(saveBar()).getByRole("button", { name: "Save" }));
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

function optionsOf(combobox: HTMLElement) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
  const names = screen.getAllByRole("option").map((option) => option.textContent);
  fireEvent.keyDown(screen.getByRole("listbox"), { key: "Escape" });
  return names;
}

describe("the rules", () => {
  it("offers rules about the viewer only on a personal collection", async () => {
    showEditorPage("/collections/c1/edit");
    await sentence();
    expect(optionsOf(screen.getByRole("combobox", { name: "Field" }))).toContain("Watched");
  });

  it("does not offer rules about the viewer on a server collection", async () => {
    showEditorPage("/admin/collections/c1/edit");
    await sentence();
    expect(optionsOf(screen.getByRole("combobox", { name: "Field" }))).not.toContain("Watched");
  });

  it("keeps a rule it can't show as a locked line, and saves it unchanged", async () => {
    const watched = { field: "watched", op: "is", value: true };
    showServerSmart(rules({ groups: [{ match: "all", rules: [COMEDY, watched] }] }));
    showEditorPage("/admin/collections/c1/edit");
    expect(await screen.findByRole("group", { name: "Rule not editable here" })).toBeTruthy();
    await rename();
    await save(1);
    expect(patchBody().query_definition.groups[0]!.rules).toEqual([COMEDY, watched]);
  });

  it("server: can't save once every library is unticked, and says why", async () => {
    const user = userEvent.setup();
    showEditorPage("/admin/collections/c1/edit");
    await sentence();
    await user.click(screen.getByRole("button", { name: libraries("Movies, 4K Movies") }));
    await user.click(screen.getByRole("menuitemcheckbox", { name: "Movies" }));
    await user.click(screen.getByRole("menuitemcheckbox", { name: "4K Movies" }));
    await user.keyboard("{Escape}");
    expect(within(saveBar()).getByRole("button", { name: "Save" })).toBeDisabled();
    expect(saveBar()).toHaveTextContent("Pick at least one library.");
    const pane = screen.getByText(/Live preview/).closest("section")!;
    expect(pane).toHaveTextContent("From every library until you pick some");
    fireEvent.click(within(saveBar()).getByRole("button", { name: "Save" }));
    expect(writes()).toEqual([]);
  });
});

describe("the live preview", () => {
  it("previews the full rules across every chosen library, with posters", async () => {
    v2Recorder.answer(
      "POST /api/v2/admin/collections/preview",
      previewOf(40, ["Clueless", "Babe"]),
    );
    showEditorPage("/admin/collections/c1/edit");
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
    showEditorPage("/collections/c1/edit");
    await sentence();
    const before = previews("personal").length;
    choose(screen.getByRole("combobox", { name: "Kind of titles" }), "shows");
    await vi.waitFor(() => expect(previews("personal").length).toBeGreaterThan(before));
    expect(previews("personal").at(-1)!.body).toMatchObject({
      query_definition: { media_scope: "series", library_ids: [] },
    });
  });
});

describe("Order", () => {
  const stored = { field: "title", order: "asc", mode: "manual_pins" };

  it("sends a stored default sort back untouched", async () => {
    showServerSmart(rules(), stored);
    showEditorPage("/admin/collections/c1/edit");
    expect(
      await screen.findByText("A saved default sort, Title A–Z, wins over this Order."),
    ).toBeTruthy();
    await rename();
    await save(1);
    expect(patchBody().sort_config).toEqual(stored);
  });

  it("clears the stored default sort when Order changes, and keeps its other settings", async () => {
    showServerSmart(rules(), stored);
    showEditorPage("/admin/collections/c1/edit");
    await sentence();
    choose(screen.getByRole("combobox", { name: "Direction" }), "Lowest first");
    expect(screen.queryByText(/A saved default sort/)).toBeNull();
    await save(1);
    expect(patchBody().sort_config).toEqual({ mode: "manual_pins" });
    expect(patchBody().query_definition).toMatchObject({
      sort: { field: "rating_imdb", order: "asc" },
    });
  });
});

describe("creating a Smart collection", () => {
  it("server: waits for a library before it can be created, but previews every library meanwhile", async () => {
    v2Recorder.answer(
      "POST /api/v2/admin/collections/preview",
      previewOf(3, ["Alien", "Avatar", "Titanic"]),
    );
    showEditorPage("/admin/collections/new?type=smart");
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

describe("Back", () => {
  it("server: still goes back to the list view it was opened from after Create", async () => {
    const view = "/admin/collections?view=list&libraryId=1&type=smart&q=fox";
    const router = showEditorPage({
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
});

describe("the save bar before Create", () => {
  it("server: says what's still missing, name then libraries, then how many titles match", async () => {
    v2Recorder.answer("POST /api/v2/admin/collections/preview", previewOf(38, ["Alien"]));
    const user = userEvent.setup();
    showEditorPage("/admin/collections/new?type=smart");
    const create = await screen.findByRole("button", { name: "Create collection" });
    const status = () => within(saveBar()).getByRole("status");
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
});
