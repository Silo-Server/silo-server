import type { LibraryCollection } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import type { CollectionScope, EditorSnapshot } from "@/lib/collections/scope";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import CollectionEditorPage from "./CollectionEditorPage";

const mocks = vi.hoisted(() => ({
  collection: null as LibraryCollection | null,
  editSnapshotError: null as Error | null,
  adminSnapshotError: null as Error | null,
}));
vi.mock("@/hooks/queries/collectionScope", () => ({
  // The admin page opens mocks.collection; the personal page opens nothing.
  useScopeEditor: (scope: CollectionScope<LibraryCollection>) => {
    const admin = scope.kind === "server";
    const collection = admin ? mocks.collection : null;
    return {
      snapshot: collection ? { view: scope.toView(collection), etag: '"revision"' } : undefined,
      isLoading: false,
      isFetching: false,
      error: admin ? mocks.adminSnapshotError : mocks.editSnapshotError,
      refetch: vi.fn(),
    };
  },
}));
vi.mock("@/hooks/queries/collections", () => ({
  useCollectionCapabilities: () => ({ data: { import_sources: ["mdblist", "tmdb", "tmdb_list"] } }),
}));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollectionCapabilities: () => ({ data: { imports: true } }),
}));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/libraries", () => ({ useUserLibraries: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p" }, isLoading: false }),
}));
vi.mock("@/components/ImageUploadField", () => ({ ImageUploadField: () => null }));
vi.mock("@/components/collections/editor/CollectionEditor", () => ({
  CollectionEditor: ({
    scope,
    kind,
    snapshot,
    libraryId,
    syncedTab,
  }: {
    scope: CollectionScope;
    kind?: string;
    snapshot?: EditorSnapshot;
    libraryId?: number | null;
    syncedTab?: string;
  }) => (
    <div
      data-testid="editor"
      data-tab={syncedTab ?? ""}
      data-kind={snapshot?.view.kind ?? kind}
      data-source={scope.itemSource}
      data-collection={snapshot?.view.id ?? ""}
      data-library={libraryId ?? ""}
    />
  ),
}));
function Location() {
  const location = useLocation();
  return <p data-testid="location">{location.pathname + location.search}</p>;
}
beforeEach(() => {
  vi.clearAllMocks();
  mocks.collection = null;
  mocks.editSnapshotError = null;
  mocks.adminSnapshotError = null;
});
function show(admin = false) {
  const base = admin ? "/admin/collections" : "/collections";
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={[`${base}/collection-1/edit`]}>
        <Routes>
          <Route element={<CollectionEditorPage scope={admin ? "server" : "personal"} />}>
            <Route path={`${base}/new`} />
            <Route path={`${base}/:id/edit`} />
          </Route>
        </Routes>
        <Location />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
it.each([
  [
    "server",
    "/admin/collections/new?libraryId=7",
    "/admin/collections?libraryId=7&view=list&dialog=new",
  ],
  ["personal", "/collections/new", "/collections?dialog=new"],
] as const)(
  "opens the %s collection list with the type picker for a create link with no type",
  (scope, from, to) => {
    const base = scope === "server" ? "/admin/collections" : "/collections";
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={[from]}>
          <Routes>
            <Route path={base} element={<p>Collection list</p>} />
            <Route element={<CollectionEditorPage scope={scope} />}>
              <Route path={`${base}/new`} />
            </Route>
          </Routes>
          <Location />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(screen.getByText("Collection list")).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(to);
    expect(screen.queryByTestId("editor")).toBeNull();
  },
);

it.each([
  ["manual", "", ""],
  ["smart", "", ""],
  ["synced", "&source=tmdb_chart", "tmdb_chart"],
] as const)("skips the type picker for a %s create link", (kind, source, tab) => {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={[`/admin/collections/new?type=${kind}${source}&libraryId=7`]}>
        <Routes>
          <Route element={<CollectionEditorPage scope="server" />}>
            <Route path="/admin/collections/new" />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  const editor = screen.getByTestId("editor");
  expect(editor).toHaveAttribute("data-source", "library");
  expect(editor).toHaveAttribute("data-kind", kind);
  expect(editor).toHaveAttribute("data-tab", tab);
  expect(editor).toHaveAttribute("data-library", "7");
});

it.each(["manual", "smart"] as const)("opens a saved %s collection in the editor page", (type) => {
  mocks.collection = {
    id: "collection-1",
    title: "Staff picks",
    collection_type: type,
    library_ids: [1],
  } as LibraryCollection;
  show(true);
  const editor = screen.getByTestId("editor");
  expect(editor).toHaveAttribute("data-source", "library");
  expect(editor).toHaveAttribute("data-kind", type);
  expect(editor).toHaveAttribute("data-collection", "collection-1");
});

function showPersonalEdit() {
  show(false);
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
  show(true);
  expect(
    screen.getByRole("heading", { level: 1, name: "Collection not found" }),
  ).toBeInTheDocument();
  expect(document.title).toMatch(/^Not found · /);
  expect(screen.getByRole("link", { name: "All collections" })).toHaveAttribute(
    "href",
    "/admin/collections",
  );
});

it("offers a retry when an admin collection fails to load", () => {
  mocks.adminSnapshotError = collectionProblem(500);
  show(true);
  expect(
    screen.getByRole("heading", { level: 1, name: "Couldn't load this collection" }),
  ).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
});
