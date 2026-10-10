/**
 * The collection editor page for Manual collections, both scopes: titles save
 * as they are added, everything else waits for the save bar, and a save keeps
 * a current token by reading the collection again after every title write.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useSyncExternalStore } from "react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  LIBRARIES_NOT_PICKED,
  PERSONAL_TAB_HELP,
  ROWS_ONCE_CREATED,
  serverTabHelp,
  SHELF_AFTER_CREATE,
  SHOW_ON_TAB_LABEL,
  SHOW_TO_OTHER_PROFILES_HELP,
  SHOW_TO_OTHER_PROFILES_LABEL,
  unshareWarning,
} from "@/lib/collections/copy";
import {
  artworkTile,
  chooseArtwork,
  openArtworkMenu,
  uploadArtwork,
} from "@/test/collectionArtwork";
import {
  adminCapabilities,
  adminCollection,
  adminCollectionList,
  personalCapabilities,
} from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";
import getCollectionOk from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import CollectionEditorPage from "./CollectionEditorPage";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("@/hooks/useDebounce", () => ({ useDebounce: (value: string) => value }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

/** The acting profile and the account's profiles, changeable mid-test. */
const account = vi.hoisted(() => {
  const listeners = new Set<() => void>();
  return {
    state: {
      profile: { id: "p-owner", name: "Sam" } as { id: string; name: string } | null,
      loading: false,
      profiles: [{ id: "p-owner", name: "Sam" }],
    },
    set(next: Partial<typeof account.state>) {
      account.state = { ...account.state, ...next };
      for (const listener of listeners) listener();
    },
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
});
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => {
    const state = useSyncExternalStore(account.subscribe, () => account.state);
    return { profile: state.profile, isLoading: state.loading };
  },
}));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: account.state.profiles }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: LIBRARIES }),
}));

const LIBRARIES = [
  { id: 1, name: "Movies", type: "movies" },
  { id: 2, name: "Kids", type: "movies" },
  { id: 3, name: "4K Movies", type: "movies" },
  { id: 4, name: "Classics", type: "movies" },
  { id: 5, name: "Anime", type: "movies" },
];

interface Title {
  content_id: string;
  title: string;
  year: number;
}
const HEAT = { content_id: "movie:heat-1995", title: "Heat", year: 1995 };
const ALIEN = { content_id: "movie:alien-1979", title: "Alien", year: 1979 };
const TOTORO = { content_id: "movie:totoro-1988", title: "My Neighbor Totoro", year: 1988 };

installV2Recorder();

/** What the personal and admin item routes answer, per collection. */
let members: string[];
/** What the viewer-scoped title search answers, per library ("all" without one). */
let searchable: Record<string, Title[]>;

function catalogItem(title: Title) {
  return { ...title, type: "movie", genres: [], keywords: [], status: "matched" };
}

function itemsAnswer() {
  return {
    items: members.map((id, position) => ({
      collection_id: "c1",
      media_item_id: id,
      position,
      added_at: "2026-01-02T03:04:05.000Z",
    })),
    page: { has_more: false },
  };
}

