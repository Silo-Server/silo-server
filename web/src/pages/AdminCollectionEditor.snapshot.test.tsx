import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AdminCollectionEditor from "./AdminCollectionEditor";
const state = vi.hoisted(() => ({
  snapshot: undefined as unknown,
  loading: false,
  collections: [] as unknown[],
  props: undefined as unknown,
}));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollections: () => ({ data: state.collections, isLoading: state.loading }),
  useAdminCollectionSnapshot: () => ({ data: state.snapshot, isLoading: false }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
vi.mock("./SmartCollectionWizard", () => ({
  default: (props: { etag: string; collection: { title: string; poster_url: string } }) => {
    state.props = props;
    return (
      <div>
        {props.collection.title} {props.collection.poster_url} {props.etag}
      </div>
    );
  },
}));
vi.mock("@/components/CollectionTemplateGallery", () => ({
  CollectionTemplateGallery: () => null,
}));
function tree() {
  return (
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={["/edit/c"]}>
        <Routes>
          <Route path="/edit/:id" element={<AdminCollectionEditor />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}
beforeEach(() => {
  state.loading = false;
  state.collections = [{ id: "c", poster_url: "poster.png" }];
  state.snapshot = {
    collection: { id: "c", title: "Original", collection_type: "smart" },
    etag: '"original"',
  };
});
describe("admin canonical editor snapshot", () => {
  it("keeps the definition and ETag together across background refetches", () => {
    const view = render(tree());
    expect(screen.getByText(/Original poster.png/)).toBeInTheDocument();
    state.snapshot = {
      collection: { id: "c", title: "Someone else's edit", collection_type: "smart" },
      etag: '"new"',
    };
    state.collections = [{ id: "c", poster_url: "new.png" }];
    view.rerender(tree());
    expect(state.props).toMatchObject({
      etag: '"original"',
      collection: { title: "Original", poster_url: "poster.png" },
    });
  });
  it("waits for list artwork before freezing the canonical definition", () => {
    state.loading = true;
    state.collections = [];
    const view = render(tree());
    expect(screen.getByText("Loading collection editor...")).toBeInTheDocument();
    state.loading = false;
    state.collections = [{ id: "c", poster_url: "hydrated.png" }];
    view.rerender(tree());
    expect(state.props).toMatchObject({
      etag: '"original"',
      collection: { poster_url: "hydrated.png" },
    });
  });
});
