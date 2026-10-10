import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Library } from "@/api/types";
import {
  coreApplied,
  coreAppliedAllFailed,
  coreDryRun,
  coreDryRunAllThere,
  coreDryRunWithHeroes,
  starterPackBundles,
  starterPackLibraries,
} from "@/test/fixtures/starterPacks";
import { goldens } from "@/test/fixtures/collectionBodies";
import { invalidateAdminCollectionQueries } from "@/hooks/queries/collectionSurfaceRefresh";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";
import { StarterPacksDialog } from "./StarterPacksDialog";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

installV2Recorder();

const DRY_RUN = "POST /api/v2/admin/collections/template-bundles/{bundle_id}/apply";
const QUEUE = "POST /api/v2/admin/collections/template-bundles/{bundle_id}/apply-job";
const JOB = "GET /api/v2/admin/collection-jobs/{job_id}";

function section(id: string, title: string, featured: boolean, libraryId: string | null) {
  return {
    id,
    scope: libraryId === null ? "home" : "library",
    library_id: libraryId,
    position: 0,
    section_type: "recently_added",
    title,
    featured,
    item_limit: 12,
    config: {},
    enabled: true,
    created_at: "2026-01-02T03:04:05.678Z",
    updated_at: "2026-01-02T03:04:05.678Z",
  };
}

/** Today's heroes: Recently Added on Home, New Episodes on the TV Shows page, none on Movies. */
const pages: Record<string, ReturnType<typeof section>[]> = {
  home: [section("h1", "Recently Added", true, null), section("h2", "Continue", false, null)],
  "library:1": [section("m1", "Movies Row", false, "1")],
  "library:2": [section("t1", "New Episodes", true, "2")],
};
const pageOf = (call: RecordedCall) =>
  call.query?.scope === "home" ? "home" : `library:${String(call.query?.library_id)}`;

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  HTMLElement.prototype.scrollIntoView = () => {};
  HTMLElement.prototype.hasPointerCapture = () => false;
  v2Recorder.answer("GET /api/v2/admin/collections/template-bundles", starterPackBundles);
  v2Recorder.answer("GET /api/v2/admin/sections/order", (call: RecordedCall) => ({
    scope: call.query?.scope,
    library_id: call.query?.library_id ?? null,
    ordered_ids: (pages[pageOf(call)] ?? []).map((row) => row.id),
  }));
  v2Recorder.answer("GET /api/v2/admin/sections", (call: RecordedCall) => ({
    items: pages[pageOf(call)] ?? [],
  }));
  v2Recorder.answer(DRY_RUN, (call: RecordedCall) =>
    (call.body as { featured?: unknown }).featured ? coreDryRunWithHeroes : coreDryRun,
  );
  v2Recorder.answer(JOB, {
    id: "collection-job",
    kind: "template_bundle_apply",
    state: "succeeded",
    terminal: true,
    cancelable: false,
    created_at: "2026-01-02T03:04:05.678Z",
    finished_at: "2026-01-02T03:04:06.678Z",
    template_result: coreApplied,
  });
});

afterEach(() => vi.unstubAllGlobals());

function renderDialog({
  libraries = starterPackLibraries,
  initialLibraryId = null,
}: { libraries?: Library[]; initialLibraryId?: number | null } = {}) {
  const onClose = vi.fn();
  const user = userEvent.setup();
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <StarterPacksDialog
          libraries={libraries}
          initialLibraryId={initialLibraryId}
          onClose={onClose}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { user, onClose, client };
}

async function checked() {
  return screen.findByText("Checked just now");
}

function table() {
  return screen.getByRole("table", { name: "What will happen" });
}

