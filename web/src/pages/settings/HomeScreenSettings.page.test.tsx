import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import type { SectionOverride, SettingsSectionEntry } from "@/api/types";
import { sectionKeys } from "@/hooks/queries/keys";
import { expectPhoneLayout, stubPhone } from "@/components/homeRows/phoneLayout.test-support";
import { recipeCatalogFixture } from "@/lib/homeRows/recipeCatalogFixture.test-support";
import HomeScreenSettings from "./HomeScreenSettings";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  role: "user" as string | undefined,
  collections: [] as Array<Record<string, unknown>>,
  catalog: vi.fn(),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => ({ user: { role: mocks.role }, profile: { id: "p1" } }),
}));
vi.mock("@/hooks/queries/libraries", () => {
  const data = [{ id: 7, name: "Movies", type: "movies" }];
  return { useUserLibraries: () => ({ data }), useAvailableUserLibraries: () => ({ data }) };
});
vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: () => ({ data: {}, isLoading: false }),
  useSetSettingValue: () => ({ mutate: vi.fn(), isPending: false }),
  invalidateSettingValueQueries: vi.fn(),
}));
vi.mock("@/hooks/queries/useAllUserCollections", () => ({
  useAllUserCollections: () => ({
    collections: mocks.collections,
    isLoading: false,
    isError: false,
  }),
}));
vi.mock("@/lib/recipes", async () => ({
  ...(await vi.importActual<typeof import("@/lib/recipes")>("@/lib/recipes")),
  fetchRecipeCatalog: () => mocks.catalog(),
}));

type Args = {
  query?: { scope?: string; library_id?: string };
  path?: { id?: string };
  body?: { overrides: SectionOverride[] };
};

function entry(id: string, position: number, more: Partial<SettingsSectionEntry> = {}) {
  return {
    id,
    section_type: "recently_added",
    title: `Row ${id}`,
    default_title: `Row ${id}`,
    featured: false,
    item_limit: 20,
    hidden: false,
    is_custom: false,
    customized: false,
    position,
    config: {},
    ...more,
  } satisfies SettingsSectionEntry;
}

/** The server rows of each page; the profile's saved overrides are applied the way the server does. */
let serverRows: Record<string, SettingsSectionEntry[]>;
let saved: Record<string, SectionOverride[]>;
let puts: Array<{ page: string; overrides: SectionOverride[] }>;
let calls: Array<{ operation: string; args: Args }>;
let held: Map<string, Array<() => void>>;
let observers: Array<{ callback: IntersectionObserverCallback; targets: Set<Element> }>;

function pageKey(args: Args) {
  return args.query?.scope === "library" ? `library:${args.query.library_id}` : "home";
}

function resolve(key: string): SettingsSectionEntry[] {
  const overrides = saved[key] ?? [];
  const bySection = new Map(overrides.filter((o) => o.section_id).map((o) => [o.section_id, o]));
  const rows: SettingsSectionEntry[] = [];
  for (const row of serverRows[key] ?? []) {
    const o = bySection.get(row.id);
    if (o?.removed) continue;
    rows.push({
      ...row,
      title: o?.title || row.title,
      hidden: o?.hidden ?? false,
      featured: o?.featured ?? row.featured,
      position: o?.position ?? row.position,
      customized: Boolean(o),
    });
  }
  for (const own of overrides.filter((o) => !o.section_id)) {
    rows.push({
      ...entry(own.id!, own.position ?? 0),
      section_type: own.section_type!,
      title: own.title!,
      default_title: "",
      hidden: Boolean(own.hidden),
      is_custom: true,
      customized: true,
      config: own.config ?? {},
    });
  }
  return rows.sort((a, b) => a.position - b.position);
}

function hold(operation: string) {
  held.set(operation, []);
}

async function release(operation: string) {
  await waitFor(() => expect(held.get(operation)?.length).toBeGreaterThan(0));
  const resume = held.get(operation)!.shift()!;
  await act(async () => resume());
}

