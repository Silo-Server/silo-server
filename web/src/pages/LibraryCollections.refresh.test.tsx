import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useDeleteCollection } from "@/hooks/queries/collections";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import getCollectionOk from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import getLibraryCollectionsOk from "../../../contracts/api/v2/fixtures/get_library_collections_ok.json";

import { useCollectionDraft } from "@/hooks/queries/collectionScope";
import { PERSONAL_SCOPE } from "@/lib/collections/scope";

import LibraryCollections from "./LibraryCollections";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p-owner", name: "Owner" }] }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner" }, hasSelectedProfile: true }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({
    cardPresentation: { poster_size: "medium", caption: "title_metadata" },
  }),
}));
vi.mock("@/hooks/queries/sidebarPins", () => ({
  useToggleSidebarPin: () => ({ togglePin: vi.fn(), isPinned: () => false, canToggle: false }),
}));
vi.mock("@/hooks/useViewTransition", () => ({ useViewTransitionNavigate: () => vi.fn() }));

installV2Recorder();

const COLLECTION_PATH = "/api/v2/collections/c1";

// What the server's library tab lists for the owner's opted-in collection,
// or nothing once it is gone.
let tabCollection: { title: string } | null;

function libraryTab() {
  const [group] = getLibraryCollectionsOk.groups;
  return {
    ...getLibraryCollectionsOk,
    groups: tabCollection
      ? [
          {
            ...group,
            collections: [
              {
                id: "c1",
                title: tabCollection.title,
                poster_url: "",
                item_count: 4,
                creator_profile_id: "p-owner",
              },
            ],
          },
        ]
      : [],
  };
}

function renderTabWith<T>(useMutationHook: () => T) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
  render(<LibraryCollections libraryId={1} />, { wrapper });
  return renderHook(useMutationHook, { wrapper }).result;
}

function card(name: string) {
  return screen.queryByRole("link", { name: new RegExp(name) });
}

function findCard(name: string) {
  return screen.findByRole("link", { name: new RegExp(name) });
}

function tabReads() {
  return v2Recorder.callsOf("GET /api/v2/library/{id}/collections").length;
}

beforeEach(() => {
  tabCollection = { title: "Rainy days" };
  v2Recorder.answer("GET /api/v2/library/{id}/collections", () => libraryTab());
});

describe("a library's Collections tab after a personal collection changes", () => {
  it("shows the new name once the owner saves an edit", async () => {
    const snapshot = await PERSONAL_SCOPE.fetchSnapshot("c1");
    const update = renderTabWith(() =>
      useCollectionDraft(PERSONAL_SCOPE, { snapshot, kind: "manual" }),
    );
    expect(await findCard("Rainy days")).toBeTruthy();
    const readsBefore = tabReads();

    v2Recorder.answer("PATCH /api/v2/collections/{id}", () => {
      tabCollection = { title: "Rainy evenings" };
      return { ...getCollectionOk, name: "Rainy evenings", include_in_server_collections: true };
    });
    act(() => update.current.setDraft((draft) => ({ ...draft, name: "Rainy evenings" })));
    await act(async () => {
      expect(await update.current.save()).toBe(true);
    });

    expect(await findCard("Rainy evenings")).toBeTruthy();
    expect(card("Rainy days")).toBeNull();
    expect(tabReads()).toBeGreaterThan(readsBefore);
  });

  it("drops a deleted collection from the tab without a reload", async () => {
    const remove = renderTabWith(useDeleteCollection);
    expect(await findCard("Rainy days")).toBeTruthy();

    v2Recorder.answer("DELETE /api/v2/collections/{id}", () => {
      tabCollection = null;
      return undefined;
    });
    await act(() =>
      remove.current.mutateAsync({ id: "c1", etag: v2Recorder.etag(COLLECTION_PATH) }),
    );

    await waitFor(() => expect(card("Rainy days")).toBeNull());
    expect(card("Oscar Winners")).toBeTruthy();
  });
});
