import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import type { WatchlistTitle } from "@/api/v2/watchlistTitles";
import WatchlistTitlesTab from "./WatchlistTitlesTab";

const mocks = vi.hoisted(() => ({ toggle: vi.fn(), submit: vi.fn() }));

vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ prefs: null, isOverlaySupported: true }),
}));
vi.mock("@/hooks/useSubmitMediaRequest", () => ({
  useSubmitMediaRequest: () => ({ submit: mocks.submit, isSubmitting: () => false }),
}));
vi.mock("@/hooks/useWatchlistTitleToggle", () => ({
  useWatchlistTitleToggle: () => ({ enabled: true, toggle: mocks.toggle, isPending: () => false }),
}));

function title(tmdbID: number, overrides: Partial<WatchlistTitle> = {}): WatchlistTitle {
  return {
    media_type: "movie",
    tmdb_id: tmdbID,
    title: `Title ${tmdbID}`,
    added_at: "2026-09-01T00:00:00Z",
    status: "active",
    request: { requestable: true },
    ...overrides,
  };
}

function renderTab(props: Partial<Parameters<typeof WatchlistTitlesTab>[0]> = {}) {
  render(
    <MemoryRouter>
      <WatchlistTitlesTab
        titles={[]}
        isLoading={false}
        isError={false}
        onRetry={vi.fn()}
        watchlistRequests
        {...props}
      />
    </MemoryRouter>,
  );
}

describe("WatchlistTitlesTab", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("points an empty watchlist to Discover", () => {
    renderTab();
    expect(screen.getByText("Nothing waiting for the library")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Browse Discover" })).toHaveAttribute(
      "href",
      "/requests",
    );
  });

  it("explains the tab, links to Discover, and lists titles soonest first", () => {
    renderTab({
      titles: [
        title(1, { status: "removed" }),
        title(2, { request: { requestable: false, status: "pending" } }),
      ],
      watchlistRequests: false,
    });
    expect(screen.getByText(/can't be played\. When one arrives/)).toBeInTheDocument();
    expect(screen.queryByText(/They've been requested for you/)).toBeNull();
    expect(screen.getByRole("link", { name: "Find more in Discover" })).toHaveAttribute(
      "href",
      "/requests",
    );
    expect(
      screen.getAllByTestId("watchlist-title-caption").map((node) => node.textContent),
    ).toEqual(["Awaiting approval", "No longer listed on TMDB · Find it"]);
  });

  it("requests and removes titles from their cards", () => {
    const heat = title(3);
    renderTab({ titles: [heat] });
    fireEvent.click(screen.getByRole("button", { name: /Request Title 3/ }));
    expect(mocks.submit).toHaveBeenCalledWith(
      expect.objectContaining({ media_type: "movie", tmdb_id: 3, availability: "missing" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Remove Title 3 from your watchlist" }));
    expect(mocks.toggle).toHaveBeenCalledWith({ ...heat, in_watchlist: true });
  });

  it("offers a retry when the titles fail to load", () => {
    const onRetry = vi.fn();
    renderTab({ titles: undefined, isError: true, onRetry });
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });
});
