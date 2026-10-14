import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import { recipeCatalogFixture } from "@/lib/homeRows/recipeCatalogFixture.test-support";
import type { RowDraft } from "@/lib/homeRows/rowDraft";
import type { EditSession, HomeRow, HomeRowsAdapter } from "@/lib/homeRows/types";
import { VARIANT_FAMILIES } from "@/lib/homeRows/variants";
import { AddRowDialog } from "./AddRowDialog";
import { paramFieldKeys } from "@/lib/homeRows/paramFields";

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));
vi.mock("@/hooks/queries/items", () => ({
  fetchWatchDetail: async (id: string) => ({ title: `Title ${id}` }),
}));

const COLLECTIONS: CollectionOption[] = [
  {
    id: "ghibli",
    title: "Studio Ghibli",
    source: "library",
    group: "Movies",
    collection_type: "manual",
    item_count: 23,
  },
  {
    id: "best",
    title: "Best Picture Winners",
    source: "library",
    group: "Movies",
    collection_type: "mdblist",
    item_count: 96,
  },
];

function row(overrides: Partial<HomeRow> = {}): HomeRow {
  return {
    id: "r1",
    title: "Spotlight",
    sectionType: "editorial_spotlight",
    config: { subject_type: "era", subject: "1990s" },
    itemLimit: 20,
    hero: false,
    shown: true,
    own: false,
    legacyTrakt: false,
    ...overrides,
  };
}

let create: ReturnType<typeof vi.fn<(draft: RowDraft) => Promise<{ newIds: string[] }>>>;
let save: ReturnType<typeof vi.fn<(session: EditSession, draft: RowDraft) => Promise<void>>>;

function adapter(): HomeRowsAdapter {
  return {
    surface: "admin",
    page: { kind: "home" },
    pages: [{ ref: { kind: "home" }, label: "Home" }],
    setPage: () => {},
    status: "ready",
    error: null,
    canEdit: true,
    rows: [
      row({ id: "t", sectionType: "trending_on_server", config: { window: "7d" } }),
      row({ id: "g", sectionType: "collection", config: { library_collection_id: "ghibli" } }),
    ],
    pending: false,
    conflict: null,
    reload: async () => {},
    canReorder: true,
    orderToken: null,
    reorder: async () => {},
    setShown: async () => {},
    setHero: async () => {},
    capabilities: { draftPreview: false, libraryCopies: false },
    create,
    openEdit: async () => {
      throw new Error("unused");
    },
    reloadEdit: async () => {
      throw new Error("unused");
    },
    save,
    collections: {
      options: COLLECTIONS,
      loading: false,
      failed: false,
      href: "/admin/collections",
    },
  };
}

function Harness({
  session = null,
  catalogLoaded = true,
}: {
  session?: EditSession | null;
  catalogLoaded?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <MemoryRouter>
      <QueryClientProvider client={new QueryClient()}>
        <button onClick={() => setOpen(true)}>Open</button>
        {open ? (
          <AddRowDialog
            adapter={adapter()}
            catalog={catalogLoaded ? recipeCatalogFixture : undefined}
            libraries={[{ id: 7, name: "Movies" }]}
            session={session}
            onClose={() => setOpen(false)}
            onSaved={() => {}}
          />
        ) : null}
      </QueryClientProvider>
    </MemoryRouter>
  );
}

async function open(session: EditSession | null = null) {
  render(<Harness session={session} />);
  const trigger = screen.getByRole("button", { name: "Open" });
  trigger.focus();
  await userEvent.click(trigger);
  return { trigger, dialog: await screen.findByRole("dialog") };
}

