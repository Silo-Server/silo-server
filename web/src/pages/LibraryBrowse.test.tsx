import { onlineManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type { ComponentProps } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setProfileId } from "@/api/client";
import { createEmptyQueryDefinition } from "@/api/types";
import { catalogKeys } from "@/hooks/queries/keys";
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

describe("audiobook group tabs after a failed refresh", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  // Loaded groups stay on screen when a later read fails, but the tab must
  // say they may be out of date and offer Try again, as the Library tab does.
  it("keeps the loaded authors and shows the refresh notice", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockRejectedValue(new TypeError("offline")));
    const client = showLibrary({ libraryType: "audiobook", browseType: "authors" });
    await screen.findByText("Couldn't load these authors");

    const key = catalogKeys.audiobookGroups(7, "author", "count", "");
    client.setQueryData(key, {
      pages: [
        {
          groups: [
            {
              name: "Ursula K. Le Guin",
              item_count: 3,
              total_duration_seconds: 36000,
              in_progress_count: 0,
              finished_count: 0,
              poster_urls: [],
            },
          ],
          total: 1,
          total_exact: true,
          has_more: false,
        },
      ],
      pageParams: [""],
    });
    await client.invalidateQueries({ queryKey: key });

    expect(await screen.findByText("Couldn't refresh this library.")).toBeTruthy();
    expect(screen.getByText("Ursula K. Le Guin")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
    client.clear();
  });
});
