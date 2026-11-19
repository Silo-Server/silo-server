/**
 * Where it shows on a server collection's editor: the Home and library page
 * rows that show it, Add as a row into Home rows (asking first about unsaved
 * changes), hiding a collection rows use, and deleting it with its rows.
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { changedBeforeDelete } from "@/lib/collections/copy";
import {
  adminCapabilities,
  adminCollection,
  adminCollectionList,
} from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";
import { location, prepareEditorPage, showEditorPage } from "./collectionEditorPage.test-support";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("@/hooks/useDebounce", () => ({ useDebounce: (value: string) => value }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner", name: "Sam" }, isLoading: false }),
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [
      { id: 1, name: "Movies", type: "movies" },
      { id: 2, name: "Kids", type: "movies" },
      { id: 3, name: "4K Movies", type: "movies" },
    ],
  }),
}));

installV2Recorder();

const GHIBLI = { ...adminCollection, library_ids: ["1", "2"] };

function section(id: string, overrides: Record<string, unknown>) {
  return {
    id,
    scope: "home",
    library_id: null,
    section_type: "collection",
    title: id,
    featured: false,
    enabled: true,
    position: 0,
    page_row_count: 1,
    config: { library_collection_id: "c1" },
    ...overrides,
  };
}

const HOME_ROW = section("s-home", { title: "Studio Ghibli", position: 5, page_row_count: 9 });
const KIDS_ROW = section("s-kids", {
  scope: "library",
  library_id: "2",
  title: "Ghibli Favorites",
  position: 2,
  page_row_count: 7,
});

let rows: Array<ReturnType<typeof section>>;

/** Each page's rows in order: `count` rows, with `rows` at their stored positions. */
function pageOrder(call: RecordedCall) {
  const libraryId = call.query?.library_id ?? null;
  const count = libraryId === null ? 9 : 7;
  const ids = Array.from({ length: count }, (_, index) => `other-${index}`);
  for (const row of rows) {
    if (row.library_id === libraryId) ids[row.position] = row.id;
  }
  return { ordered_ids: ids };
}

beforeEach(() => {
  prepareEditorPage();
  rows = [HOME_ROW, KIDS_ROW];
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", {
    ...adminCapabilities,
    section_references: true,
  });
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", GHIBLI);
  v2Recorder.answer("PATCH /api/v2/admin/collections/{id}", (call: RecordedCall) => ({
    ...GHIBLI,
    ...(call.body as object),
  }));
  v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(GHIBLI));
  v2Recorder.answer("GET /api/v2/admin/collections/{id}/items", {
    items: [],
    page: { has_more: false },
  });
  v2Recorder.answer("GET /api/v2/admin/collections/{id}/items/order", {
    ordered_ids: [],
    has_more: false,
  });
  v2Recorder.answer("POST /api/v2/catalog/query", {
    items: [],
    page: { has_more: false },
    total: 0,
    total_exact: true,
    effective_sort: { field: "relevance", order: "desc" },
  });
  v2Recorder.answer("GET /api/v2/admin/collections/{id}/sections", () => ({ items: rows }));
  v2Recorder.answer("GET /api/v2/admin/sections/order", pageOrder);
  v2Recorder.answer("GET /api/v2/admin/sections/{id}", (call: RecordedCall) =>
    rows.find((row) => call.path.endsWith(`/${row.id}`)),
  );
  v2Recorder.answer("DELETE /api/v2/admin/sections/{id}", (call: RecordedCall) => {
    rows = rows.filter((row) => !call.path.endsWith(`/${row.id}`));
  });
  v2Recorder.answer("DELETE /api/v2/admin/collections/{id}", undefined);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const EDITOR = "/admin/collections/c1/edit?libraryId=1";
const showPage = (entry: Parameters<typeof showEditorPage>[0]) =>
  showEditorPage(entry, { where: true });

describe("deleting a collection rows show", () => {
  async function openDelete(user: ReturnType<typeof userEvent.setup>) {
    await screen.findByText(/On Home and the Kids page/);
    await user.click(screen.getByRole("button", { name: "More actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete…" }));
    return screen.findByRole("alertdialog", { name: 'Delete "Original"?' });
  }

  it("leaves a row that now shows another collection, and still deletes the collection", async () => {
    v2Recorder.answer("GET /api/v2/admin/sections/{id}", (call: RecordedCall) => {
      const row = rows.find((candidate) => call.path.endsWith(`/${candidate.id}`))!;
      return row.id === "s-kids" ? { ...row, config: { library_collection_id: "c2" } } : row;
    });
    const user = userEvent.setup();
    showPage(EDITOR);
    const dialog = await openDelete(user);
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/collections(\?|$)/));
    expect(
      v2Recorder.callsOf("DELETE /api/v2/admin/sections/{id}").map((call) => call.path),
    ).toEqual(["/api/v2/admin/sections/s-home"]);
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(1);
  });

  it("deletes no row when the collection changed since the editor read it", async () => {
    const user = userEvent.setup();
    showPage(EDITOR);
    const dialog = await openDelete(user);
    v2Recorder.bump("/api/v2/admin/collections/c1");
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(changedBeforeDelete());
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/sections/{id}")).toHaveLength(0);
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(0);
    // The editor read it again, so the next Delete sends the new token.
    const changed = v2Recorder.etag("/api/v2/admin/collections/c1");
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    await waitFor(() => expect(location()).toMatch(/^\/admin\/collections(\?|$)/));
    const [remove] = v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}");
    expect(remove!.headers["If-Match"]).toBe(changed);
  });

  it("stops before the collection when a row can't be deleted, and names what's left", async () => {
    v2Recorder.answer("DELETE /api/v2/admin/sections/{id}", (call: RecordedCall) => {
      if (call.path.endsWith("/s-kids")) throw new Error("Server error");
      rows = rows.filter((row) => !call.path.endsWith(`/${row.id}`));
    });
    const user = userEvent.setup();
    showPage(EDITOR);
    const dialog = await openDelete(user);
    await user.click(within(dialog).getByRole("button", { name: "Delete it and its 2 rows" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "Couldn't delete every row, so the collection was kept. Still showing it: Ghibli Favorites (Kids page).",
    );
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(0);
    expect(
      await within(dialog).findByRole("button", { name: "Delete it and its 1 row" }),
    ).toBeEnabled();
  });
});
