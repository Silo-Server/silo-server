import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LibraryCollection } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import type { EditorSnapshot } from "@/lib/collections/scope";
import { adminCollectionList, adminSmartCollection } from "@/test/fixtures/collectionAnswers";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import CollectionEditorPage from "./CollectionEditorPage";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
const state = vi.hoisted(() => ({ props: undefined as unknown }));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
vi.mock("@/components/collections/editor/CollectionEditor", () => ({
  CollectionEditor: ({ snapshot }: { snapshot: EditorSnapshot<LibraryCollection> }) => {
    const props = { etag: snapshot.etag, collection: snapshot.view.raw };
    state.props = props;
    return (
      <div>
        {props.collection.title} {props.collection.poster_url} {props.etag}
      </div>
    );
  },
}));

installV2Recorder();

const ORIGINAL_ETAG = '"/api/v2/admin/collections/c1#1"';
const smart = adminSmartCollection({});

let client: QueryClient;
function show() {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/edit/c1"]}>
        <Routes>
          <Route element={<CollectionEditorPage scope="server" />}>
            <Route path="/edit/:id" />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/** Refetch everything in the background, as a window refocus or another tab's save would. */
async function refetchAll() {
  const reads = v2Recorder.callsOf("GET /api/v2/admin/collections/{id}").length;
  await act(() => client.invalidateQueries());
  await waitFor(() =>
    expect(v2Recorder.callsOf("GET /api/v2/admin/collections/{id}").length).toBeGreaterThan(reads),
  );
}

beforeEach(() => {
  state.props = undefined;
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", smart);
  v2Recorder.answer(
    "GET /api/v2/admin/collections",
    adminCollectionList({ ...smart, poster_url: "poster.png" }),
  );
});

describe("the editor page keeps the collection it opened", () => {
  it("keeps the definition and ETag together across background refetches", async () => {
    show();
    expect(await screen.findByText(`Original poster.png ${ORIGINAL_ETAG}`)).toBeInTheDocument();

    v2Recorder.answer("GET /api/v2/admin/collections/{id}", {
      ...smart,
      title: "Someone else's edit",
    });
    v2Recorder.answer(
      "GET /api/v2/admin/collections",
      adminCollectionList({ ...smart, poster_url: "new.png" }),
    );
    v2Recorder.bump("/api/v2/admin/collections/c1");
    await refetchAll();

    expect(state.props).toMatchObject({
      etag: ORIGINAL_ETAG,
      collection: { title: "Original", poster_url: "poster.png" },
    });
  });

  it("waits for list artwork before freezing the canonical definition", async () => {
    let listed!: (value: unknown) => void;
    v2Recorder.answer(
      "GET /api/v2/admin/collections",
      () => new Promise((resolve) => (listed = resolve)),
    );
    show();
    await waitFor(() =>
      expect(v2Recorder.callsOf("GET /api/v2/admin/collections/{id}")).toHaveLength(1),
    );
    expect(screen.getByText("Loading collection editor…")).toBeInTheDocument();

    await act(async () => listed(adminCollectionList({ ...smart, poster_url: "hydrated.png" })));

    expect(await screen.findByText(`Original hydrated.png ${ORIGINAL_ETAG}`)).toBeInTheDocument();
  });

  it("lets a 404 replace a collection that was already loaded", async () => {
    show();
    await screen.findByText(`Original poster.png ${ORIGINAL_ETAG}`);

    v2Recorder.answer("GET /api/v2/admin/collections/{id}", () => {
      throw new V2ProblemError("getAdminCollection", {
        type: "https://silo.example/problems/not_found",
        title: "Not Found",
        status: 404,
        detail: "Collection not found.",
        instance: "/api/v2/admin/collections/c1",
      });
    });
    await refetchAll();

    expect(
      await screen.findByRole("heading", { level: 1, name: "Collection not found" }),
    ).toBeInTheDocument();
  });
});
