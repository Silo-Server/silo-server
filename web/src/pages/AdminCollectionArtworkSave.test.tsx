import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Library, LibraryCollection } from "@/api/types";
import { v2Problem } from "@/api/v2/problems.test-support";
import { CollectionForm, CollectionEditForm } from "./adminCollectionsShared";

const mocks = vi.hoisted(() => ({ request: vi.fn(), warning: vi.fn(), error: vi.fn() }));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: vi.fn(),
}));
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), warning: mocks.warning, error: mocks.error },
}));
vi.mock("@/components/ImageUploadField", () => ({
  ImageUploadField: ({
    label,
    currentUrl,
    onDelete,
  }: {
    label: string;
    currentUrl?: string;
    onDelete?: () => void;
  }) => (
    <div>
      {currentUrl && <img alt={label} src={currentUrl} />}
      <button type="button" onClick={onDelete}>
        Remove {label}
      </button>
    </div>
  ),
}));
vi.mock("@/components/collections/CollectionBuilder", async () => ({
  ...(await vi.importActual<typeof import("@/components/collections/CollectionBuilder")>(
    "@/components/collections/CollectionBuilder",
  )),
  default: ({
    value,
    onChange,
    onSubmit,
    children,
  }: {
    value: { title: string };
    onChange: (value: unknown) => void;
    onSubmit: () => void;
    children: ReactNode;
  }) => (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
    >
      <input
        aria-label="Title"
        value={value.title}
        onChange={(event) => onChange({ ...value, title: event.target.value })}
      />
      {children}
      <button>Save Collection</button>
    </form>
  ),
}));
const collection = {
  id: "c",
  collection_type: "manual",
  library_id: 7,
  library_ids: [7],
  title: "Original",
  description: "",
  slug: "original",
  visibility: "visible",
  featured: false,
  sort_order: 0,
  group_id: null,
  poster_url: "https://example.test/poster",
  backdrop_url: "https://example.test/backdrop",
  source_url: "https://example.test/list",
  query_definition: {
    library_ids: [7],
    match: "all",
    groups: [],
    sort: { field: "title", order: "asc" },
  },
  sort_config: {},
  source_config: {},
  last_sync_status: "idle",
  last_sync_message: "",
  item_count: 0,
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
} as LibraryCollection;
let revision: number;
let writes: string[];
let failDelete: boolean;
beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  vi.clearAllMocks();
  revision = 1;
  writes = [];
  failDelete = false;
  mocks.request.mockImplementation(
    async (
      operation: string,
      args: { headers?: Record<string, string>; body?: Record<string, unknown> },
    ) => {
      if (operation === "GET /api/v2/admin/collections/capabilities")
        return { artwork: true, groups: true, imports: true, item_reorder: true };
      writes.push(operation);
      if (operation.startsWith("PATCH")) {
        if (args.headers?.["If-Match"] !== `"rev-${revision}"`)
          throw v2Problem(412, "precondition_failed", "Changed");
        revision++;
        return { ...collection, ...args.body, library_id: "7", library_ids: ["7"] };
      }
      if (operation.startsWith("DELETE")) {
        if (failDelete) throw new Error("Storage unavailable");
        revision++;
        return undefined;
      }
      throw new Error(`Unexpected operation ${operation}`);
    },
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function editor(kind: "manual" | "mdblist") {
  const onClose = vi.fn();
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const Form = kind === "manual" ? CollectionForm : CollectionEditForm;
  render(
    <QueryClientProvider client={client}>
      <Form
        etag={'"rev-1"'}
        collection={{ ...collection, collection_type: kind }}
        libraries={[{ id: 7, name: "Movies", type: "movies" } as Library]}
        initialLibraryId={7}
        onClose={onClose}
      />
    </QueryClientProvider>,
  );
  return onClose;
}
describe.each(["manual", "mdblist"] as const)("%s artwork removal", (kind) => {
  it.each(["Poster", "Backdrop"])(
    "removes %s with Save without invalidating its own captured version",
    async (label) => {
      const onClose = editor(kind);
      await screen.findByRole("img", { name: label });
      fireEvent.change(screen.getByLabelText("Title"), { target: { value: "My draft" } });
      fireEvent.click(screen.getByRole("button", { name: `Remove ${label}` }));
      await act(async () => {});
      fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));
      await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
      expect(writes).toEqual([
        "PATCH /api/v2/admin/collections/{id}",
        "DELETE /api/v2/admin/collections/{id}/image",
      ]);
      expect(mocks.error).not.toHaveBeenCalled();
      expect(screen.queryByRole("img", { name: label })).not.toBeInTheDocument();
    },
  );
  it("keeps removal local and preserves the draft when another editor changes the definition", async () => {
    const onClose = editor(kind);
    await screen.findByRole("img", { name: "Poster" });
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "My draft" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove Poster" }));
    await act(async () => {});
    expect(writes).toEqual([]);
    revision++;
    fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalled());
    expect(writes).toEqual(["PATCH /api/v2/admin/collections/{id}"]);
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Title")).toHaveValue("My draft");
  });
  it("reports removal failure after saving the definition without replaying that save", async () => {
    failDelete = true;
    const onClose = editor(kind);
    await screen.findByRole("img", { name: "Poster" });
    fireEvent.click(screen.getByRole("button", { name: "Remove Poster" }));
    fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(writes).toEqual([
      "PATCH /api/v2/admin/collections/{id}",
      "DELETE /api/v2/admin/collections/{id}/image",
    ]);
    expect(mocks.warning).toHaveBeenCalledWith(
      expect.stringContaining("Collection saved"),
      expect.anything(),
    );
  });
});