beforeEach(() => {
  create = vi.fn(async () => ({ newIds: ["n"] }));
  save = vi.fn(async () => {});
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Add row picker", () => {
  it("lists the groups as vertical tabs with their counts and moves between them with arrows", async () => {
    const { dialog } = await open();
    const tablist = within(dialog).getByRole("tablist", { name: "Kinds of rows" });
    expect(tablist).toHaveAttribute("aria-orientation", "vertical");
    const tabs = within(tablist).getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      "Keep watching5 kinds",
      "What's new4 kinds",
      "Popular4 kinds",
      "Picked for you4 kinds",
      "Moods & themes11 kinds",
      "Collections & rules2 kinds",
    ]);
    expect(within(dialog).getByRole("searchbox", { name: "Search rows" })).toHaveFocus();
    tabs[0]!.focus();
    await userEvent.keyboard("{ArrowDown}");
    expect(tabs[1]).toHaveFocus();
    expect(tabs[1]).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{End}");
    expect(tabs[5]).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}");
    expect(tabs[0]).toHaveFocus();
  });

  it("marks kinds already on the page and names variant chips with their kind", async () => {
    const { dialog } = await open();
    expect(
      within(dialog).getByRole("button", { name: "Trending on this server" }),
    ).toHaveAccessibleDescription("What's been played most here lately. Already on Home.");
    expect(
      within(dialog).getByRole("button", { name: "Trending on this server, 7 days" }),
    ).toBeInTheDocument();
  });

  it("offers a way back from a search that finds nothing", async () => {
    const { dialog } = await open();
    await userEvent.type(within(dialog).getByRole("searchbox"), "zzz");
    expect(within(dialog).getByText("No rows match “zzz”.")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Clear search" }));
    expect(within(dialog).getAllByRole("tab")).toHaveLength(6);
  });

  it("closes on Escape from step 2 and puts focus back on the button that opened it", async () => {
    const { trigger, dialog } = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: "Hidden gems" }));
    expect(await screen.findByRole("heading", { name: "Hidden gems" })).toHaveFocus();
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(trigger).toHaveFocus();
  });

  it("shows variants as text on phones and lays the groups out as a row of chips", async () => {
    vi.stubGlobal(
      "matchMedia",
      (query: string) =>
        ({
          matches: query === "(max-width: 1023px)",
          media: query,
          addEventListener: () => {},
          removeEventListener: () => {},
        }) as unknown as MediaQueryList,
    );
    const { dialog } = await open();
    expect(within(dialog).getByRole("tablist")).toHaveAttribute("aria-orientation", "horizontal");
    expect(within(dialog).getByText("24 hours, 7 days or 30 days")).toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "Trending on this server, 7 days" }),
    ).toBeNull();
  });

  it("uses the phone sheet's short copy on phones", async () => {
    vi.stubGlobal(
      "matchMedia",
      (query: string) =>
        ({
          matches: query === "(max-width: 1023px)" || query === "(max-width: 639px)",
          media: query,
          addEventListener: () => {},
          removeEventListener: () => {},
        }) as unknown as MediaQueryList,
    );
    const { dialog } = await open();
    expect(
      within(dialog).getByText("Pick what it shows. It goes to the bottom of Home."),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("searchbox", { name: "Search rows" })).toHaveAttribute(
      "placeholder",
      "Search rows",
    );
  });
});

