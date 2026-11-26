import { onlineManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setProfileId } from "@/api/client";
import { createEmptyQueryDefinition } from "@/api/types";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import LibraryBrowse from "./LibraryBrowse";

// The filters panel reads its own options; this page test is about the grid.
vi.mock("@/components/catalog/CatalogFiltersPanel", () => ({ default: () => null }));

function showLibrary() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <LibraryBrowse
          libraryId={7}
          libraryType="series"
          browseType="series"
          queryDefinition={createEmptyQueryDefinition()}
          onBrowseTypeChange={vi.fn()}
          onQueryDefinitionChange={vi.fn()}
        />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return client;
}

describe("library browse while offline", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });
  afterEach(() => {
    cleanup();
    onlineManager.setOnline(true);
    vi.unstubAllGlobals();
  });

  // Offline, the first page is paused rather than failed. It must read as a
  // load that can be retried, never as a library with nothing in it.
  it("shows the load error with Try again, not an empty library", async () => {
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);
    onlineManager.setOnline(false);

    const client = showLibrary();

    expect(await screen.findByText("Couldn't load this library")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
    expect(screen.queryByText("No items found.")).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
    client.clear();
  });
});