function catalogAnswer(call: RecordedCall) {
  const body = call.body as { collection_id?: string; library_id?: string };
  const all = [HEAT, ALIEN, TOTORO];
  const titles = body.collection_id
    ? members.map((id) => all.find((title) => title.content_id === id)!)
    : (searchable[body.library_id ?? "all"] ?? []);
  return {
    items: titles.map(catalogItem),
    page: { has_more: false },
    total: titles.length,
    total_exact: true,
    effective_sort: { field: "relevance", order: "desc" },
  };
}

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
  HTMLElement.prototype.scrollIntoView = () => {};
  HTMLElement.prototype.hasPointerCapture = () => false;
  account.set({
    profile: { id: "p-owner", name: "Sam" },
    loading: false,
    profiles: [{ id: "p-owner", name: "Sam" }],
  });
  members = [HEAT.content_id];
  searchable = { all: [HEAT, ALIEN], "1": [HEAT, ALIEN] };
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", adminCapabilities);
  v2Recorder.answer("GET /api/v2/collections/capabilities", personalCapabilities);
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", adminCollection);
  v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(adminCollection));
  v2Recorder.answer("GET /api/v2/collections", { items: [getCollectionOk] });
  v2Recorder.answer("GET /api/v2/collections/{id}", getCollectionOk);
  v2Recorder.answer("GET /api/v2/collections/{id}/items", itemsAnswer);
  v2Recorder.answer("GET /api/v2/admin/collections/{id}/items", itemsAnswer);
  const order = () => ({ ordered_ids: [...members], has_more: false });
  v2Recorder.answer("GET /api/v2/collections/{id}/items/order", order);
  v2Recorder.answer("GET /api/v2/admin/collections/{id}/items/order", order);
  v2Recorder.answer("POST /api/v2/catalog/query", catalogAnswer);
  const put = (call: RecordedCall) => {
    const id = call.path.split("/").pop()!;
    if (!members.includes(id)) members.push(id);
    // A title write moves the order's token too, as the server does.
    v2Recorder.bump(call.path.replace(/\/items\/[^/]+$/, "/items/order"));
  };
  v2Recorder.answer("PUT /api/v2/collections/{id}/items/{item_id}", put);
  v2Recorder.answer("PUT /api/v2/admin/collections/{id}/items/{item_id}", put);
  v2Recorder.answer("DELETE /api/v2/collections/{id}/items/{item_id}", (call: RecordedCall) => {
    members = members.filter((id) => id !== call.path.split("/").pop());
  });
  v2Recorder.answer("PUT /api/v2/collections/{id}/items/order", (call: RecordedCall) => {
    members = (call.body as { ordered_ids: string[] }).ordered_ids;
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function showPage(url: string) {
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
const search = () => screen.findByRole("combobox", { name: /Add a title/ });
const bar = () => screen.getByRole("region", { name: "Unsaved changes" });

async function addTitle(query: string, title: string) {
  fireEvent.change(await search(), { target: { value: query } });
  const option = await screen.findByRole("option", { name: new RegExp(`^${title}.*Add$`) });
  fireEvent.click(option);
}

async function save() {
  fireEvent.click(within(bar()).getByRole("button", { name: /^(Save|Try again)$/ }));
}

function patches() {
  return writes().filter((call) => call.operation.startsWith("PATCH"));
}

describe("titles save as you change them", () => {
  it("sends the rename's PATCH with the token the title add left behind", async () => {
    showPage("/collections/c1/edit");
    await addTitle("alien", "Alien");
    await screen.findByText("Saved");
    // The tick sits by the heading, so the panel and its list are still just "Titles".
    expect(screen.getByRole("region", { name: "Titles" })).toBeTruthy();
    expect(screen.getByRole("list", { name: "Titles" })).toBeTruthy();
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    await save();
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.headers["If-Match"]).toBe('"/api/v2/collections/c1#2"');
    await waitFor(() =>
      expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull(),
    );
  });

  it("puts a removed title back at its position with Undo", async () => {
    members = [HEAT.content_id, ALIEN.content_id];
    showPage("/collections/c1/edit");
    // The first title, so putting it back at the end would be a different position.
    fireEvent.click(await screen.findByRole("button", { name: "Remove Heat" }));
    const toast = await screen.findByText("Removed Heat");
    fireEvent.click(within(toast.closest("[role=status]")!).getByRole("button", { name: "Undo" }));
    await waitFor(() =>
      expect(writes().map((call) => [call.operation, call.body])).toEqual([
        ["DELETE /api/v2/collections/{id}/items/{item_id}", undefined],
        ["PUT /api/v2/collections/{id}/items/{item_id}", { position: 0 }],
      ]),
    );
    expect(writes()[1]!.path).toBe("/api/v2/collections/c1/items/movie:heat-1995");
  });

  it("drags titles with the token read after the last title write, then saves the rename", async () => {
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      const row = this.closest("li[data-title-id]");
      const index = row ? Array.from(row.parentElement!.children).indexOf(row) : 0;
      const top = row ? index * 60 : 0;
      const height = row ? 60 : 0;
      return {
        x: 0,
        y: top,
        top,
        left: 0,
        right: 800,
        bottom: top + height,
        width: 800,
        height,
        toJSON: () => ({}),
      } as DOMRect;
    });
    showPage("/collections/c1/edit");
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    await addTitle("alien", "Alien");
    const grip = await screen.findByRole("button", { name: "Move Alien" });
    await waitFor(() =>
      expect(v2Recorder.callsOf("GET /api/v2/collections/{id}/items/order").length).toBeGreaterThan(
        1,
      ),
    );
    grip.focus();
    fireEvent.keyDown(grip, { code: "Space", key: " " });
    await screen.findByText(/Picked up Alien/);
    fireEvent.keyDown(grip, { code: "ArrowUp", key: "ArrowUp" });
    await screen.findByText(/Alien is now at position 1 of 2/);
    fireEvent.keyDown(grip, { code: "Space", key: " " });
    await waitFor(() =>
      expect(v2Recorder.callsOf("PUT /api/v2/collections/{id}/items/order")).toHaveLength(1),
    );
    const [reorder] = v2Recorder.callsOf("PUT /api/v2/collections/{id}/items/order");
    // The add moved the order's token; the drag sends the one read after it.
    expect(reorder!.headers["If-Match"]).toBe('"/api/v2/collections/c1/items/order#2"');
    expect(reorder!.body).toEqual({ ordered_ids: [ALIEN.content_id, HEAT.content_id] });
    await waitFor(() => expect(screen.getByText("Name not saved")).toBeTruthy());
    await save();
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.body).toMatchObject({ name: "Renamed" });
    await waitFor(() =>
      expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull(),
    );
  });
});

