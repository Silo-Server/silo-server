import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import type { LibraryCollection } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import type { CollectionBuilderProps } from "@/components/collections/CollectionBuilder";
import CollectionEditor from "./CollectionEditor";
import AdminCollectionEditor from "./AdminCollectionEditor";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  adminCreate: vi.fn(),
  collection: null as LibraryCollection | null,
  editSnapshotError: null as Error | null,
  adminSnapshotError: null as Error | null,
}));
vi.mock("@/hooks/queries/collections", () => ({
  useCollections: () => ({ data: [] }),
  useCollectionEditSnapshot: () => ({
    data: undefined,
    isLoading: false,
    isFetching: false,
    error: mocks.editSnapshotError,
    refetch: vi.fn(),
  }),
  useCollectionCapabilities: () => ({ data: {} }),
  useCreateCollection: () => ({ mutate: mocks.create }),
  useUpdateCollection: () => ({}),
  useDeleteUserCollectionImage: () => ({}),
}));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollections: () => ({
    data: mocks.collection ? [mocks.collection] : [],
    isLoading: false,
  }),
  useAdminCollectionSnapshot: () => ({
    data: mocks.collection ? { collection: mocks.collection, etag: '"revision"' } : undefined,
    isLoading: false,
    isFetching: false,
    error: mocks.adminSnapshotError,
    refetch: vi.fn(),
  }),
  useAdminCollectionCapabilities: () => ({ data: {} }),
  useCreateAdminCollection: () => ({ mutate: mocks.adminCreate }),
  useUpdateAdminCollection: () => ({}),
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/libraries", () => ({ useUserLibraries: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p" } }),
}));
vi.mock("@/components/CollectionTemplateGallery", () => ({
  CollectionTemplateGallery: () => null,
}));
vi.mock("@/components/ImageUploadField", () => ({ ImageUploadField: () => null }));
vi.mock("./SmartCollectionWizard", () => ({ default: () => <div>Smart wizard</div> }));
vi.mock("@/components/collections/CollectionBuilder", async () => ({
  ...(await vi.importActual<typeof import("@/components/collections/CollectionBuilder")>(
    "@/components/collections/CollectionBuilder",
  )),
  default: ({ value, onChange, onSubmit, lockCollectionType }: CollectionBuilderProps) => (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
    >
      <input
        aria-label="Name"
        value={value.title}
        onChange={(event) => onChange({ ...value, title: event.target.value })}
      />
      <select
        aria-label="Collection Mode"
        disabled={lockCollectionType}
        value={value.collection_type}
        onChange={(event) =>
          onChange({ ...value, collection_type: event.target.value as "manual" | "smart" })
        }
      >
        <option value="smart">Smart</option>
        <option value="manual">Manual</option>
      </select>
      <button>Save Collection</button>
    </form>
  ),
}));
vi.mock("@/components/collections/ManualCollectionItemsEditor", () => ({
  ManualCollectionItemsEditor: ({
    collectionId,
    source,
  }: {
    collectionId: string;
    source: string;
  }) => <div data-testid="manual-items" data-source={source} data-collection={collectionId} />,
}));
beforeEach(() => {
  vi.clearAllMocks();
  mocks.collection = null;
  mocks.editSnapshotError = null;
  mocks.adminSnapshotError = null;
});
function show(admin = false, edit = false) {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={[edit ? "/collection-1/edit" : "/new"]}>
        <Routes>
          <Route path="/new" element={admin ? <AdminCollectionEditor /> : <CollectionEditor />} />
          <Route path="/:id/edit" element={<AdminCollectionEditor />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
it("creates a personal manual collection from the new collection route", () => {
  show();
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "My picks" } });
  fireEvent.change(screen.getByLabelText("Collection Mode"), { target: { value: "manual" } });
  fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));
  expect(mocks.create).toHaveBeenCalledWith(
    expect.objectContaining({
      body: expect.objectContaining({
        name: "My picks",
        collection_type: "manual",
        query_definition: undefined,
      }),
    }),
    expect.anything(),
  );
});
it("opens a manual admin form and submits a manual collection after selecting Manual", () => {
  show(true);
  fireEvent.click(screen.getByRole("button", { name: /Manual Curate items by hand/ }));
  expect(screen.getByLabelText("Collection Mode")).toHaveValue("manual");
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Staff picks" } });
  fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));
  expect(mocks.adminCreate).toHaveBeenCalledWith(
    expect.objectContaining({
      body: expect.objectContaining({
        title: "Staff picks",
        collection_type: "manual",
        query_definition: undefined,
      }),
    }),
    expect.anything(),
  );
});

it("mounts the library item picker outside the metadata form for a saved manual collection", () => {
  mocks.collection = {
    id: "collection-1",
    title: "Staff picks",
    collection_type: "manual",
    library_ids: [1],
  } as LibraryCollection;
  show(true, true);
  const editor = screen.getByTestId("manual-items");
  expect(editor).toHaveAttribute("data-source", "library");
  expect(editor).toHaveAttribute("data-collection", "collection-1");
  expect(editor.closest("form")).toBeNull();
});

function showPersonalEdit() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={["/collections/collection-1/edit"]}>
        <Routes>
          <Route path="/collections/:id/edit" element={<CollectionEditor />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function collectionProblem(status: number) {
  return new V2ProblemError("getPersonalCollection", {
    type: `https://silo.example/problems/${status === 404 ? "not_found" : "internal_error"}`,
    title: status === 404 ? "Not Found" : "Internal Server Error",
    status,
    detail: status === 404 ? "Collection not found." : "Collections are unavailable.",
    instance: "/api/v2/collections/collection-1",
  });
}

it("points a missing personal collection back to the collection list", () => {
  mocks.editSnapshotError = collectionProblem(404);
  showPersonalEdit();
  expect(
    screen.getByRole("heading", { level: 1, name: "This collection isn't available" }),
  ).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "All collections" })).toHaveAttribute(
    "href",
    "/collections",
  );
});

it("offers a retry when a personal collection fails to load", () => {
  mocks.editSnapshotError = collectionProblem(500);
  showPersonalEdit();
  expect(
    screen.getByRole("heading", { level: 1, name: "Couldn't load this collection" }),
  ).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
});

it("points a missing admin collection back to the collection board", () => {
  mocks.adminSnapshotError = collectionProblem(404);
  show(true, true);
  expect(
    screen.getByRole("heading", { level: 1, name: "Collection not found" }),
  ).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "All collections" })).toHaveAttribute(
    "href",
    "/admin/collections",
  );
});

it("offers a retry when an admin collection fails to load", () => {
  mocks.adminSnapshotError = collectionProblem(500);
  show(true, true);
  expect(
    screen.getByRole("heading", { level: 1, name: "Couldn't load this collection" }),
  ).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
});

it("lets a 404 replace an admin collection that was already loaded", () => {
  mocks.collection = {
    id: "collection-1",
    title: "Staff picks",
    collection_type: "manual",
    library_ids: [1],
  } as LibraryCollection;
  mocks.adminSnapshotError = collectionProblem(404);
  show(true, true);
  expect(
    screen.getByRole("heading", { level: 1, name: "Collection not found" }),
  ).toBeInTheDocument();
  expect(screen.queryByTestId("manual-items")).not.toBeInTheDocument();
});