describe("Add row form", () => {
  it("adds with the preset name when the name is left blank, never a raw type", async () => {
    const { dialog } = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: "Short & sweet" }));
    const form = await screen.findByRole("dialog", { name: "Short & sweet" });
    fireEvent.change(within(form).getByLabelText("Row name"), { target: { value: " " } });
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0]![0]).toMatchObject({
      sectionType: "short_watches",
      title: "Short & Sweet",
      config: { max_minutes: 95 },
    });
  });

  it("leaves a dialog opened after it alone when an add finishes after it closed", async () => {
    let finish!: (value: { newIds: string[] }) => void;
    create.mockImplementation(() => new Promise((resolve) => (finish = resolve)));
    const { trigger, dialog } = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: "Short & sweet" }));
    const form = await screen.findByRole("dialog", { name: "Short & sweet" });
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    await userEvent.click(trigger);
    expect(await screen.findByRole("dialog", { name: "Add a row to Home" })).toBeInTheDocument();
    await act(async () => finish({ newIds: ["n"] }));
    expect(screen.getByRole("dialog", { name: "Add a row to Home" })).toBeInTheDocument();
  });

  it("keeps rarely changed settings under More options with a summary", async () => {
    const { dialog } = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: "Short & sweet" }));
    const form = await screen.findByRole("dialog", { name: "Short & sweet" });
    const more = within(form).getByRole("button", { name: /More options/ });
    expect(more).toHaveTextContent("20 titles·not the hero banner");
    expect(more).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(more);
    await userEvent.click(within(form).getByRole("switch", { name: "Hero banner" }));
    expect(more).toHaveTextContent("the hero banner");
    expect(
      within(form).getByText(
        "The web shows this row as a banner. The apps show it as a regular row.",
      ),
    ).toBeInTheDocument();
    expect(within(form).getByLabelText("Longest runtime (minutes)")).toHaveValue(95);
  });

  it("lets Number of titles be cleared and retyped, and restores it when left blank", async () => {
    const { dialog } = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: "Short & sweet" }));
    const form = await screen.findByRole("dialog", { name: "Short & sweet" });
    const more = within(form).getByRole("button", { name: /More options/ });
    await userEvent.click(more);
    const limit = within(form).getByLabelText("Number of titles");
    await userEvent.clear(limit);
    expect(limit).toHaveValue(null);
    await userEvent.type(limit, "35");
    expect(more).toHaveTextContent("35 titles");
    await userEvent.clear(limit);
    await userEvent.tab();
    expect(limit).toHaveValue(35);
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0]![0].itemLimit).toBe(35);
  });

  it("keeps Family movie night out of the holiday list and needs at least one holiday", async () => {
    const { dialog } = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: "Seasonal picks" }));
    const form = await screen.findByRole("dialog", { name: "Seasonal picks" });
    const holidays = within(form).getByRole("group", { name: "Holidays" });
    expect(within(holidays).queryByRole("checkbox", { name: "Family movie night" })).toBeNull();
    for (const box of within(holidays).getAllByRole("checkbox")) {
      if (box.getAttribute("aria-checked") === "true") await userEvent.click(box);
    }
    expect(within(form).getByRole("radio", { name: /Holidays/ })).toBeChecked();
    expect(within(form).getByText("Pick at least one holiday.")).toBeInTheDocument();
    expect(within(form).getByRole("button", { name: "Add row" })).toBeDisabled();
  });

  it("keeps the spaces typed in a holiday's name and saves it trimmed", async () => {
    const { dialog } = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: "Seasonal picks" }));
    const form = await screen.findByRole("dialog", { name: "Seasonal picks" });
    const name = within(form).getByRole("textbox", { name: "Row name during Christmas" });
    await userEvent.type(name, " Christmas movies ");
    expect(name).toHaveValue(" Christmas movies ");
    await userEvent.tab();
    expect(name).toHaveValue("Christmas movies");
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0]![0].config.theme_titles).toEqual({ christmas: "Christmas movies" });
  });
});

