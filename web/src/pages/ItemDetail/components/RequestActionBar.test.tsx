import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RequestMediaDetail } from "@/api/types";
import type { WatchlistTitleEntry } from "@/api/v2/watchlistTitles";
import { requestKeys } from "@/hooks/queries/keys";
import RequestActionBar from "./RequestActionBar";

const mocks = vi.hoisted(() => ({
  featureStatus: vi.fn(),
  addWatchlistTitle: vi.fn(),
  deleteWatchlistTitle: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/useRequests")>(
    "@/hooks/queries/useRequests",
  );
  const idle = { mutate: vi.fn(), isPending: false };
  return {
    ...actual,
    useRequestFeatureStatus: () => mocks.featureStatus(),
    useCreateMediaRequest: () => idle,
    useCancelMediaRequest: () => idle,
    useToggleRequestFollow: () => idle,
    useMediaRequest: () => ({ data: undefined }),
    useMyMediaRequests: () => ({ data: undefined }),
  };
});
vi.mock("@/api/v2/watchlistTitles", () => ({
  addWatchlistTitleV2: (...args: unknown[]) => mocks.addWatchlistTitle(...args),
  deleteWatchlistTitleV2: (...args: unknown[]) => mocks.deleteWatchlistTitle(...args),
  listWatchlistTitlesV2: vi.fn(),
}));
vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));
vi.mock("./SubtitlesPopover", () => ({ default: () => null }));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { id: 1 } }) }));
vi.mock("sonner", () => ({
  toast: { success: mocks.toastSuccess, error: mocks.toastError },
}));

const heat: RequestMediaDetail = {
  media_type: "movie",
  tmdb_id: 949,
  title: "Heat",
  availability: "missing",
  in_watchlist: false,
  request: { requestable: true },
} as RequestMediaDetail;

function renderBar(item: RequestMediaDetail, queryClient = new QueryClient()) {
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <RequestActionBar item={item} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return queryClient;
}

const NOTE = /Adding it to your watchlist also requests it/;

describe("RequestActionBar watchlist toggle", () => {
  beforeEach(() => {
    mocks.featureStatus.mockReturnValue({
      data: {
        requests_enabled: true,
        allowed: true,
        watchlist_titles_supported: true,
        watchlist_requests: true,
      },
    });
  });
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("offers Add to Watchlist and says the add also requests", () => {
    renderBar(heat);
    const button = screen.getByRole("button", { name: "Add to Watchlist" });
    expect(button).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByText(NOTE)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings › Requests" })).toHaveAttribute(
      "href",
      "/settings/requests",
    );
  });

  it("drops the note when watchlist adds don't request or the title can't be requested", () => {
    mocks.featureStatus.mockReturnValue({
      data: {
        requests_enabled: true,
        allowed: true,
        watchlist_titles_supported: true,
        watchlist_requests: false,
      },
    });
    renderBar(heat);
    expect(screen.queryByText(NOTE)).toBeNull();
    cleanup();

    mocks.featureStatus.mockReturnValue({
      data: {
        requests_enabled: true,
        allowed: true,
        watchlist_titles_supported: true,
        watchlist_requests: true,
      },
    });
    renderBar({ ...heat, request: { requestable: false, status: "pending" } });
    expect(screen.queryByText(NOTE)).toBeNull();
  });

  it("shows On Watchlist, pressed, without the note", () => {
    renderBar({ ...heat, in_watchlist: true });
    expect(screen.getByRole("button", { name: "On Watchlist" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.queryByText(NOTE)).toBeNull();
  });

  it("offers no watchlist action on a server without watchlist titles", () => {
    mocks.featureStatus.mockReturnValue({ data: { allowed: true } });
    renderBar(heat);
    expect(screen.queryByRole("button", { name: /Watchlist/ })).toBeNull();
    expect(screen.queryByText(NOTE)).toBeNull();
  });

  it("offers no watchlist action while requests are off", () => {
    mocks.featureStatus.mockReturnValue({
      data: { requests_enabled: false, allowed: true, watchlist_titles_supported: true },
    });
    renderBar(heat);
    expect(screen.queryByRole("button", { name: /Watchlist/ })).toBeNull();
  });

  it("adds the title and says it was also requested", async () => {
    const entry: WatchlistTitleEntry = {
      media_type: "movie",
      tmdb_id: 949,
      added_at: "2026-09-30T00:00:00Z",
      request: { requestable: false, status: "pending", requested_by_viewer: true },
    };
    mocks.addWatchlistTitle.mockResolvedValue(entry);
    const queryClient = new QueryClient();
    queryClient.setQueryData(requestKeys.detail("movie", 949), heat);
    renderBar(heat, queryClient);

    fireEvent.click(screen.getByRole("button", { name: "Add to Watchlist" }));

    await waitFor(() =>
      expect(mocks.toastSuccess).toHaveBeenCalledWith("Added to your watchlist and requested", {
        description:
          "We'll let you know when Heat is available. It moves into your watchlist on its own.",
      }),
    );
    expect(mocks.addWatchlistTitle).toHaveBeenCalledWith("movie", 949);
    // The title page reads as on the watchlist straight away.
    expect(
      queryClient.getQueryData<RequestMediaDetail>(requestKeys.detail("movie", 949))?.in_watchlist,
    ).toBe(true);
  });

  it("says why an add did not request the title", async () => {
    mocks.addWatchlistTitle.mockResolvedValue({
      media_type: "movie",
      tmdb_id: 949,
      added_at: "2026-09-30T00:00:00Z",
      request: { requestable: false, reason: "quota_exceeded" },
    } satisfies WatchlistTitleEntry);
    renderBar(heat);

    fireEvent.click(screen.getByRole("button", { name: "Add to Watchlist" }));

    await waitFor(() =>
      expect(mocks.toastSuccess).toHaveBeenCalledWith("Added to your watchlist", {
        description: "It wasn't requested: request limit reached.",
      }),
    );
  });

  it("removes the title and reverts the page when the call fails", async () => {
    mocks.deleteWatchlistTitle.mockRejectedValue(new Error("Network down"));
    const queryClient = new QueryClient();
    const onList = { ...heat, in_watchlist: true };
    queryClient.setQueryData(requestKeys.detail("movie", 949), onList);
    renderBar(onList, queryClient);

    fireEvent.click(screen.getByRole("button", { name: "On Watchlist" }));

    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("Network down"));
    expect(mocks.deleteWatchlistTitle).toHaveBeenCalledWith("movie", 949);
    expect(
      queryClient.getQueryData<RequestMediaDetail>(requestKeys.detail("movie", 949))?.in_watchlist,
    ).toBe(true);
  });
});