describe("Starter packs", () => {
  it("lists the packs with their counts and Everything last, and opens on the first", async () => {
    renderDialog();
    const rail = await screen.findByRole("tablist", { name: "Packs" });
    expect(rail).toHaveAttribute("aria-orientation", "vertical");
    expect(
      within(rail)
        .getAllByRole("tab")
        .map((tab) => tab.textContent),
    ).toEqual([
      "Core Defaults5 lists",
      "Popular Genres2 lists",
      "Franchise Collections1 list",
      "All Defaults8 lists",
    ]);
    expect(within(rail).getByRole("tab", { name: /Core Defaults/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByRole("heading", { name: "Core Defaults" })).toBeInTheDocument();
  });

  it("ties each pack tab to the pane it shows", async () => {
    const { user } = renderDialog();
    const rail = await screen.findByRole("tablist", { name: "Packs" });
    const tabs = within(rail).getAllByRole("tab");
    const panel = screen.getByRole("tabpanel", { name: /Core Defaults/ });
    for (const tab of tabs) expect(tab).toHaveAttribute("aria-controls", panel.id);
    expect(within(panel).getByRole("heading", { name: "Core Defaults" })).toBeInTheDocument();

    await user.click(within(rail).getByRole("tab", { name: /Popular Genres/ }));
    expect(screen.getByRole("tabpanel", { name: /Popular Genres/ })).toBe(panel);
  });

  it("shows what will happen per library from the dry run, tagging only the exceptions", async () => {
    renderDialog();
    await checked();
    const rows = within(table()).getAllByRole("row");
    expect(rows.map((row) => row.textContent)).toEqual([
      "LibraryNewAlready thereNot for this library",
      "Movies2 new1 pinned first12 TV lists",
      expect.stringContaining("Trending Movies This Week"),
      "TV Shows2 new1 pinned first03 movie lists",
    ]);
    expect(
      within(table())
        .getAllByRole("rowheader")
        .map((cell) => cell.textContent),
    ).toEqual(["Movies", "TV Shows"]);
    const movies = within(table()).getByRole("button", { name: "Movies" });
    expect(movies).toHaveAttribute("aria-expanded", "true");
    const list = screen.getByRole("list", { name: "Lists for Movies" });
    expect(
      within(list)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual([
      "Trending Movies This WeekPinned first",
      "Popular Movies",
      "Top Rated MoviesAlready there",
    ]);

    await userEvent.click(within(table()).getByRole("button", { name: "TV Shows" }));
    expect(screen.getByRole("list", { name: "Lists for TV Shows" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add 4 collections" })).toBeEnabled();
  });

  it("says where new lists land and that no sections are added", async () => {
    renderDialog();
    await checked();
    expect(screen.getByText(/New lists land under/)).toHaveTextContent(
      "New lists land under No heading on each library's Collections tab, ready to move into a shelf in Arrange. “Already there” lists aren't touched. No sections are added unless you turn on hero banners.",
    );
  });

  it("sends no featured member while the hero switch is off, then a fresh dry run and the job", async () => {
    const { user } = renderDialog();
    await checked();
    await user.click(screen.getByRole("button", { name: "Add 4 collections" }));
    await screen.findByRole("heading", { name: "Core Defaults added" });
    await waitFor(() => expect(v2Recorder.callsOf(DRY_RUN)).toHaveLength(3));
    expect(v2Recorder.writes()).toEqual(goldens.starterPackApply);
  });

  it("carries the chosen heroes in the dry run and the job when the switch is on", async () => {
    const { user } = renderDialog();
    await checked();
    expect(screen.getByRole("combobox", { name: "Hero banner on Home" })).toBeDisabled();
    await user.click(screen.getByRole("switch", { name: "Also use the pack's hero banners" }));
    await waitFor(() =>
      expect(within(table()).getByText("Hero banner on Home")).toBeInTheDocument(),
    );
    expect(
      within(table())
        .getAllByRole("row")
        .slice(-3)
        .map((row) => row.textContent),
    ).toEqual([
      "Hero banner on HomeTrending Movies This Week",
      "Hero banner on the Movies pageTrending Movies This Week",
      "Hero banner on the TV Shows pageTrending TV This Week",
    ]);
    expect(screen.getByText(/New lists land under/)).toHaveTextContent(
      "Hero banners are added to the pages you picked below.",
    );
    await user.click(screen.getByRole("button", { name: "Add 4 collections" }));
    await screen.findByRole("heading", { name: "Core Defaults added" });
    await waitFor(() => expect(within(table()).queryByText("Hero banner on Home")).toBeNull());
    expect(v2Recorder.writes()).toEqual(goldens.starterPackApplyWithHeroes);
  });

  it("turns the hero switch back off once the heroes are set", async () => {
    const { user } = renderDialog();
    await checked();
    const heroSwitch = screen.getByRole("switch", { name: "Also use the pack's hero banners" });
    await user.click(heroSwitch);
    await waitFor(() =>
      expect(within(table()).getByText("Hero banner on Home")).toBeInTheDocument(),
    );
    await user.click(screen.getByRole("button", { name: "Add 4 collections" }));
    await screen.findByRole("heading", { name: "Core Defaults added" });
    expect(heroSwitch).not.toBeChecked();
    await waitFor(() => expect(within(table()).queryByText("Hero banner on Home")).toBeNull());
    expect(screen.queryByRole("button", { name: "Set hero banners" })).toBeNull();
  });

  it("says in words why a hero banner can't be set", async () => {
    v2Recorder.answer(DRY_RUN, (call: RecordedCall) =>
      (call.body as { featured?: unknown }).featured
        ? {
            ...coreDryRunWithHeroes,
            featured: coreDryRunWithHeroes.featured.slice(1),
            featured_failed: [
              { ...coreDryRunWithHeroes.featured[0]!, reason: "collection_not_available" },
            ],
          }
        : coreDryRun,
    );
    const { user } = renderDialog();
    await checked();
    await user.click(screen.getByRole("switch", { name: "Also use the pack's hero banners" }));
    const home = await within(table()).findByRole("rowheader", { name: "Hero banner on Home" });
    expect(home.closest("tr")).toHaveTextContent(
      "Trending Movies This Week · Can't set it: its list wasn't added to that library",
    );
    expect(table()).not.toHaveTextContent("collection_not_available");
  });

  it("locks the pack, its libraries and its heroes while the pack is being added", async () => {
    v2Recorder.answer(JOB, {
      id: "collection-job",
      kind: "template_bundle_apply",
      state: "running",
      terminal: false,
      cancelable: false,
      created_at: "2026-01-02T03:04:05.678Z",
    });
    const { user } = renderDialog();
    await checked();
    await user.click(screen.getByRole("switch", { name: "Also use the pack's hero banners" }));
    await user.click(screen.getByRole("button", { name: "Add 4 collections" }));
    expect(await screen.findByText(/Adding Core Defaults…/)).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Movies" })).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: "TV Shows" })).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Also use the pack's hero banners" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Hero banner on Home" })).toBeDisabled();
    expect(screen.getByRole("tab", { name: /Popular Genres/ })).toBeDisabled();
    expect(screen.getByRole("tab", { name: /Core Defaults/ })).toBeEnabled();
    // The active tab stays focusable but can't reset the choices being applied.
    await user.click(screen.getByRole("tab", { name: /Core Defaults/ }));
    expect(screen.getByRole("switch", { name: "Also use the pack's hero banners" })).toBeChecked();
  });

  it("leaves its check alone when the page refreshes the admin collections", async () => {
    // The page refreshes them when a job ends, while the hero switch may be
    // turning off; re-running the check it leaves would be wasted.
    const { client } = renderDialog();
    await checked();
    await invalidateAdminCollectionQueries(client);
    expect(v2Recorder.callsOf(DRY_RUN)).toHaveLength(1);
  });

  it("names the hero each page line replaces, and lets a page keep its current one", async () => {
    const { user } = renderDialog();
    await checked();
    await user.click(screen.getByRole("switch", { name: "Also use the pack's hero banners" }));
    await waitFor(() =>
      expect(screen.getByTestId("hero-home")).toHaveTextContent("replaces Recently Added"),
    );
    expect(screen.getByTestId("hero-library-2")).toHaveTextContent("replaces New Episodes");
    expect(screen.getByTestId("hero-library-1")).toHaveTextContent("No hero banner there now");

    await user.click(screen.getByRole("combobox", { name: "Hero banner on the TV Shows page" }));
    await user.click(await screen.findByRole("option", { name: "Keep current" }));
    expect(screen.getByTestId("hero-library-2")).toHaveTextContent("Keeps New Episodes");
    await user.click(screen.getByRole("button", { name: "Add 4 collections" }));
    await screen.findByRole("heading", { name: "Core Defaults added" });
    const jobs = v2Recorder.callsOf(QUEUE);
    expect(jobs).toHaveLength(1);
    expect(jobs[0]!.body).toEqual({
      library_ids: ["1", "2"],
      delete_existing: false,
      featured: {
        home: { library_id: "1", template_id: "tmdb_trending_movies_week" },
        libraries: { "1": "tmdb_trending_movies_week" },
      },
    });
  });

  it("never offers to delete existing collections", async () => {
    const { user } = renderDialog();
    await checked();
    expect(screen.queryByText(/delete/i)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Add 4 collections" }));
    await screen.findByRole("heading", { name: "Core Defaults added" });
    for (const call of [...v2Recorder.callsOf(DRY_RUN), ...v2Recorder.callsOf(QUEUE)]) {
      expect((call.body as { delete_existing: boolean }).delete_existing).toBe(false);
    }
  });

  it("names Discover and Franchise lists from the pack summaries, never the template catalog", async () => {
    const { user } = renderDialog();
    await checked();
    await user.click(screen.getByRole("tab", { name: /Popular Genres/ }));
    expect(await screen.findByRole("heading", { name: "Popular Genres" })).toBeInTheDocument();
    await user.click(screen.getByRole("switch", { name: "Also use the pack's hero banners" }));
    await user.click(screen.getByRole("combobox", { name: "Hero banner on the Movies page" }));
    expect(await screen.findByRole("option", { name: "Popular Horror Movies" })).toBeTruthy();
    expect(v2Recorder.operations()).not.toContain("GET /api/v2/admin/collections/templates");
  });

  it("never shows a template that needs setup", async () => {
    const { user } = renderDialog();
    await checked();
    await user.click(screen.getByRole("tab", { name: /Franchise Collections/ }));
    await screen.findByRole("heading", { name: "Franchise Collections" });
    await user.click(screen.getByRole("switch", { name: "Also use the pack's hero banners" }));
    await user.click(screen.getByRole("combobox", { name: "Hero banner on the Movies page" }));
    expect(await screen.findByRole("option", { name: "Star Wars" })).toBeTruthy();
    expect(screen.queryByText("TMDB Franchise")).toBeNull();
  });

  it("disables Add and offers Retry when the dry run fails", async () => {
    let fail = true;
    v2Recorder.answer(DRY_RUN, (call: RecordedCall) => {
      if (fail) throw new Error(`${call.operation} failed`);
      return coreDryRun;
    });
    const { user } = renderDialog();
    expect(await screen.findByText("Couldn't check this pack")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Add/ })).toBeDisabled();
    fail = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await checked();
    expect(screen.getByRole("button", { name: "Add 4 collections" })).toBeEnabled();
  });

  it("says there is nothing new to add when every list is already there", async () => {
    v2Recorder.answer(DRY_RUN, coreDryRunAllThere);
    renderDialog({ initialLibraryId: 1 });
    await checked();
    expect(screen.getByRole("button", { name: "Nothing new to add" })).toBeDisabled();
  });

  it("links to Libraries when no library can take the pack", async () => {
    renderDialog({ libraries: [starterPackLibraries[2]!] });
    expect(await screen.findByText("None of your libraries can take this pack.")).toBeVisible();
    expect(screen.getByRole("link", { name: "Libraries" })).toHaveAttribute(
      "href",
      "/admin/libraries",
    );
    expect(screen.getByRole("button", { name: /^Add/ })).toBeDisabled();
    expect(v2Recorder.callsOf(DRY_RUN)).toEqual([]);
  });

  it("checks again with the libraries ticked", async () => {
    const { user } = renderDialog();
    await checked();
    await user.click(screen.getByRole("checkbox", { name: "TV Shows" }));
    await waitFor(() =>
      expect(v2Recorder.callsOf(DRY_RUN).at(-1)?.body).toEqual({
        library_ids: ["1"],
        dry_run: true,
        delete_existing: false,
      }),
    );
    expect(screen.queryByRole("checkbox", { name: "Audiobooks" })).toBeNull();
  });

  it("stays open after Add, tags the pack Added and moves focus to the result", async () => {
    const { user, onClose } = renderDialog();
    await checked();
    await user.click(screen.getByRole("button", { name: "Add 4 collections" }));
    const heading = await screen.findByRole("heading", { name: "Core Defaults added" });
    const summary = heading.closest("section")!;
    await waitFor(() => expect(summary).toHaveFocus());
    expect(summary).toHaveTextContent("Added 2 lists to Movies.");
    expect(summary).toHaveTextContent("Added 1 list to TV Shows.");
    expect(summary).toHaveTextContent("Couldn't add Popular TV to TV Shows: something went wrong.");
    expect(screen.getByRole("tab", { name: /Core Defaults/ })).toHaveTextContent("Added");
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Starter packs" })).toBeInTheDocument();
  });

  it("doesn't tag the pack Added when nothing new landed and lists failed", async () => {
    v2Recorder.answer(JOB, {
      id: "collection-job",
      kind: "template_bundle_apply",
      state: "succeeded",
      terminal: true,
      cancelable: false,
      created_at: "2026-01-02T03:04:05.678Z",
      finished_at: "2026-01-02T03:04:06.678Z",
      template_result: coreAppliedAllFailed,
    });
    const { user } = renderDialog();
    await checked();
    await user.click(screen.getByRole("button", { name: "Add 4 collections" }));
    const heading = await screen.findByRole("heading", {
      name: "Core Defaults finished with problems",
    });
    expect(heading.closest("section")).toHaveTextContent(
      "Couldn't add Popular Movies to Movies: something went wrong.",
    );
    expect(screen.getByRole("tab", { name: /Core Defaults/ })).not.toHaveTextContent("Added");
  });

  it("adds a note about sync load above 30 new lists", async () => {
    const many = Array.from({ length: 31 }, (_, index) => ({
      template_id: `tmdb_popular_movies`,
      template_title: `List ${index}`,
      library_id: "1",
      library_name: "Movies",
      reason: "would_create",
    }));
    v2Recorder.answer(DRY_RUN, { ...coreDryRun, created: many, skipped: [] });
    renderDialog();
    expect(
      await screen.findByText(
        "That's 31 new lists. Their first syncs run in the background and can take a while to finish.",
      ),
    ).toBeInTheDocument();
  });

  it("picks the pack from a select on phones", async () => {
    vi.stubGlobal(
      "matchMedia",
      (query: string) =>
        ({
          matches: query === "(max-width: 1023px)",
          media: query,
          addEventListener() {},
          removeEventListener() {},
        }) as unknown as MediaQueryList,
    );
    const { user } = renderDialog();
    const select = await screen.findByRole("combobox", { name: "Pack" });
    expect(screen.queryByRole("tablist", { name: "Packs" })).toBeNull();
    await user.click(select);
    await user.click(await screen.findByRole("option", { name: "Popular Genres · 2 lists" }));
    expect(await screen.findByRole("heading", { name: "Popular Genres" })).toBeInTheDocument();
  });
});
