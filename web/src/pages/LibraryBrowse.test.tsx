import { onlineManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ComponentProps } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setProfileId } from "@/api/client";
import { createEmptyQueryDefinition } from "@/api/types";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import LibraryBrowse from "./LibraryBrowse";

// The filters panel reads its own options; this page test is about the grid.
vi.mock("@/components/catalog/CatalogFiltersPanel", () => ({ default: () => null }));

function showLibrary(
  props: Pick<ComponentProps<typeof LibraryBrowse>, "libraryType" | "browseType"> = {
    libraryType: "series",
    browseType: "series",
  },
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <LibraryBrowse
          libraryId={7}
          libraryType={props.libraryType}
          browseType={props.browseType}
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
    expect(screen.queryByText("No items found.")).toBeNull();
    // The retry pauses too and settles only on reconnect; the button must
    // not lock up until then.
    const tryAgain = screen.getByRole("button", { name: "Try again" });
    fireEvent.click(tryAgain);
    expect((tryAgain as HTMLButtonElement).disabled).toBe(false);
    expect(fetchMock).not.toHaveBeenCalled();
    client.clear();
  });

  it("shows the load error on an audiobook library's Authors tab, not an empty one", async () => {
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);
    onlineManager.setOnline(false);

    const client = showLibrary({ libraryType: "audiobook", browseType: "authors" });

    expect(await screen.findByText("Couldn't load these authors")).toBeTruthy();
    expect(screen.queryByText("No authors found in this library.")).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
    client.clear();
  });
});