describe("creating a Manual collection", () => {
  beforeEach(() => {
    members = [];
  });

  it("adds staged titles in order after the POST and stays on the page without asking", async () => {
    const router = showPage("/collections/new?type=manual");
    await addTitle("heat", "Heat");
    await addTitle("alien", "Alien");
    // The name is what's still missing; once it's there, the bar counts the titles.
    expect(within(bar()).getByRole("status")).toHaveTextContent("Name it, then create it.");
    expect(writes()).toEqual([]);
    const name = await nameField();
    fireEvent.change(name, { target: { value: "Rainy days" } });
    expect(within(bar()).getByRole("status")).toHaveTextContent("2 titles ready to add");
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/collections/c1/edit"));
    expect(writes().map((call) => [call.operation, call.path, call.body])).toEqual([
      [
        "POST /api/v2/collections",
        "/api/v2/collections",
        expect.objectContaining({ name: "Rainy days", collection_type: "manual" }),
      ],
      [
        "PUT /api/v2/collections/{id}/items/{item_id}",
        "/api/v2/collections/c1/items/movie:heat-1995",
        { position: 0 },
      ],
      [
        "PUT /api/v2/collections/{id}/items/{item_id}",
        "/api/v2/collections/c1/items/movie:alien-1979",
        { position: 1 },
      ],
    ]);
    // The same page carries on: no discard prompt, the same Name field, focus in the title search.
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(await nameField()).toBe(name);
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("combobox", { name: /Add a title/ })),
    );
    expect(v2Recorder.callsOf("GET /api/v2/collections/{id}").length).toBeLessThanOrEqual(1);
  });

  it("starts a fresh editor when New collection is opened again after a create", async () => {
    const router = showPage("/collections/new?type=manual");
    fireEvent.change(await nameField(), { target: { value: "Rainy days" } });
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/collections/c1/edit"));
    await act(() => router.navigate("/collections/new?type=manual"));
    expect(await nameField()).toHaveValue("");
    expect(screen.getByRole("button", { name: "Create collection" })).toBeDisabled();
    await act(() => router.navigate("/collections/c1/edit"));
    expect(await nameField()).toHaveValue("Rainy days");
    expect(screen.queryByRole("button", { name: "Create collection" })).toBeNull();
  });

  it("moves to the edit page when the new collection can't be read back, and reads it before the next save", async () => {
    let unreadable = true;
    v2Recorder.answer("GET /api/v2/collections/{id}", () => {
      if (!unreadable) return getCollectionOk;
      unreadable = false;
      throw new Error("Network error");
    });
    const router = showPage("/collections/new?type=manual");
    await addTitle("heat", "Heat");
    fireEvent.change(await nameField(), { target: { value: "Rainy days" } });
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/collections/c1/edit"));
    expect(screen.queryByRole("button", { name: "Create collection" })).toBeNull();
    // Heat was added: nothing is left staged, and nothing waits for Save.
    expect(screen.queryByText(/Created, but couldn't add/)).toBeNull();
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull();
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    await save();
    await waitFor(() => expect(patches()).toHaveLength(1));
    expect(patches()[0]!.headers["If-Match"]).toBe('"/api/v2/collections/c1#2"');
    expect(patches()[0]!.body).toMatchObject({ name: "Renamed" });
    await waitFor(() =>
      expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull(),
    );
  });

  it("keeps titles that couldn't be added marked, and tries them again", async () => {
    let refuse = true;
    v2Recorder.answer("PUT /api/v2/collections/{id}/items/{item_id}", (call: RecordedCall) => {
      const id = call.path.split("/").pop()!;
      if (refuse && id === ALIEN.content_id) throw new Error("Not available");
      if (!members.includes(id)) members.push(id);
    });
    const router = showPage("/collections/new?type=manual");
    await addTitle("heat", "Heat");
    await addTitle("alien", "Alien");
    fireEvent.change(await nameField(), { target: { value: "Rainy days" } });
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/collections/c1/edit"));
    const notice = await screen.findByText("Created, but couldn't add 1 title");
    expect(screen.getByText("Couldn't add")).toBeTruthy();
    refuse = false;
    fireEvent.click(within(notice.parentElement!).getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(screen.queryByText("Created, but couldn't add 1 title")).toBeNull());
    const adds = v2Recorder.callsOf("PUT /api/v2/collections/{id}/items/{item_id}");
    expect(adds.map((call) => [call.path.split("/").pop(), call.body])).toEqual([
      [HEAT.content_id, { position: 0 }],
      [ALIEN.content_id, { position: 1 }],
      [ALIEN.content_id, { position: 1 }],
    ]);
  });

  it("keeps a poster that failed after create staged, with Retry", async () => {
    let fail = true;
    v2Recorder.answer("PUT /api/v2/admin/collections/{id}/poster", () => {
      if (fail) throw new Error("too large");
      return adminCollection;
    });
    showPage("/admin/collections/new?type=manual&libraryId=1");
    fireEvent.change(await nameField(), { target: { value: "Staff picks" } });
    await uploadArtwork("poster");
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    const problem = await screen.findByText(/Couldn't save the poster/);
    expect(within(bar()).getByText("Poster not saved")).toBeTruthy();
    fail = false;
    fireEvent.click(within(problem).getByRole("button", { name: "Retry" }));
    await waitFor(() =>
      expect(writes().map((call) => call.operation)).toEqual([
        "POST /api/v2/admin/collections",
        "PUT /api/v2/admin/collections/{id}/poster",
        "PATCH /api/v2/admin/collections/{id}",
        "PUT /api/v2/admin/collections/{id}/poster",
      ]),
    );
    await waitFor(() => expect(screen.queryByText(/Couldn't save the poster/)).toBeNull());
  });

  it("creates a server collection unpinned", async () => {
    showPage("/admin/collections/new?type=manual&libraryId=1");
    fireEvent.change(await nameField(), { target: { value: "Staff picks" } });
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0]!.body).toMatchObject({ featured: false, library_ids: ["1"] });
  });

  it("can't be created until it has a name", async () => {
    showPage("/collections/new?type=manual");
    expect(await screen.findByRole("button", { name: "Create collection" })).toBeDisabled();
    expect(within(bar()).getByRole("status")).toHaveTextContent(
      "Not created yet Name it, then create it.",
    );
    expect(screen.getByRole("region", { name: "Titles" })).toBeTruthy();
    expect(document.title).toMatch(/^New collection · /);
  });

  it("says a server collection needs a library before it can be created", async () => {
    showPage("/admin/collections/new?type=manual");
    fireEvent.change(await nameField(), { target: { value: "Staff picks" } });
    expect(screen.getByRole("button", { name: "Create collection" })).toBeDisabled();
    expect(within(bar()).getByRole("status")).toHaveTextContent(
      "Not created yet Pick its libraries, then create it.",
    );
  });

  it("names the page after the collection once it is created", async () => {
    const router = showPage("/collections/new?type=manual");
    fireEvent.change(await nameField(), { target: { value: "Rainy days" } });
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/collections/c1/edit"));
    await waitFor(() => expect(document.title).toMatch(/^Edit Rainy days · /));
  });
});

describe("saving when the collection changed elsewhere", () => {
  function changedElsewhere(fields: Partial<typeof adminCollection>) {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", { ...adminCollection, ...fields });
    v2Recorder.bump("/api/v2/admin/collections/c1");
  }

  it("takes the other change and saves again once, with no banner", async () => {
    showPage("/admin/collections/c1/edit?libraryId=1");
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    changedElsewhere({ description: "Written elsewhere" });
    await save();
    await waitFor(() => expect(patches()).toHaveLength(2));
    expect(patches()[0]!.headers["If-Match"]).toBe('"/api/v2/admin/collections/c1#1"');
    expect(patches()[1]!.headers["If-Match"]).toBe('"/api/v2/admin/collections/c1#2"');
    expect(patches()[1]!.body).toMatchObject({
      title: "Renamed",
      description: "Written elsewhere",
    });
    expect(screen.queryByText("This collection changed since you opened it.")).toBeNull();
  });

  it("asks about a field both sides changed: Keep mine saves mine", async () => {
    showPage("/admin/collections/c1/edit?libraryId=1");
    fireEvent.change(await nameField(), { target: { value: "Mine" } });
    changedElsewhere({ title: "Theirs" });
    await save();
    const banner = await screen.findByRole("alert", { name: "" });
    expect(banner).toHaveTextContent("This collection changed since you opened it.");
    expect(banner).toHaveTextContent("Name changed in both places.");
    expect(patches()).toHaveLength(1);
    fireEvent.click(within(banner).getByRole("button", { name: "Keep mine" }));
    await save();
    await waitFor(() => expect(patches()).toHaveLength(2));
    expect(patches()[1]!.body).toMatchObject({ title: "Mine" });
    expect(patches()[1]!.headers["If-Match"]).toBe('"/api/v2/admin/collections/c1#2"');
  });

  it("asks about a field both sides changed: Use theirs drops mine", async () => {
    showPage("/admin/collections/c1/edit?libraryId=1");
    fireEvent.change(await nameField(), { target: { value: "Mine" } });
    changedElsewhere({ title: "Theirs" });
    await save();
    const banner = await screen.findByText("This collection changed since you opened it.");
    fireEvent.click(
      within(banner.closest("[role=alert]") as HTMLElement).getByRole("button", {
        name: "Use theirs",
      }),
    );
    expect(await nameField()).toHaveValue("Theirs");
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull();
    expect(patches()).toHaveLength(1);
  });
});

describe("who can open the editor", () => {
  it("sends someone who can't change the collection to its page with a notice", async () => {
    v2Recorder.answer("GET /api/v2/collections/{id}", {
      ...getCollectionOk,
      creator_profile_id: "p-other",
      is_shared: true,
    });
    const router = showPage("/collections/c1/edit");
    await screen.findByText("Collection page");
    expect(router.state.location.pathname).toBe("/catalog");
    const params = new URLSearchParams(router.state.location.search);
    expect(params.get("notice")).toBe("read-only");
    expect(params.get("collection_id")).toBe("c1");
    expect(screen.queryByRole("textbox", { name: "Name" })).toBeNull();
  });

  it("waits for the acting profile before deciding", async () => {
    account.set({ profile: null, loading: true });
    const router = showPage("/collections/c1/edit");
    await screen.findByText("Loading collection editor…");
    await act(async () => {});
    expect(router.state.location.pathname).toBe("/collections/c1/edit");
    act(() => account.set({ profile: { id: "p-owner", name: "Sam" }, loading: false }));
    expect(await nameField()).toHaveValue("Rainy days");
  });
});

describe("the save bar and Where it shows", () => {
  it("names the fields that wait for Save and says titles are already saved", async () => {
    showPage("/collections/c1/edit");
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    fireEvent.change(screen.getByRole("textbox", { name: /Description/ }), {
      target: { value: "For grey afternoons" },
    });
    const region = bar();
    expect(within(region).getByRole("status")).toHaveTextContent("Name and description not saved");
    expect(within(region).getByRole("status")).toHaveTextContent("Titles are already saved.");
    fireEvent.click(within(region).getByRole("button", { name: "Discard" }));
    expect(await nameField()).toHaveValue("Rainy days");
  });

  it("server: shows the Collections tab switch with where viewers find it", async () => {
    showPage("/admin/collections/c1/edit?libraryId=1");
    const toggle = await screen.findByRole("switch", { name: SHOW_ON_TAB_LABEL });
    expect(toggle).toHaveAccessibleDescription(serverTabHelp(["Movies"]));
    expect(toggle).toBeChecked();
    expect(screen.queryByRole("switch", { name: SHOW_TO_OTHER_PROFILES_LABEL })).toBeNull();
  });

  it("server: names each library's shelf, and says a pinned collection leads it", async () => {
    const shelf = (id: string, name: string, default_sort_mode: string) => ({
      id,
      library_id: "1",
      name,
      slug: id,
      kind: "regular",
      default_sort_mode,
      sort_order: 0,
    });
    v2Recorder.answer("GET /api/v2/admin/collections", {
      ...adminCollectionList({ ...adminCollection, featured: true, group_id: "studios" }),
      groups: [shelf("studios", "Studios", "manual")],
    });
    showPage("/admin/collections/c1/edit?libraryId=1");
    expect(await screen.findByText(/Movies › Studios, pinned first/)).toBeInTheDocument();
  });

  it("server: leaves out pinned first on a shelf that sorts itself", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections", {
      ...adminCollectionList({ ...adminCollection, featured: true, group_id: "awards" }),
      groups: [
        {
          id: "awards",
          library_id: "1",
          name: "Awards",
          slug: "awards",
          kind: "regular",
          default_sort_mode: "name_asc",
          sort_order: 0,
        },
      ],
    });
    showPage("/admin/collections/c1/edit?libraryId=1");
    expect(await screen.findByText(/Movies › Awards/)).toBeInTheDocument();
    expect(screen.queryByText(/pinned first/)).toBeNull();
  });

  it("personal, one profile: only the Collections tab switch, for you and anyone you share with", async () => {
    showPage("/collections/c1/edit");
    const toggle = await screen.findByRole("switch", { name: SHOW_ON_TAB_LABEL });
    expect(toggle).toHaveAccessibleDescription(PERSONAL_TAB_HELP);
    expect(screen.queryByRole("switch", { name: SHOW_TO_OTHER_PROFILES_LABEL })).toBeNull();
  });

  it("personal, several profiles: one sharing switch, and a warning before unsharing", async () => {
    account.set({
      profiles: [
        { id: "p-owner", name: "Sam" },
        { id: "p-maya", name: "Maya" },
        { id: "p-leo", name: "Leo" },
      ],
    });
    v2Recorder.answer("GET /api/v2/collections/{id}", { ...getCollectionOk, is_shared: true });
    showPage("/collections/c1/edit");
    const sharing = await screen.findAllByRole("switch", { name: SHOW_TO_OTHER_PROFILES_LABEL });
    expect(sharing).toHaveLength(1);
    // The same switch, and words, as every other place a personal collection is shared.
    expect(sharing[0]).toHaveAccessibleDescription(SHOW_TO_OTHER_PROFILES_HELP);
    expect(SHOW_TO_OTHER_PROFILES_HELP).toContain("Nobody else on the server can see it.");
    fireEvent.click(sharing[0]!);
    expect(await screen.findByText(unshareWarning(["Maya", "Leo"]))).toBeTruthy();
    expect(within(bar()).getByRole("status")).toHaveTextContent(
      `${SHOW_TO_OTHER_PROFILES_LABEL} not saved`,
    );
  });
});

