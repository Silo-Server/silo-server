import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { WatchlistTitle } from "@/api/v2/watchlistTitles";

const mocks = vi.hoisted(() => ({
  featureStatus: vi.fn(),
  watchlistTitles: vi.fn(),
  titlesTab: vi.fn(),
}));

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: () => ({
    data: {
      title: "Watchlist",
      totalItems: 1,
      pages: new Map([[0, [{ content_id: "movie-1", title: "Heat", type: "movie" }]]]),
    },
    isLoading: false,
    isError: false,
    isPlaceholderData: false,
    error: null,
    sourceError: null,
    refetch: vi.fn(),
  }),
  useCatalogFilters: () => ({ data: undefined, isLoading: false }),
  useCatalogMetadataFilters: () => ({ data: undefined, isLoading: false }),
}));
vi.mock("@/hooks/queries/personSearch", () => ({
  usePersonSearch: () => ({ data: undefined, isLoading: false, isError: false }),
}));
vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => ({ discoveryEnabled: false, isResolving: false }),
}));
vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: () => ({ data: undefined, isLoading: false }),
  useRequestFeatureStatus: () => mocks.featureStatus(),
}));
vi.mock("@/hooks/queries/watchlistTitles", () => ({
  useWatchlistTitles: (options: { enabled?: boolean }) => mocks.watchlistTitles(options),
}));
vi.mock("@/components/watchlist/WatchlistTitlesTab", () => ({
  default: (props: { watchlistRequests: boolean; titles?: WatchlistTitle[] }) => {
    mocks.titlesTab(props);
    return <div data-testid="watchlist-titles-tab" />;
  },
}));
vi.mock("@/components/ItemGrid", () => ({ default: () => <div data-testid="item-grid" /> }));
vi.mock("@/components/catalog/CatalogFiltersPanel", () => ({
  default: () => <div data-testid="catalog-filters" />,
}));

import Catalog from "./Catalog";

function title(tmdbID: number, status = "active"): WatchlistTitle {
  return {
    media_type: "movie",
    tmdb_id: tmdbID,
    title: `Title ${tmdbID}`,
    added_at: "2026-09-01T00:00:00Z",
    status,
    request: { requestable: true },
  };
}

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{`${location.pathname}${location.search}`}</div>;
}

function renderCatalog(href: string) {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={[href]}>
        <Catalog />
        <LocationProbe />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function selectTab(name: RegExp) {
  // Radix tabs activate on a primary-button mouse down.
  fireEvent.mouseDown(screen.getByRole("tab", { name }), { button: 0, ctrlKey: false });
}

beforeEach(() => {
  mocks.featureStatus.mockReturnValue({
    data: {
      requests_enabled: true,
      allowed: true,
      watchlist_titles_supported: true,
      watchlist_requests: true,
    },
  });
  mocks.watchlistTitles.mockReturnValue({
    data: [title(1), title(2)],
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  });
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("opens on the library tab and keeps the other tab in the URL", () => {
  renderCatalog("/catalog?source=watchlist");

  expect(screen.getByRole("tab", { name: "In your library" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  expect(screen.getByTestId("item-grid")).toBeInTheDocument();
  expect(screen.getByTestId("catalog-filters")).toBeInTheDocument();
  expect(screen.queryByTestId("watchlist-titles-tab")).toBeNull();
  expect(screen.getByRole("tab", { name: /Not in your library yet/ })).toHaveTextContent("2");
  expect(screen.queryByTestId("watchlist-attention-dot")).toBeNull();

  selectTab(/Not in your library yet/);

  expect(screen.getByTestId("location").textContent).toBe(
    "/catalog?source=watchlist&tab=not-in-library",
  );
  expect(screen.getByTestId("watchlist-titles-tab")).toBeInTheDocument();
  expect(screen.queryByTestId("item-grid")).toBeNull();
  expect(screen.queryByTestId("catalog-filters")).toBeNull();

  selectTab(/In your library/);
  expect(screen.getByTestId("location").textContent).toBe("/catalog?source=watchlist");
});

it("labels each tab's content as its tab panel", () => {
  renderCatalog("/catalog?source=watchlist");
  const checkPanel = (tabName: RegExp, content: string) => {
    const tab = screen.getByRole("tab", { name: tabName });
    const panel = screen.getByRole("tabpanel", { name: tabName });
    expect(tab).toHaveAttribute("aria-controls", panel.id);
    expect(panel).toContainElement(screen.getByTestId(content));
  };
  checkPanel(/In your library/, "item-grid");
  expect(screen.getByRole("tabpanel")).toContainElement(screen.getByTestId("catalog-filters"));

  selectTab(/Not in your library yet/);
  checkPanel(/Not in your library yet/, "watchlist-titles-tab");
});

it("restores the titles tab from the URL and passes whether adds request", () => {
  mocks.watchlistTitles.mockReturnValue({
    data: [title(1), title(2, "needs_review")],
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  });
  mocks.featureStatus.mockReturnValue({
    data: {
      requests_enabled: true,
      allowed: true,
      watchlist_titles_supported: true,
      watchlist_requests: false,
    },
  });
  renderCatalog("/catalog?source=watchlist&tab=not-in-library");

  const tab = screen.getByRole("tab", { name: /Not in your library yet/ });
  expect(tab).toHaveAttribute("aria-selected", "true");
  expect(screen.getByTestId("watchlist-attention-dot")).toBeInTheDocument();
  expect(tab).toHaveAccessibleName(/some need attention/);
  expect(mocks.titlesTab).toHaveBeenLastCalledWith(
    expect.objectContaining({ watchlistRequests: false }),
  );
});

it.each([
  [
    "a server without watchlist titles",
    { requests_enabled: true, watchlist_titles_supported: false },
  ],
  // The titles operations answer 409 capability_disabled while requests are off.
  ["requests turned off", { requests_enabled: false, watchlist_titles_supported: true }],
])("shows no tabs with %s", (_, status) => {
  mocks.featureStatus.mockReturnValue({ data: { allowed: true, ...status } });
  renderCatalog("/catalog?source=watchlist&tab=not-in-library");

  expect(screen.queryByRole("tab")).toBeNull();
  expect(screen.queryByTestId("watchlist-titles-tab")).toBeNull();
  expect(screen.getByTestId("item-grid")).toBeInTheDocument();
  expect(screen.queryByRole("tabpanel")).toBeNull();
  expect(mocks.watchlistTitles).toHaveBeenLastCalledWith({ enabled: false });
});

it("shows no tabs outside the watchlist", () => {
  renderCatalog("/catalog?source=favorites&tab=not-in-library");
  expect(screen.queryByRole("tab")).toBeNull();
  expect(mocks.watchlistTitles).toHaveBeenLastCalledWith({ enabled: false });
});
