import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { components } from "@/api/v2/schema";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";

import {
  answerAdminCollections,
  storedAdminCollection as stored,
} from "./adminCollections.test-support";

import { starterPackBlocksDelete } from "@/lib/collections/copy";

import AdminCollections from "./AdminCollections";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [
      { id: 1, name: "Movies" },
      { id: 2, name: "Kids" },
      { id: 3, name: "TV Shows" },
    ],
  }),
}));
/** Starter pack jobs the page sees; `set` re-renders it as a job starts. */
const applyJobs = vi.hoisted(() => {
  let jobs: unknown[] = [];
  const listeners = new Set<() => void>();
  return {
    get: () => jobs,
    set(next: unknown[]) {
      jobs = next;
      listeners.forEach((listener) => listener());
    },
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => void listeners.delete(listener);
    },
  };
});
vi.mock("@/hooks/queries/admin/taskJobs", async () => {
  const { useSyncExternalStore } = await import("react");
  return {
    useAdminTaskJobs: () => ({
      data: useSyncExternalStore(applyJobs.subscribe, applyJobs.get),
    }),
  };
});
vi.mock("@/components/realtimeEventsContext", () => ({ useEventChannel: vi.fn() }));
vi.mock("@/components/collections/StarterPacksDialog", () => ({
  StarterPacksDialog: () => null,
}));

installV2Recorder();

type AdminCollection = components["schemas"]["AdminCollection"];

let items: AdminCollection[];

function renderPage(path = "/admin/collections", client = new QueryClient()) {
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/admin/collections" element={<AdminCollections />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const bar = () => screen.getByRole("group", { name: "Selected collections" });
const pathsOf = (operation: string) => v2Recorder.callsOf(operation).map((call) => call.path);

async function enterSelectMode(user = userEvent.setup()) {
  await screen.findByText("Studio Ghibli");
  await user.click(screen.getByRole("button", { name: "More" }));
  await user.click(await screen.findByRole("menuitem", { name: "Select collections" }));
  return user;
}

async function pick(user: ReturnType<typeof userEvent.setup>, ...titles: string[]) {
  for (const title of titles)
    await user.click(screen.getByRole("checkbox", { name: `Select ${title}` }));
}

/** Holds each `operation` until the returned function answers the latest with `answer`. */
function hold(operation: string, answer?: unknown) {
  let finish!: () => void;
  v2Recorder.answer(operation, () => new Promise((resolve) => (finish = () => resolve(answer))));
  return () => finish();
}

beforeEach(() => {
  applyJobs.set([]);
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      readonly root = null;
      readonly rootMargin = "0px";
      readonly thresholds = [0];
      observe = () => undefined;
      unobserve = () => undefined;
      disconnect = () => undefined;
      takeRecords = () => [];
    },
  );
  items = [
    stored("Best Picture Winners", {
      collection_type: "mdblist",
      library_ids: ["1", "2"],
      item_count: 96,
      home_row_count: 1,
      row_count: 1,
    }),
    stored("Christmas Classics", { collection_type: "smart", item_count: 41 }),
    stored("Netflix Originals", {
      collection_type: "tmdb",
      library_ids: ["1", "3"],
      item_count: 182,
    }),
    stored("Staff Picks", { visibility: "hidden", row_count: 2, item_count: 14 }),
    stored("Studio Ghibli", { library_ids: ["1", "2"], item_count: 23 }),
    stored("Trending This Week", { collection_type: "tmdb", item_count: 20, row_count: 2 }),
  ];
  answerAdminCollections(() => items);
});