describe("the Look card", () => {
  const SERVER_EDIT = "/admin/collections/c1/edit?libraryId=1";
  const look = () => screen.getByRole("region", { name: "Look" });

  it("starts closed as one line saying what each image is, and opens to the tiles", async () => {
    showPage(SERVER_EDIT);
    const toggle = await within(await screen.findByRole("region", { name: "Look" })).findByRole(
      "button",
      { name: "Look" },
    );
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    // No poster yet: the collage comes once the server has titles' posters to make it from.
    expect(toggle).toHaveAccessibleDescription(
      "Poster: a collage once its titles have posters · Backdrop: none",
    );
    expect(within(look()).queryByRole("group", { name: "Poster" })).toBeNull();
    fireEvent.click(toggle);
    const done = within(look()).getByRole("button", { name: "Done" });
    expect(done).toHaveAttribute("aria-expanded", "true");
    await waitFor(() => expect(document.activeElement).toBe(done));
    expect(within(look()).getByRole("group", { name: "Poster" })).toHaveTextContent(
      "A collage once its titles have posters",
    );
    expect(within(look()).getByRole("group", { name: "Backdrop" })).toHaveTextContent(
      "Fills the top of its page",
    );
    // One control per image: no link field until Paste a link… is chosen.
    expect(within(look()).queryByRole("textbox")).toBeNull();
    fireEvent.click(done);
    const closed = within(look()).getByRole("button", { name: "Look" });
    await waitFor(() => expect(document.activeElement).toBe(closed));
  });

  it("shows the collage the server made in the header and the Look card, as a collage", async () => {
    const collage = "https://images.example/collage.webp";
    v2Recorder.answer(
      "GET /api/v2/admin/collections",
      adminCollectionList({ ...adminCollection, poster_url: collage, poster_is_collage: true }),
    );
    showPage(SERVER_EDIT);
    const toggle = await within(await screen.findByRole("region", { name: "Look" })).findByRole(
      "button",
      { name: "Look" },
    );
    expect(toggle).toHaveAccessibleDescription("Poster: a collage of its titles · Backdrop: none");
    expect(document.querySelector(`header img[src="${collage}"]`)).not.toBeNull();
    const tile = await artworkTile("poster");
    expect(within(tile).getByRole("img", { name: "Poster preview" })).toHaveAttribute(
      "src",
      collage,
    );
    expect(tile).toHaveTextContent("Collage of its titles");
    // The collage is already in use: there's no image to remove.
    const menu = await openArtworkMenu("poster");
    expect(within(menu).getByRole("menuitem", { name: "Use the collage" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  it("shows a personal collection's collage the same way", async () => {
    const collage = "https://images.example/mine.webp";
    v2Recorder.answer("GET /api/v2/collections", {
      items: [{ ...getCollectionOk, poster_url: collage, poster_is_collage: true }],
    });
    showPage("/collections/c1/edit");
    const toggle = await within(await screen.findByRole("region", { name: "Look" })).findByRole(
      "button",
      { name: "Look" },
    );
    expect(toggle).toHaveAccessibleDescription("Poster: a collage of its titles");
    expect(document.querySelector(`header img[src="${collage}"]`)).not.toBeNull();
    expect(
      within(await artworkTile("poster")).getByRole("img", { name: "Poster preview" }),
    ).toHaveAttribute("src", collage);
  });

  it("sums up a saved image, and a new one waiting for Save", async () => {
    const withPoster = { ...adminCollection, poster_url: "https://images.example/poster.png" };
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", withPoster);
    v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(withPoster));
    showPage(SERVER_EDIT);
    const toggle = await within(await screen.findByRole("region", { name: "Look" })).findByRole(
      "button",
      { name: "Look" },
    );
    expect(toggle).toHaveAccessibleDescription("Poster: an image · Backdrop: none");
    await uploadArtwork("backdrop");
    expect(within(look()).getByText("The new backdrop saves when you press Save.")).toBeTruthy();
    fireEvent.click(within(look()).getByRole("button", { name: "Done" }));
    expect(within(look()).getByRole("button", { name: "Look" })).toHaveAccessibleDescription(
      "Poster: an image · Backdrop: a new image",
    );
    expect(within(bar()).getByRole("status")).toHaveTextContent("Backdrop not saved");
  });

  it("Upload image… opens the file picker for that image", async () => {
    const click = vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(() => {});
    showPage(SERVER_EDIT);
    const menu = await openArtworkMenu("poster");
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((item) => item.textContent),
    ).toEqual(["Upload image…", "Paste a link…", "Use the collage"]);
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Upload image…" }));
    expect(click).toHaveBeenCalledTimes(1);
    expect(click.mock.contexts[0]).toBe(
      within(await artworkTile("poster")).getByLabelText("Upload poster"),
    );
    click.mockRestore();
  });

  it("Paste a link… shows the link field; Use link stages it and Save sends it", async () => {
    v2Recorder.answer("PUT /api/v2/admin/collections/{id}/poster", adminCollection);
    showPage(SERVER_EDIT);
    await chooseArtwork("poster", "Paste a link…");
    const field = await within(look()).findByRole("textbox", { name: "Poster image link" });
    await waitFor(() => expect(document.activeElement).toBe(field));
    expect(writes()).toEqual([]);
    fireEvent.change(field, { target: { value: "  https://images.example/fox.jpg " } });
    fireEvent.click(within(look()).getByRole("button", { name: "Use link" }));
    expect(within(look()).queryByRole("textbox")).toBeNull();
    expect(within(look()).getByRole("img", { name: "Poster preview" })).toHaveAttribute(
      "src",
      "https://images.example/fox.jpg",
    );
    expect(within(bar()).getByRole("status")).toHaveTextContent("Poster not saved");
    expect(writes()).toEqual([]);
    await save();
    await waitFor(() =>
      expect(writes().map((call) => call.operation)).toEqual([
        "PATCH /api/v2/admin/collections/{id}",
        "PUT /api/v2/admin/collections/{id}/poster",
      ]),
    );
    expect(writes()[1]!.form).toEqual({ source_url: "https://images.example/fox.jpg" });
  });

  it("Cancel closes the link field without staging anything", async () => {
    showPage(SERVER_EDIT);
    await chooseArtwork("backdrop", "Paste a link…");
    const field = await within(look()).findByRole("textbox", { name: "Backdrop image link" });
    fireEvent.change(field, { target: { value: "https://images.example/wide.jpg" } });
    fireEvent.click(within(look()).getByRole("button", { name: "Cancel" }));
    expect(within(look()).queryByRole("textbox")).toBeNull();
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull();
    await waitFor(() =>
      expect(document.activeElement).toBe(
        within(look()).getByRole("button", { name: "Change backdrop" }),
      ),
    );
  });

  it("Use the collage and Remove backdrop wait for an image to remove", async () => {
    showPage(SERVER_EDIT);
    const poster = await openArtworkMenu("poster");
    expect(within(poster).getByRole("menuitem", { name: "Use the collage" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    fireEvent.keyDown(poster, { key: "Escape" });
    const backdrop = await openArtworkMenu("backdrop");
    expect(within(backdrop).getByRole("menuitem", { name: "Remove backdrop" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  it("Use the collage drops a new poster that isn't saved yet, sending nothing", async () => {
    showPage(SERVER_EDIT);
    await uploadArtwork("poster");
    expect(within(bar()).getByRole("status")).toHaveTextContent("Poster not saved");
    await chooseArtwork("poster", "Use the collage");
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull();
    expect(writes()).toEqual([]);
  });

  it("opens by itself when an image couldn't be saved, with Retry", async () => {
    v2Recorder.answer("PUT /api/v2/admin/collections/{id}/poster", () => {
      throw new Error("too large");
    });
    showPage(SERVER_EDIT);
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    await uploadArtwork("poster");
    fireEvent.click(within(look()).getByRole("button", { name: "Done" }));
    await save();
    expect(await within(look()).findByText(/Couldn't save the poster/)).toBeTruthy();
    expect(within(look()).getByRole("button", { name: "Retry" })).toBeTruthy();
  });
});

describe("Where it shows on a new server collection", () => {
  it("lists Libraries, Shelf, the Collections tab and Rows that show it, one line each", async () => {
    showPage("/admin/collections/new?type=manual");
    await nameField();
    const where = screen.getByRole("region", { name: "Where it shows" });
    const libraries = within(where).getByRole("group", { name: "Libraries" });
    expect(libraries).toHaveTextContent(LIBRARIES_NOT_PICKED);
    expect(within(where).getByRole("group", { name: "Shelf" })).toHaveTextContent(
      `No heading · ${SHELF_AFTER_CREATE}`,
    );
    expect(within(where).getByRole("switch", { name: SHOW_ON_TAB_LABEL })).toBeChecked();
    expect(within(where).getByRole("group", { name: "Rows that show it" })).toHaveTextContent(
      ROWS_ONCE_CREATED,
    );
    fireEvent.click(within(libraries).getByRole("button", { name: "Change libraries" }));
    expect(document.activeElement).toHaveAccessibleName(/^Titles from/);
  });

  it("names the libraries once they're picked", async () => {
    showPage("/admin/collections/new?type=manual&libraryId=1");
    await nameField();
    const where = screen.getByRole("region", { name: "Where it shows" });
    expect(within(where).getByRole("group", { name: "Libraries" })).toHaveTextContent(
      "LibrariesMovies",
    );
    expect(within(where).queryByRole("link", { name: "Arrange" })).toBeNull();
  });
});

describe("unticking a server collection's library", () => {
  it("names only the titles no kept library shows, reading each library to its last page", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", {
      ...adminCollection,
      library_ids: ["1", "2"],
    });
    members = [HEAT.content_id, ALIEN.content_id, TOTORO.content_id];
    const page = (titles: Title[], next?: string) => ({
      items: titles.map(catalogItem),
      page: next ? { has_more: true, next_cursor: next } : { has_more: false },
      effective_sort: { field: "position", order: "asc" },
    });
    // Movies shows Heat only on its second page; Kids shows Heat and Totoro.
    v2Recorder.answer("POST /api/v2/catalog/query", (call: RecordedCall) => {
      const body = call.body as { library_id?: string; cursor?: string };
      if (body.library_id === "1") return body.cursor ? page([HEAT]) : page([ALIEN], "next");
      if (body.library_id === "2") return page([HEAT, TOTORO]);
      return catalogAnswer(call);
    });
    const user = userEvent.setup();
    showPage("/admin/collections/c1/edit?libraryId=1");
    await user.click(await screen.findByRole("button", { name: /^Titles from:/ }));
    await user.click(screen.getByRole("menuitemcheckbox", { name: "Kids" }));
    await user.keyboard("{Escape}");
    expect(await screen.findByText(/only in Kids/)).toHaveTextContent(
      "1 title is only in Kids: My Neighbor Totoro. They'll stop showing when you save.",
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
  it("orders the manual editor sections with Name first", async () => {
    showPage("/collections/new?type=manual");
    await nameField();
    expect(screen.queryByRole("navigation", { name: "Editor sections" })).toBeNull();
    const order = ["Name and description", "Titles", "Where it shows", "Look"];
    expect(inPageOrder([...order].reverse())).toEqual(order);
  });
});

describe("title search", () => {
  it("searches each of a server collection's libraries, at most four at once, merged by rank", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", {
      ...adminCollection,
      library_ids: ["1", "2", "3", "4", "5"],
    });
    v2Recorder.answer(
      "GET /api/v2/admin/collections",
      adminCollectionList({ ...adminCollection, library_ids: ["1", "2", "3", "4", "5"] }),
    );
    members = [];
    searchable = { "1": [HEAT, ALIEN], "2": [TOTORO], "3": [], "4": [], "5": [] };
    let inFlight = 0;
    let most = 0;
    v2Recorder.answer("POST /api/v2/catalog/query", async (call: RecordedCall) => {
      if ((call.body as { q?: string }).q !== "a") return catalogAnswer(call);
      inFlight++;
      most = Math.max(most, inFlight);
      await new Promise((resolve) => setTimeout(resolve, 5));
      inFlight--;
      return catalogAnswer(call);
    });
    showPage("/admin/collections/c1/edit?libraryId=1");
    fireEvent.change(await search(), { target: { value: "a" } });
    const options = await screen.findAllByRole("option");
    expect(options.map((option) => option.textContent)).toEqual([
      expect.stringContaining("Heat"),
      expect.stringContaining("My Neighbor Totoro"),
      expect.stringContaining("Alien"),
    ]);
    const searches = v2Recorder
      .callsOf("POST /api/v2/catalog/query")
      .filter((call) => (call.body as { q?: string }).q === "a");
    expect(
      searches.map((call) => (call.body as { library_id?: string }).library_id).sort(),
    ).toEqual(["1", "2", "3", "4", "5"]);
    expect(most).toBeLessThanOrEqual(4);
  });

  it("sends the one library of a single-library collection", async () => {
    showPage("/admin/collections/c1/edit?libraryId=1");
    fireEvent.change(await search(), { target: { value: "heat" } });
    await screen.findAllByRole("option");
    const [query] = v2Recorder
      .callsOf("POST /api/v2/catalog/query")
      .filter((call) => (call.body as { q?: string }).q === "heat");
    expect(query!.body).toMatchObject({ source: "query", library_id: "1", q: "heat" });
  });

  it("offers only what the viewer's catalog answers, and reports an add the server refuses", async () => {
    // The viewer-scoped search leaves out titles from hidden libraries and
    // above the profile's rating ceiling; Totoro is one of them.
    searchable = { all: [ALIEN] };
    v2Recorder.answer("PUT /api/v2/collections/{id}/items/{item_id}", () => {
      throw new Error("Item not found.");
    });
    showPage("/collections/c1/edit");
    fireEvent.change(await search(), { target: { value: "o" } });
    const options = await screen.findAllByRole("option");
    expect(options).toHaveLength(1);
    expect(screen.queryByRole("option", { name: /Totoro/ })).toBeNull();
    const [query] = v2Recorder
      .callsOf("POST /api/v2/catalog/query")
      .filter((call) => (call.body as { q?: string }).q === "o");
    expect(query!.body).not.toHaveProperty("library_id");
    fireEvent.click(options[0]!);
    expect(await screen.findByText(/Couldn't add Alien/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Remove Alien" })).toBeNull();
  });

  it("marks titles already in the collection and adds the highlighted one with Enter", async () => {
    showPage("/collections/c1/edit");
    const box = await search();
    fireEvent.change(box, { target: { value: "e" } });
    const heat = await screen.findByRole("option", { name: /Heat.*In this collection/ });
    expect(heat).toHaveAttribute("aria-disabled", "true");
    fireEvent.keyDown(box, { key: "ArrowDown" });
    expect(box).toHaveAttribute("aria-activedescendant", expect.stringContaining("alien"));
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() =>
      expect(v2Recorder.callsOf("PUT /api/v2/collections/{id}/items/{item_id}")).toHaveLength(1),
    );
    fireEvent.keyDown(box, { key: "Escape" });
    expect(box).toHaveAttribute("aria-expanded", "false");
  });
});
