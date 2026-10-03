import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Collection, LibraryCollection, QueryDefinition } from "@/api/types";

import SmartCollectionWizard from "./SmartCollectionWizard";

const mocks = vi.hoisted(() => ({ adminUpdate: vi.fn(), userUpdate: vi.fn(), totalItems: 3 }));

vi.mock("@/hooks/queries/admin/collections", () => ({
  useCreateAdminCollection: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateAdminCollection: () => ({ mutate: mocks.adminUpdate, isPending: false }),
  useAdminCollectionCapabilities: () => ({ data: { artwork: false } }),
}));
vi.mock("@/hooks/queries/collections", () => ({
  useCreateCollection: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateCollection: () => ({ mutate: mocks.userUpdate, isPending: false }),
  useDeleteUserCollectionImage: () => ({ mutate: vi.fn() }),
  useCollectionCapabilities: () => ({ data: { artwork: false } }),
}));
vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: () => ({
    data: { totalItems: mocks.totalItems, pages: new Map() },
    isLoading: false,
  }),
  useCatalogFilters: () => ({ data: undefined, isLoading: false }),
  useCatalogMetadataFilters: () => ({ data: undefined, isLoading: false }),
}));
vi.mock("@/hooks/queries/libraries", () => ({ useUserLibraries: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "profile-1" } }),
}));
vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));
vi.mock("@/components/ItemGrid", () => ({ default: () => null }));

// Rules the Guided view can't show: a negated genre, two actors, and an OR group.
const ADVANCED_RULES: QueryDefinition = {
  library_ids: [7],
  match: "all",
  groups: [
    {
      match: "all",
      rules: [
        { field: "genre", op: "is_not", value: "Horror" },
        { field: "actor", op: "is", value: "Actor A" },
        { field: "actor", op: "is", value: "Actor B" },
      ],
    },
    {
      match: "any",
      rules: [
        { field: "genre", op: "is", value: "Comedy" },
        { field: "genre", op: "is", value: "Drama" },
      ],
    },
  ],
  sort: { field: "added_at", order: "desc" },
};

type Mode = "admin" | "user";

beforeEach(() => {
  vi.clearAllMocks();
  mocks.totalItems = 3;
  HTMLElement.prototype.scrollIntoView = vi.fn();
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

function showWizard(mode: Mode, queryDefinition: QueryDefinition) {
  const wizard =
    mode === "admin" ? (
      <SmartCollectionWizard
        mode="admin"
        collection={
          {
            id: "col-1",
            title: "Picks",
            collection_type: "smart",
            library_id: 7,
            library_ids: [7],
            query_definition: queryDefinition,
          } as LibraryCollection
        }
        etag={'"v1"'}
        libraries={[]}
        initialLibraryId={7}
        onClose={vi.fn()}
      />
    ) : (
      <SmartCollectionWizard
        mode="user"
        collection={
          {
            id: "col-1",
            name: "Picks",
            collection_type: "smart",
            creator_profile_id: "profile-1",
            query_definition: queryDefinition,
          } as Collection
        }
        etag={'"v1"'}
        onClose={vi.fn()}
      />
    );
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>{wizard}</MemoryRouter>
    </QueryClientProvider>,
  );
}

function save(): QueryDefinition {
  fireEvent.click(screen.getByRole("button", { name: "Next: Details" }));
  fireEvent.click(screen.getByRole("button", { name: "Save Collection" }));
  const update = mocks.adminUpdate.mock.lastCall ?? mocks.userUpdate.mock.lastCall;
  // Compare what goes over the wire, where an undefined limit is omitted.
  return JSON.parse(JSON.stringify(update?.[0].body.query_definition));
}

describe.each<Mode>(["admin", "user"])("SmartCollectionWizard (%s)", (mode) => {
  it.each([
    ["no limit", undefined, null, undefined],
    ["the server's no-limit sentinel", 10_000_000, null, undefined],
    ["an explicit limit", 250, 250, 250],
  ])("saves a collection with %s unchanged", (_label, stored, shown, sent) => {
    showWizard(mode, { ...ADVANCED_RULES, limit: stored });

    expect(screen.getByLabelText("Max items")).toHaveValue(shown);
    expect(save()).toEqual(
      sent === undefined ? ADVANCED_RULES : { ...ADVANCED_RULES, limit: sent },
    );
  });

  it("keeps rules the Guided view can't show when the toolbar sort changes", () => {
    showWizard(mode, ADVANCED_RULES);

    const sortSelect = screen
      .getAllByRole("combobox")
      .find((element) => element.textContent?.trim() === "Date Added");
    fireEvent.click(sortSelect!);
    fireEvent.click(screen.getByRole("option", { name: "Title" }));

    expect(save()).toEqual({ ...ADVANCED_RULES, sort: { field: "title", order: "asc" } });
  });

  it("lets a collection that matches nothing continue to Details", () => {
    mocks.totalItems = 0;
    showWizard(mode, ADVANCED_RULES);

    expect(screen.getByText("No titles match these filters yet.")).toBeVisible();
    expect(screen.getByRole("button", { name: "Next: Details" })).toBeEnabled();
    expect(save()).toEqual(ADVANCED_RULES);
  });
});