describe("Edit row form", () => {
  const session = (overrides: Partial<HomeRow> = {}): EditSession => ({
    row: row(overrides),
    token: null,
  });

  it("shows no variant for a config no preset matches and saves it unchanged", async () => {
    const { dialog } = await open(session({ title: "Decades" }));
    expect(within(dialog).getByText("Spotlight")).toBeInTheDocument();
    expect(
      within(dialog)
        .getAllByRole("radio")
        .map((radio) => radio.getAttribute("aria-checked")),
    ).toEqual(["false", "false", "false", "false"]);
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]![1].config).toEqual({ subject_type: "era", subject: "1990s" });
  });

  it("keeps a monthly rotation when a director spotlight becomes an actor spotlight", async () => {
    const { dialog } = await open(
      session({
        title: "Directors",
        config: { subject_type: "director", auto_rotate: true, rotation_cadence: "monthly" },
      }),
    );
    expect(within(dialog).getByRole("radio", { name: /Director/ })).toBeChecked();
    await userEvent.click(within(dialog).getByRole("radio", { name: /Actor/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]![1]).toMatchObject({
      title: "Directors",
      config: { subject_type: "actor", auto_rotate: true, rotation_cadence: "monthly" },
    });
  });

  it("renames a row that kept its preset name when the kinds of rows load after it opened", async () => {
    const view = render(
      <Harness
        catalogLoaded={false}
        session={session({
          title: "Director Spotlight",
          config: { subject_type: "director", auto_rotate: true, rotation_cadence: "weekly" },
        })}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    const dialog = await screen.findByRole("dialog");
    view.rerender(
      <Harness
        session={session({
          title: "Director Spotlight",
          config: { subject_type: "director", auto_rotate: true, rotation_cadence: "weekly" },
        })}
      />,
    );
    await userEvent.click(within(dialog).getByRole("radio", { name: /Actor/ }));
    expect(within(dialog).getByLabelText("Row name")).toHaveValue("Actor Spotlight");
  });

  it("reads a legacy family movie night row as that variant", async () => {
    const { dialog } = await open(
      session({ sectionType: "seasonal_themed", config: { theme: "family_movie_night" } }),
    );
    expect(within(dialog).getByRole("radio", { name: /Family movie night/ })).toBeChecked();
  });

  it("names a legacy single-holiday row and edits its holidays from that one", async () => {
    const { dialog } = await open(
      session({ sectionType: "seasonal_themed", config: { theme: "christmas", mode: "auto" } }),
    );
    expect(within(dialog).getByText("Seasonal picks (Christmas only)")).toBeInTheDocument();
    const holidays = within(dialog).getByRole("group", { name: "Holidays" });
    expect(within(holidays).getByRole("checkbox", { name: "Christmas" })).toBeChecked();
    await userEvent.click(within(holidays).getByRole("checkbox", { name: "Halloween" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalledTimes(1));
    expect(save.mock.calls[0]![1].config).toMatchObject({
      enabled_themes: ["christmas", "halloween"],
      theme: "",
    });
  });
});

describe("param fields", () => {
  // The holiday checklist refines the Holidays variant, and a spotlight's
  // free-text subject sits under More options; both are deliberate.
  const ALLOWED: Record<string, string[]> = {
    seasonal_themed: ["enabled_themes", "theme"],
    editorial_spotlight: ["subject"],
  };

  it("never offers a control for a key a variant owns", () => {
    for (const [type, family] of Object.entries(VARIANT_FAMILIES)) {
      const overlap = paramFieldKeys(type).filter(
        (key) => family.keys.includes(key) && !(ALLOWED[type] ?? []).includes(key),
      );
      expect(overlap, type).toEqual([]);
    }
  });

  it("has no Anchor item field for Because you watched", () => {
    expect(paramFieldKeys("because_you_watched")).toEqual([]);
  });
});

function editSession(overrides: Partial<HomeRow>): EditSession {
  return { row: row({ id: "e", ...overrides }), token: null };
}

function savedDraft(): RowDraft {
  expect(save).toHaveBeenCalledTimes(1);
  return save.mock.calls[0]![1];
}

describe("collection rows", () => {
  it("picks a collection, names the row after it and adds it", async () => {
    const { dialog } = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: "A collection" }));
    const form = await screen.findByRole("dialog", { name: "A collection" });
    expect(within(form).getByRole("link", { name: "Collections" })).toHaveAttribute(
      "href",
      "/admin/collections",
    );
    expect(within(form).getByRole("button", { name: "Add row" })).toBeDisabled();
    expect(within(form).getByRole("radio", { name: "Studio Ghibli" })).toHaveAccessibleDescription(
      "Manual · 23 titles · On Home",
    );
    await userEvent.click(within(form).getByRole("radio", { name: "Best Picture Winners" }));
    expect(within(form).getByLabelText("Row name")).toHaveValue("Best Picture Winners");
    expect(within(form).getByText("Starts as the collection's name.")).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0]![0]).toMatchObject({
      sectionType: "collection",
      title: "Best Picture Winners",
      config: { library_collection_id: "best" },
    });
  });

  it("keeps a row's collection, and every other key, when only its name changes", async () => {
    const config = { library_collection_id: "lib-gone", generated_source: "collection_auto" };
    await open(editSession({ title: "Shared", sectionType: "collection", config }));
    const dialog = screen.getByRole("dialog", { name: "Edit row" });
    expect(
      within(dialog).getByText(
        "This row's collection isn't in this list. The row keeps it until you pick another.",
      ),
    ).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("Row name"), { target: { value: "Renamed" } });
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(savedDraft()).toMatchObject({ title: "Renamed", config });
  });

  // The admin endpoint rejects a collection row without library_collection_id,
  // so a legacy personal-collection row needs a library collection picked.
  it("makes an admin pick a library collection for a legacy personal-collection row", async () => {
    const config = { user_collection_id: "u-9", sort_by: "title" };
    await open(editSession({ title: "Shared", sectionType: "collection", config }));
    const dialog = screen.getByRole("dialog", { name: "Edit row" });
    expect(
      within(dialog).queryByText(
        "This row's collection isn't in this list. The row keeps it until you pick another.",
      ),
    ).not.toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("Row name"), { target: { value: "Renamed" } });
    expect(within(dialog).getByRole("button", { name: "Save" })).toBeDisabled();

    await userEvent.click(within(dialog).getByRole("radio", { name: "Studio Ghibli" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(savedDraft()).toMatchObject({
      title: "Renamed",
      config: { library_collection_id: "ghibli", sort_by: "title" },
    });
    expect(savedDraft().config).not.toHaveProperty("user_collection_id");
  });

  it("turns a ready-made row into a collection row without leaving the dialog", async () => {
    await open(
      editSession({
        title: "My picks",
        sectionType: "trending_on_server",
        config: { window: "7d" },
      }),
    );
    const dialog = screen.getByRole("dialog", { name: "Edit row" });
    await userEvent.click(within(dialog).getByRole("button", { name: /^Change what/ }));
    const picker = await screen.findByRole("dialog", { name: "Change what this row shows" });
    await userEvent.click(within(picker).getByRole("button", { name: "A collection" }));
    const form = await screen.findByRole("dialog", { name: "Edit row" });
    expect(within(form).getByRole("button", { name: "Save" })).toBeDisabled();
    await userEvent.click(within(form).getByRole("radio", { name: "Studio Ghibli" }));
    expect(within(form).getByLabelText("Row name")).toHaveValue("My picks");
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(savedDraft()).toMatchObject({
      sectionType: "collection",
      title: "My picks",
      config: { library_collection_id: "ghibli" },
    });
  });
});

