import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MediaRequest, RequestMediaDetail } from "@/api/types";
import type { WatchlistTitleEntry } from "@/api/v2/watchlistTitles";
import { requestKeys } from "@/hooks/queries/keys";
import RequestActionBar from "./RequestActionBar";

const mocks = vi.hoisted(() => ({
  featureStatus: vi.fn(),
  myRequests: vi.fn(),
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
    useMyMediaRequests: () => ({ data: mocks.myRequests() }),
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

    const button = screen.getByRole("button", { name: "Add to Watchlist" });
    expect(button).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByText(NOTE)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings › Requests" })).toHaveAttribute(
      "href",
      "/settings/requests",
    );

    fireEvent.click(button);

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

  it("removes the title and reverts the page when the call fails", async () => {
    let rejectDelete!: (error: Error) => void;
    mocks.deleteWatchlistTitle.mockReturnValue(
      new Promise<void>((_resolve, reject) => {
        rejectDelete = reject;
      }),
    );
    const queryClient = new QueryClient();
    const onList = { ...heat, in_watchlist: true };
    queryClient.setQueryData(requestKeys.detail("movie", 949), onList);
    renderBar(onList, queryClient);

    expect(screen.getByRole("button", { name: "On Watchlist" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.queryByText(NOTE)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "On Watchlist" }));
    await waitFor(() =>
      expect(
        queryClient.getQueryData<RequestMediaDetail>(requestKeys.detail("movie", 949))
          ?.in_watchlist,
      ).toBe(false),
    );
    await waitFor(() => expect(mocks.deleteWatchlistTitle).toHaveBeenCalledWith("movie", 949));
    rejectDelete(new Error("Network down"));

    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("Network down"));
    expect(mocks.deleteWatchlistTitle).toHaveBeenCalledWith("movie", 949);
    expect(
      queryClient.getQueryData<RequestMediaDetail>(requestKeys.detail("movie", 949))?.in_watchlist,
    ).toBe(true);
  });
});

describe("RequestActionBar cancel", () => {
  beforeEach(() => {
    mocks.featureStatus.mockReturnValue({ data: { requests_enabled: true, allowed: true } });
  });
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  // Someone else's pending request: the detail names no request, so the
  // viewer's own requests for the title are looked up instead.
  const othersPending: RequestMediaDetail = {
    ...heat,
    request: { requestable: false, reason: "already_requested", status: "pending" },
  };
  const ownPending = (is4K: boolean): MediaRequest => ({
    id: is4K ? "r-4k" : "r-hd",
    provider: "tmdb",
    media_type: "movie",
    tmdb_id: 949,
    title: "Heat",
    status: "pending",
    outcome: "active",
    requested_by_user_id: 1,
    is_4k: is4K,
    created_at: "2026-01-02T03:04:05.000Z",
    updated_at: "2026-01-02T03:04:05.000Z",
  });

  it("does not offer the viewer's 4K request as the title's request to cancel", () => {
    mocks.myRequests.mockReturnValue([ownPending(true)]);
    renderBar(othersPending);
    expect(screen.queryByRole("button", { name: "Cancel request" })).toBeNull();
  });

  it("offers the viewer's own 4K request to cancel when there is no regular one", () => {
    mocks.myRequests.mockReturnValue([ownPending(true)]);
    renderBar({
      ...heat,
      request_4k: { requestable: false, reason: "already_requested", status: "pending" },
    });
    expect(screen.getByRole("button", { name: "Cancel 4K request" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Cancel request" })).toBeNull();
  });

  it("offers the viewer's own regular request to cancel", () => {
    mocks.myRequests.mockReturnValue([ownPending(true), ownPending(false)]);
    renderBar(othersPending);
    expect(screen.getByRole("button", { name: "Cancel request" })).toBeTruthy();
  });

  it("asks which seasons of a series to request in 4K", () => {
    mocks.myRequests.mockReturnValue([]);
    renderBar({
      ...heat,
      media_type: "series",
      title: "Example",
      request_4k: { requestable: true },
      seasons: [
        {
          season_number: 1,
          episode_count: 8,
          air_date: "2020-01-01",
          availability: "available",
          requested: false,
        },
      ],
    });
    fireEvent.click(screen.getByRole("button", { name: "Request in 4K" }));
    expect(screen.getByRole("heading", { name: "Request seasons in 4K" })).toBeTruthy();
  });
});