describe("AdminCollections Select collections", () => {
  it("refuses more than 100 at a time", async () => {
    items = Array.from({ length: 100 }, (_, index) => stored(`List ${index + 1}`));
    items.push(stored("Studio Ghibli"));
    renderPage();
    const user = await enterSelectMode();
    await user.click(screen.getByRole("checkbox", { name: "Select all" }));
    expect(within(bar()).getByRole("status")).toHaveTextContent(
      "101 selectedSelect up to 100 collections at a time.",
    );
    expect(within(bar()).getByRole("button", { name: "Hide from tabs" })).toBeDisabled();
  });

  it("keeps at most four syncs in flight", async () => {
    items = Array.from({ length: 7 }, (_, index) =>
      stored(`Chart ${index + 1}`, { collection_type: "tmdb" }),
    );
    items.push(stored("Studio Ghibli"));
    const answers: Array<() => void> = [];
    let inFlight = 0;
    let most = 0;
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", () => {
      inFlight++;
      most = Math.max(most, inFlight);
      return new Promise((resolve) =>
        answers.push(() => {
          inFlight--;
          resolve({ status: "success", message: "", items_matched: 1 });
        }),
      );
    });
    renderPage();
    const user = await enterSelectMode();
    await user.click(screen.getByRole("checkbox", { name: "Select all" }));
    await user.click(within(bar()).getByRole("button", { name: "Sync 7 lists" }));
    await waitFor(() => expect(answers).toHaveLength(4));
    // Busy while it runs: a second press can't start another batch.
    expect(within(bar()).getByRole("button", { name: "Sync 7 lists" })).toBeDisabled();
    while (answers.length > 0 || inFlight > 0) {
      await act(async () => answers.shift()?.());
    }
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Synced 7 lists."));
    expect(v2Recorder.callsOf("POST /api/v2/admin/collections/{id}/sync")).toHaveLength(7);
    expect(most).toBe(4);
  });

  it("reports a partial failure, naming each list it couldn't sync", async () => {
    v2Recorder.answer("POST /api/v2/admin/collections/{id}/sync", (call: RecordedCall) => {
      if (call.path.includes("netflix")) throw new Error("TMDB answered 401");
      return { status: "success", message: "", items_matched: 3 };
    });
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Best Picture Winners", "Netflix Originals");
    await user.click(within(bar()).getByRole("button", { name: "Sync 2 lists" }));
    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith("Synced 1 of 2 lists.", {
        description: "Netflix Originals: TMDB answered 401",
      }),
    );
  });

  it("shows only the hidden ones it picked, each with its type and a fresh ETag", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Staff Picks", "Studio Ghibli");
    await user.click(within(bar()).getByRole("button", { name: "Show on tabs" }));
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("Showed 1 collection on Collections tabs."),
    );
    const patches = v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}");
    expect(patches).toHaveLength(1);
    expect(patches[0]!.path).toBe("/api/v2/admin/collections/staff-picks");
    expect(patches[0]!.headers["If-Match"]).toBe('"/api/v2/admin/collections/staff-picks#1"');
    expect(patches[0]!.body).toEqual({ collection_type: "manual", visibility: "visible" });
  });

  it("hides at once when no row uses what it hides", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Studio Ghibli", "Christmas Classics", "Staff Picks");
    await user.click(within(bar()).getByRole("button", { name: "Hide from tabs" }));
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("Hid 2 collections from Collections tabs."),
    );
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(pathsOf("PATCH /api/v2/admin/collections/{id}").sort()).toEqual([
      "/api/v2/admin/collections/christmas-classics",
      "/api/v2/admin/collections/studio-ghibli",
    ]);
  });

  it("checks the latest row count before hiding and sends nothing on Cancel", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Best Picture Winners", "Studio Ghibli");
    // Another admin adds two rows for Studio Ghibli after the list loads.
    items = items.map((entry) =>
      entry.title === "Studio Ghibli" ? { ...entry, row_count: 2 } : entry,
    );
    await user.click(within(bar()).getByRole("button", { name: "Hide from tabs" }));
    const dialog = await screen.findByRole("alertdialog", {
      name: "Hide 2 collections from Collections tabs?",
    });
    expect(dialog).toHaveTextContent(
      "3 rows still show them, but those rows' See all won't open while they're hidden.",
    );
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(v2Recorder.writes()).toEqual([]);

    await user.click(within(bar()).getByRole("button", { name: "Hide from tabs" }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Hide" }),
    );
    await waitFor(() =>
      expect(pathsOf("PATCH /api/v2/admin/collections/{id}").sort()).toEqual([
        "/api/v2/admin/collections/best-picture-winners",
        "/api/v2/admin/collections/studio-ghibli",
      ]),
    );
  });

  it("hides nothing and says so when the rows that use them can't be read", async () => {
    vi.mocked(toast.error).mockClear();
    renderPage(
      "/admin/collections",
      new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    );
    const user = await enterSelectMode();
    v2Recorder.answer("GET /api/v2/admin/collections", () => {
      throw new Error("Server unavailable");
    });
    await pick(user, "Studio Ghibli", "Christmas Classics");
    await user.click(within(bar()).getByRole("button", { name: "Hide from tabs" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("deletes only what rows currently don't use, and names the ones it keeps", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Best Picture Winners", "Trending This Week", "Studio Ghibli");
    // Another admin removes the last row for Best Picture Winners after the list loads.
    items = items.map((entry) =>
      entry.title === "Best Picture Winners"
        ? { ...entry, row_count: 0, home_row_count: 0 }
        : entry,
    );
    await user.click(within(bar()).getByRole("button", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete 2 collections?" });
    expect(dialog).toHaveTextContent(
      "They're removed for everyone. Their titles stay in your libraries.",
    );
    expect(dialog).toHaveTextContent("1 is kept because rows use it: Trending This Week.");
    expect(dialog).toHaveTextContent("This can't be undone.");
    await user.click(within(dialog).getByRole("button", { name: "Delete 2" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());

    const removes = v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}");
    expect(removes.map((call) => call.path).sort()).toEqual([
      "/api/v2/admin/collections/best-picture-winners",
      "/api/v2/admin/collections/studio-ghibli",
    ]);
    for (const call of removes) expect(call.headers["If-Match"]).toBe(`"${call.path}#1"`);
  });

  it("deletes nothing and says why when rows use everything picked", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Trending This Week", "Staff Picks");
    await user.click(within(bar()).getByRole("button", { name: "Delete…" }));
    expect(toast.error).toHaveBeenCalledWith("Rows still use them. Remove the rows first.");
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(v2Recorder.writes()).toEqual([]);
  });
});

describe("AdminCollections Delete all while select mode works", () => {
  it("waits for a row's own switch to save", async () => {
    const finish = hold("PATCH /api/v2/admin/collections/{id}");
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Studio Ghibli");
    await user.click(
      screen.getByRole("switch", {
        name: "Show Studio Ghibli on the Movies and Kids Collections tabs",
      }),
    );
    await waitFor(() =>
      expect(v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}")).toHaveLength(1),
    );
    // A bar action now would read the ETag the switch's PATCH is about to change.
    expect(within(bar()).getByRole("button", { name: "Show on tabs" })).toBeDisabled();
    expect(within(bar()).getByRole("button", { name: "Delete…" })).toBeDisabled();
    await act(async () => finish());
    await waitFor(() =>
      expect(within(bar()).getByRole("button", { name: "Show on tabs" })).toBeEnabled(),
    );
  });

  it("waits for a Show or Hide to finish", async () => {
    const finish = hold("PATCH /api/v2/admin/collections/{id}");
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Staff Picks");
    await user.click(within(bar()).getByRole("button", { name: "Show on tabs" }));
    await waitFor(() =>
      expect(v2Recorder.callsOf("PATCH /api/v2/admin/collections/{id}")).toHaveLength(1),
    );
    await user.click(screen.getByRole("button", { name: "More" }));
    expect(
      await screen.findByRole("menuitem", { name: "Delete all in this view…" }),
    ).toHaveAttribute("aria-disabled", "true");
    await user.keyboard("{Escape}");

    await act(async () => finish());
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("Showed 1 collection on Collections tabs."),
    );
    await user.click(screen.getByRole("button", { name: "More" }));
    expect(
      await screen.findByRole("menuitem", { name: "Delete all in this view…" }),
    ).not.toHaveAttribute("aria-disabled");
  });
});

describe("AdminCollections Select collections delete", () => {
  it("holds the confirm open, and says why, when a starter pack starts adding", async () => {
    renderPage();
    const user = await enterSelectMode();
    await pick(user, "Studio Ghibli");
    await user.click(within(bar()).getByRole("button", { name: "Delete…" }));
    const name = "Delete 1 manual collection?";
    const dialog = await screen.findByRole("alertdialog", { name });
    act(() =>
      applyJobs.set([
        {
          id: "job-1",
          job_type: "template_bundle_apply",
          status: "running",
          message: "Adding",
          requested_at: new Date().toISOString(),
        },
      ]),
    );
    expect(within(dialog).getByRole("alert")).toHaveTextContent(starterPackBlocksDelete());
    const confirm = within(dialog).getByRole("button", { name: "Delete 1" });
    expect(confirm).toBeDisabled();
    await user.click(confirm);
    expect(screen.getByRole("alertdialog", { name })).toBeInTheDocument();
    expect(pathsOf("DELETE /api/v2/admin/collections/{id}")).toEqual([]);
  });
});

describe("AdminCollections Delete all in this view", () => {
  it("counts only what goes, names the lists it keeps, and where shared lists also go", async () => {
    renderPage("/admin/collections?view=list&libraryId=1&type=synced");
    const user = userEvent.setup();
    await screen.findByText("Netflix Originals");
    await user.click(screen.getByRole("button", { name: "More" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete all in this view…" }));
    const dialog = await screen.findByRole("alertdialog", {
      name: "Delete 1 synced list in Movies?",
    });
    expect(dialog).toHaveTextContent(
      "It's removed for everyone. Its titles stay in your libraries.",
    );
    expect(dialog).toHaveTextContent(
      "A synced list that's also in another library goes there too: Netflix Originals (TV Shows).",
    );
    expect(dialog).toHaveTextContent(
      "2 are kept because rows use them: Best Picture Winners and Trending This Week.",
    );
    // Only what will go is read for its ETag.
    expect(pathsOf("GET /api/v2/admin/collections/{id}")).toEqual([
      "/api/v2/admin/collections/netflix-originals",
    ]);
    await user.click(within(dialog).getByRole("button", { name: "Delete 1" }));
    await waitFor(() =>
      expect(pathsOf("DELETE /api/v2/admin/collections/{id}")).toEqual([
        "/api/v2/admin/collections/netflix-originals",
      ]),
    );
  });
});