// Configs in the shape the older editor has always written for rule rows.
const MULTI_GROUP = {
  library_ids: [1],
  match: "any",
  groups: [
    {
      match: "all",
      rules: [
        { field: "genre", op: "is", value: "Horror" },
        { field: "year", op: "gte", value: 1980 },
      ],
    },
    { match: "any", rules: [{ field: "genre", op: "is_not", value: "Comedy" }] },
  ],
  sort: { field: "year", order: "asc" },
};

// The removed Easy mode saved fields the server does not know and booleans as text.
const UNKNOWN_FIELDS = {
  library_ids: [],
  match: "all",
  groups: [
    {
      match: "all",
      rules: [
        { field: "cast", op: "contains", value: "Tom Hanks" },
        { field: "genre", op: "is", value: "Drama" },
        { field: "watched", op: "is", value: "true" },
      ],
    },
  ],
  sort: { field: "added_at", order: "desc" },
};

describe("rule rows", () => {
  it.each([
    ["multi-group", MULTI_GROUP],
    ["unknown-field", UNKNOWN_FIELDS],
  ])("saves an untouched %s rule row with the config it opened with", async (_, config) => {
    await open(editSession({ sectionType: "custom_filter", config: structuredClone(config) }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(JSON.stringify(savedDraft().config)).toBe(JSON.stringify(config));
  });

  it("edits rules in one sentence and list, with no Easy mode", async () => {
    await open(editSession({ sectionType: "custom_filter", config: structuredClone(MULTI_GROUP) }));
    expect(screen.getAllByRole("group", { name: "What the row shows" })).toHaveLength(1);
    expect(screen.getByRole("group", { name: "Group 2" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Easy" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Advanced" })).toBeNull();
  });

  it("keeps personalized rules and sorts editable, with the order under More options", async () => {
    await open(
      editSession({
        sectionType: "custom_filter",
        config: {
          library_ids: [],
          match: "all",
          groups: [{ match: "all", rules: [{ field: "watched", op: "is", value: false }] }],
          sort: { field: "date_viewed", order: "desc" },
        },
      }),
    );
    expect(screen.queryByRole("group", { name: "Rule not editable here" })).toBeNull();
    expect(screen.getByRole("combobox", { name: "Field" })).toHaveTextContent("Watched");
    await userEvent.click(screen.getByRole("button", { name: /More options/ }));
    expect(screen.getByRole("combobox", { name: "Sort by" })).toHaveTextContent("Date Viewed");
  });

  it("names the order on the closed More options box", async () => {
    await open(
      editSession({
        sectionType: "custom_filter",
        config: { ...structuredClone(MULTI_GROUP), sort: { field: "rating_imdb", order: "desc" } },
      }),
    );
    expect(screen.getByRole("button", { name: /More options/ })).toHaveTextContent(
      "Highest rated first·20 titles·not the hero banner",
    );
  });

  it("shows rules it can't edit read-only and keeps them until removed", async () => {
    await open(
      editSession({ sectionType: "custom_filter", config: structuredClone(UNKNOWN_FIELDS) }),
    );
    const readOnly = screen.getAllByRole("group", { name: "Rule not editable here" });
    expect(readOnly).toHaveLength(2);
    await userEvent.click(within(readOnly[0]!).getByRole("button", { name: "Remove" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(savedDraft().config.groups).toEqual([
      {
        match: "all",
        rules: [
          { field: "genre", op: "is", value: "Drama" },
          { field: "watched", op: "is", value: "true" },
        ],
      },
    ]);
  });

  it("opens a legacy genre row in the rule builder and saves it as it is", async () => {
    const config = {
      filter_type: "movie",
      match: "all",
      groups: [{ match: "all", rules: [{ field: "genre", op: "contains", value: "Horror" }] }],
      sort: "added_at",
      order: "desc",
    };
    await open(
      editSession({ title: "Horror", sectionType: "genre", config: structuredClone(config) }),
    );
    expect(screen.getByText("Genre (no longer offered)")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Kind of titles" })).toHaveTextContent(/^movies$/);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(save).toHaveBeenCalled());
    expect(JSON.stringify(savedDraft().config)).toBe(JSON.stringify(config));
  });

  it("starts a new rule row with no rules", async () => {
    const { dialog } = await open();
    await userEvent.click(within(dialog).getByRole("button", { name: "Titles matching rules" }));
    const form = await screen.findByRole("dialog", { name: "Titles matching rules" });
    expect(
      within(form).getByText("Describe the titles you want. New matches show up on their own."),
    ).toBeInTheDocument();
    expect(within(form).getByText(/No rules yet/)).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Add rule" }));
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0]![0].config).toMatchObject({
      library_ids: [],
      match: "all",
      groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "" }] }],
    });
  });
});

describe("Editor's Picks rows", () => {
  it("edits the titles and can't save an empty list", async () => {
    await open(
      editSession({
        title: "Staff picks",
        sectionType: "admin_curated_list",
        config: { item_ids: ["m1"] },
      }),
    );
    expect(await screen.findByText("Title m1")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Remove Title m1" }));
    expect(screen.getByText("Search above and add at least one title.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });
});