beforeEach(() => {
  vi.clearAllMocks();
  // The signed-in profile; saves are written as it.
  setAccessToken("token");
  setProfileId("p1");
  mocks.role = "user";
  mocks.catalog.mockReset().mockResolvedValue(recipeCatalogFixture);
  mocks.collections = [{ id: "lib-c", title: "Studio Ghibli", source: "library", group: "Movies" }];
  serverRows = {
    home: [entry("a", 0), entry("b", 1)],
    "library:7": [entry("m", 0, { title: "New Movies", default_title: "New Movies" })],
  };
  saved = {};
  puts = [];
  calls = [];
  held = new Map();
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
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    calls.push({ operation, args });
    const queue = held.get(operation);
    if (queue) await new Promise<void>((resume) => queue.push(resume));
    const key = pageKey(args);
    switch (operation) {
      case "GET /api/v2/profile/sections/settings":
        return { items: resolve(key) };
      case "GET /api/v2/profile/sections":
        return { items: saved[key] ?? [] };
      case "PUT /api/v2/profile/sections":
        puts.push({ page: key, overrides: args.body!.overrides });
        saved[key] = args.body!.overrides;
        return { items: saved[key] };
      case "DELETE /api/v2/profile/sections":
        saved[key] = [];
        return undefined;
      case "GET /api/v2/home/sections/{id}/items":
        return {
          id: args.path!.id,
          section_type: "recently_added",
          title: "Row",
          featured: false,
          item_limit: 20,
          total_count: 1,
          is_custom: false,
          customized: false,
          items: [],
        };
      case "GET /api/v2/system/identity":
        return { server_id: "server-1" };
    }
    throw new Error(`Unexpected ${operation}`);
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setProfileId(null);
  setAccessToken(null);
});

async function renderPage(path = "/settings/home-screen", queryClient?: QueryClient) {
  const client =
    queryClient ??
    new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <QueryClientProvider client={client}>
        <HomeScreenSettings />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  const add = await screen.findByRole("button", { name: "Add row" });
  await waitFor(() => expect(add).toBeEnabled());
  return client;
}

const pageButton = (name: string) => screen.getByRole("button", { name });
const rowSwitch = (name: string) => screen.getByRole("switch", { name });

async function rowMenu(title: string) {
  await userEvent.click(screen.getByRole("button", { name: `More for ${title}` }));
  return screen.findByRole("menu");
}

async function chooseFromMenu(title: string, item: string) {
  const menu = await rowMenu(title);
  await userEvent.click(within(menu).getByRole("menuitem", { name: item }));
}

async function openMore() {
  await userEvent.click(screen.getByRole("button", { name: "More" }));
  return screen.findByRole("menu");
}

describe("Settings > Home Screen", () => {
  it("fits a phone", async () => {
    stubPhone();
    await renderPage();
    await expectPhoneLayout("Home");
  });

  it("is titled Home screen and says only this profile changes", async () => {
    await renderPage();
    expect(screen.getByRole("heading", { level: 2, name: "Home screen" })).toBeInTheDocument();
    expect(
      screen.getByText(
        "Choose the rows you see and their order. Only this profile changes, and it saves as you go.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("2 rows · 2 shown")).toBeInTheDocument();
  });

  it("hides a row with its switch, saving only the hide, and keeps the page switcher off until it lands", async () => {
    await renderPage();
    hold("PUT /api/v2/profile/sections");

    await userEvent.click(rowSwitch("Show Row a on my Home"));

    expect(await screen.findByText(/is hidden on your Home\./)).toBeInTheDocument();
    expect(pageButton("Movies")).toBeDisabled();
    await release("PUT /api/v2/profile/sections");
    await waitFor(() => expect(pageButton("Movies")).toBeEnabled());
    expect(puts).toHaveLength(1);
    // Only what the profile changed: no title, size, hero or config, and no
    // override at all for the row it left alone.
    expect(puts[0]!.overrides).toEqual([{ section_id: "a", id: expect.any(String), hidden: true }]);
  });

  it("names the page in switch labels and hidden rows on a library page", async () => {
    saved["library:7"] = [{ id: "o-m", section_id: "m", hidden: true }];
    await renderPage("/settings/home-screen?page=7");
    expect(
      await screen.findByRole("switch", { name: "Show New Movies on my Movies page" }),
    ).not.toBeChecked();
    expect(screen.getByText(/is hidden on your Movies page\./)).toBeInTheDocument();
    expect(pageButton("Movies")).toHaveAttribute("aria-pressed", "true");
  });

  it("renames a row from Edit row, storing the name alone, and locks what a server row shows", async () => {
    await renderPage();
    await chooseFromMenu("Row a", "Edit row…");
    const dialog = await screen.findByRole("dialog", { name: "Edit row" });
    expect(within(dialog).getByText("Changes apply only to this profile.")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Change" })).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/Live preview/)).not.toBeInTheDocument();
    expect(
      within(dialog).getByText("You'll see your changes on Home after you save."),
    ).toBeInTheDocument();
    const name = within(dialog).getByLabelText("Row name");
    await userEvent.clear(name);
    await userEvent.type(name, "Fresh Movies");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]!.overrides).toEqual([
      { section_id: "a", id: expect.any(String), hidden: false, title: "Fresh Movies" },
    ]);
    expect(await screen.findByText(/^Renamed from/)).toBeInTheDocument();
  });

  it("keeps following the server's size and hero for a row renamed while they changed", async () => {
    const client = await renderPage();
    await chooseFromMenu("Row a", "Edit row…");
    const dialog = await screen.findByRole("dialog", { name: "Edit row" });
    // An administrator changes the row in another tab while Edit row is open.
    serverRows.home![0] = { ...serverRows.home![0]!, item_limit: 30, featured: true };
    await act(async () => client.invalidateQueries({ queryKey: sectionKeys.all }));
    const name = within(dialog).getByLabelText("Row name");
    await userEvent.clear(name);
    await userEvent.type(name, "Fresh Movies");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]!.overrides).toEqual([
      { section_id: "a", id: expect.any(String), hidden: false, title: "Fresh Movies" },
    ]);
  });

  it("doesn't offer to change a server row's collection, rules or picked titles", async () => {
    serverRows.home = [
      entry("c", 0, {
        section_type: "collection",
        title: "Ghibli",
        default_title: "Ghibli",
        config: { collection_id: "lib-c" },
      }),
      entry("f", 1, {
        section_type: "custom_filter",
        title: "Short films",
        default_title: "Short films",
        config: { query_definition: { groups: [{ match: "all", rules: [] }] } },
      }),
      entry("p", 2, {
        section_type: "admin_curated_list",
        title: "Staff picks",
        default_title: "Staff picks",
        config: { item_ids: [] },
      }),
    ];
    await renderPage();
    const controls: Array<[string, () => HTMLElement | null]> = [
      ["Ghibli", () => screen.queryByRole("searchbox", { name: "Search collections" })],
      ["Short films", () => screen.queryByRole("button", { name: "Add rule" })],
      ["Staff picks", () => screen.queryByRole("searchbox", { name: "Search titles to add" })],
    ];
    for (const [title, control] of controls) {
      await chooseFromMenu(title, "Edit row…");
      const dialog = await screen.findByRole("dialog", { name: "Edit row" });
      expect(within(dialog).getByLabelText("Row name")).toHaveValue(title);
      expect(control()).not.toBeInTheDocument();
      await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    }
  });

  it("says what a renamed row was called and gives it its name back", async () => {
    saved.home = [{ id: "o-a", section_id: "a", title: "Mine" }];
    await renderPage();
    const line = screen.getByText("Mine").closest("li")!;
    expect(within(line).getByText(/^Renamed from/)).toBeInTheDocument();
    expect(within(line).getByText("Row a")).toBeInTheDocument();

    await chooseFromMenu("Mine", "Use the original name");

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]!.overrides).toEqual([{ section_id: "a", id: "o-a", hidden: false }]);
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "More for Mine" })).not.toBeInTheDocument(),
    );
    const menu = await rowMenu("Row a");
    expect(within(menu).queryByRole("menuitem", { name: "Use the original name" })).toBeNull();
  });

  it("removes a server row after asking", async () => {
    await renderPage();
    await chooseFromMenu("Row b", "Remove from my Home…");
    const dialog = await screen.findByRole("dialog", { name: "Remove Row b from your Home?" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove row" }));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]!.overrides).toEqual([
      { section_id: "b", id: expect.any(String), removed: true },
    ]);
    await waitFor(() => expect(screen.queryByText("Row b")).not.toBeInTheDocument());
  });

  it("deletes the profile's own row, tagged Yours, after asking", async () => {
    saved.home = [
      {
        id: "own-1",
        position: 2,
        hidden: false,
        title: "My Gems",
        section_type: "hidden_gems",
        config: {},
      },
    ];
    await renderPage();
    const line = screen.getByText("My Gems").closest("li")!;
    expect(within(line).getByText("Yours")).toBeInTheDocument();

    await chooseFromMenu("My Gems", "Delete row…");
    const dialog = await screen.findByRole("dialog", { name: "Delete My Gems?" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete row" }));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]!.overrides.some((o) => o.id === "own-1")).toBe(false);
  });

  it("adds a row at the bottom with no draft preview", async () => {
    await renderPage();
    await userEvent.click(screen.getByRole("button", { name: "Add row" }));
    const picker = await screen.findByRole("dialog", { name: "Add a row to Home" });
    await userEvent.click(within(picker).getByRole("button", { name: "Hidden gems" }));
    const form = await screen.findByRole("dialog", { name: "Hidden gems" });
    expect(within(form).getByText("You'll see it on Home after you add it.")).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]!.overrides).toEqual([
      expect.objectContaining({
        section_id: undefined,
        position: 2,
        section_type: "hidden_gems",
        hidden: false,
      }),
    ]);
    expect(calls.some((call) => call.operation.includes("preview"))).toBe(false);
  });

  it("keeps a personal collection row's collection when it isn't in the picker list", async () => {
    saved.home = [
      {
        id: "own-c",
        position: 2,
        hidden: false,
        title: "Comfort Shows",
        section_type: "collection",
        config: { user_collection_id: "gone-1", extra: true },
      },
    ];
    await renderPage();
    await chooseFromMenu("Comfort Shows", "Edit row…");
    const dialog = await screen.findByRole("dialog", { name: "Edit row" });
    const name = within(dialog).getByLabelText("Row name");
    await userEvent.clear(name);
    await userEvent.type(name, "Cozy");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]!.overrides.find((o) => o.id === "own-c")).toMatchObject({
      title: "Cozy",
      config: { user_collection_id: "gone-1", extra: true },
    });
  });

  it("calls only this profile's own collections its own", async () => {
    mocks.collections = [
      {
        id: "mine",
        title: "Rainy days",
        source: "user",
        group: "My Collections",
        creator_profile_id: "p1",
      },
      {
        id: "theirs",
        title: "Road trips",
        source: "user",
        group: "My Collections",
        creator_profile_id: "p2",
      },
    ];
    saved.home = ["mine", "theirs"].map((id, index) => ({
      id: `row-${id}`,
      position: 2 + index,
      hidden: false,
      title: `Row for ${id}`,
      section_type: "collection",
      config: { user_collection_id: id },
    }));
    await renderPage();
    await screen.findByText("Row for mine");

    const text = document.body.textContent;
    expect(text).toContain("Your Rainy days collection");
    // Another profile made this one and shared it, so it isn't this profile's.
    expect(text).toContain("The Road trips collection");
  });

  it("offers export, import and a reset named after the page under More", async () => {
    const createObjectURL = vi.fn(() => "blob:layout");
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() }));
    await renderPage();
    let menu = await openMore();
    expect(
      within(menu).getByRole("menuitem", { name: "Reset Home to the server's rows…" }),
    ).toBeInTheDocument();
    await userEvent.click(within(menu).getByRole("menuitem", { name: "Export layout" }));
    await waitFor(() => expect(createObjectURL).toHaveBeenCalled());

    menu = await openMore();
    await userEvent.click(within(menu).getByRole("menuitem", { name: "Import layout…" }));
    const dialog = await screen.findByRole("dialog", { name: "Import home layout" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "More" })).toHaveFocus());

    await userEvent.click(pageButton("Movies"));
    menu = await openMore();
    expect(
      within(menu).getByRole("menuitem", { name: "Reset the Movies page to the server's rows…" }),
    ).toBeInTheDocument();
  });

  it("resets the page after asking and keeps editing off until the reset lands", async () => {
    saved.home = [{ id: "o-a", section_id: "a", hidden: true }];
    await renderPage();
    hold("DELETE /api/v2/profile/sections");
    const menu = await openMore();
    await userEvent.click(
      within(menu).getByRole("menuitem", { name: "Reset Home to the server's rows…" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "Reset your Home to the server's rows?",
    });
    await userEvent.click(within(dialog).getByRole("button", { name: "Reset" }));

    await waitFor(() => expect(rowSwitch("Show Row b on my Home")).toBeDisabled());
    expect(pageButton("Movies")).toBeDisabled();
    await release("DELETE /api/v2/profile/sections");
    await waitFor(() => expect(rowSwitch("Show Row a on my Home")).toBeChecked());
    expect(rowSwitch("Show Row a on my Home")).toBeEnabled();
  });

  it("loads peeks from this profile's own Home rows, never the admin preview", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    // Home already has row a cached: its posters show while the peek loads.
    client.setQueryData(sectionKeys.homeItems("a"), {
      section: {
        id: "a",
        section_type: "recently_added",
        title: "Row a",
        featured: false,
        item_limit: 20,
        total_count: 1,
        is_custom: false,
        customized: false,
        items: [{ content_id: "m1", title: "Past Lives", poster_url: "/p.jpg" }],
      },
    });
    await renderPage("/settings/home-screen", client);
    const line = screen.getByText("Row a").closest("li")!;
    expect(line.querySelector("img")).not.toBeNull();

    act(() => {
      for (const observer of observers) {
        for (const target of observer.targets) {
          observer.callback(
            [{ isIntersecting: true, target } as IntersectionObserverEntry],
            {} as IntersectionObserver,
          );
        }
      }
    });
    await waitFor(() =>
      expect(
        calls
          .filter((call) => call.operation === "GET /api/v2/home/sections/{id}/items")
          .map((call) => call.args.path?.id)
          .sort(),
      ).toEqual(["a", "b"]),
    );
    expect(calls.some((call) => call.operation.includes("admin"))).toBe(false);
  });
});

describe("rule rows on Settings > Home Screen", () => {
  async function pickerCards() {
    await userEvent.click(screen.getByRole("button", { name: "Add row" }));
    const picker = await screen.findByRole("dialog", { name: "Add a row to Home" });
    return within(picker);
  }

  it.each(["user", "admin"])(
    "offers rule rows to a %s account without reading a server setting",
    async (role) => {
      mocks.role = role;
      await renderPage();
      const picker = await pickerCards();
      expect(picker.getByRole("button", { name: "Titles matching rules" })).toBeInTheDocument();
      // Editor's picks stays off the picker for new rows.
      expect(picker.queryByRole("button", { name: "Editor's picks" })).toBeNull();
      expect(
        calls.some(
          ({ operation }) =>
            operation.includes("/sections/flags") || operation.includes("/admin/settings"),
        ),
      ).toBe(false);
    },
  );

  it("keeps a page with the profile's rule rows and Editor's picks rows open to change", async () => {
    saved.home = [
      {
        id: "rule-1",
        position: 2,
        hidden: false,
        title: "90s Crowd-Pleasers",
        section_type: "custom_filter",
        config: {},
      },
      {
        id: "picks-1",
        position: 3,
        hidden: false,
        title: "Staff Picks",
        section_type: "admin_curated_list",
        config: { item_ids: ["x"] },
      },
    ];
    await renderPage();
    expect(rowSwitch("Show 90s Crowd-Pleasers on my Home")).toBeEnabled();
    const menu = await rowMenu("Staff Picks");
    expect(within(menu).getByRole("menuitem", { name: "Edit row…" })).not.toHaveAttribute(
      "aria-disabled",
      "true",
    );
    await userEvent.keyboard("{Escape}");

    await userEvent.click(rowSwitch("Show Row a on my Home"));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]!.overrides.map((o) => o.id)).toEqual(
      expect.arrayContaining(["rule-1", "picks-1"]),
    );
  });
});
