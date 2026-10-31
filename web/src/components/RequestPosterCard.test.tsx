import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import type { MediaRequest, RequestMediaDetail, RequestMediaResult } from "@/api/types";

const mocks = vi.hoisted(() => ({
  submit: vi.fn(),
  isSubmitting: vi.fn(() => false),
  seriesDetail: undefined as RequestMediaDetail | undefined,
}));

vi.mock("@/hooks/useSubmitMediaRequest", () => ({
  useSubmitMediaRequest: () => ({ submit: mocks.submit, isSubmitting: () => mocks.isSubmitting() }),
}));

// A series' Request action opens the seasons picker, which reads the series
// detail on its own; that read is not this file's business.
vi.mock("@/hooks/queries/useRequests", () => ({
  useCreateMediaRequest: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useRequestMediaDetail: () => ({ data: mocks.seriesDetail, isFetching: false, isError: false }),
}));

import RequestPosterCard from "./RequestPosterCard";

const requestable: RequestMediaResult = {
  media_type: "movie",
  tmdb_id: 42,
  title: "Test Movie",
  availability: "missing",
  request: { requestable: true },
};

beforeEach(() => {
  mocks.submit.mockReset();
  mocks.isSubmitting.mockReset().mockReturnValue(false);
  mocks.seriesDetail = undefined;
});

describe("RequestPosterCard (discover variant)", () => {
  it("requests a movie from the corner action", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} />
      </MemoryRouter>,
    );

    const request = screen.getByRole("button", { name: "Request Test Movie" });
    expect(request).toHaveAttribute("title", "Request");
    fireEvent.click(request);

    expect(mocks.submit).toHaveBeenCalledWith(requestable);
  });

  it("opens the seasons picker for a series instead of requesting it", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={{ ...requestable, media_type: "series", tmdb_id: 7, title: "Test Series" }}
        />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Request Test Series" }));

    // Only the series detail lists seasons, so the picker chooses them.
    expect(mocks.submit).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Request seasons" })).toBeInTheDocument();
  });

  it("offers no Request action for a title it cannot request", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={{ ...requestable, request: { requestable: false, reason: "quota_exceeded" } }}
        />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("button", { name: "Request Test Movie" })).toBeNull();
    expect(screen.getByText("Request limit reached")).toBeInTheDocument();
  });

  it("says the library has it instead of badging it Available", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={{
            ...requestable,
            availability: "available",
            library_content_id: "movie tmdb 42",
            request: { requestable: false, reason: "already_available" },
          }}
        />
      </MemoryRouter>,
    );

    // The chip carries the news; an Available badge or reason would repeat it.
    expect(screen.queryByText("Available")).toBeNull();
    const chip = screen.getByRole("link", { name: "Open Test Movie in library" });
    expect(chip).toHaveAttribute("href", "/item/movie%20tmdb%2042");
    expect(chip).toHaveTextContent("In library");
    // The chip overlays the artwork; it is not part of the poster link.
    expect(chip.closest(".group\\/media")).not.toBeNull();
  });

  it("shows the default artwork in place of a poster that fails to load", () => {
    const { container } = render(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={{ ...requestable, poster_path: "/p.jpg" }} />
      </MemoryRouter>,
    );
    fireEvent.error(screen.getByRole("img", { name: "Test Movie" }));
    expect(screen.queryByRole("img", { name: "Test Movie" })).not.toBeInTheDocument();
    expect(container.querySelector(".media-card-image .default-artwork")).not.toBeNull();
  });
});

describe("RequestPosterCard watchlist action", () => {
  it("adds or removes the title from the hover corner action", () => {
    const onToggleWatchlist = vi.fn();
    const { unmount } = render(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={requestable}
          onToggleWatchlist={onToggleWatchlist}
        />
      </MemoryRouter>,
    );
    const add = screen.getByRole("button", { name: "Add Test Movie to your watchlist" });
    expect(add).toHaveAttribute("aria-pressed", "false");
    expect(add).toHaveAttribute("title", "Add to Watchlist");
    fireEvent.click(add);
    expect(onToggleWatchlist).toHaveBeenCalledTimes(1);
    unmount();

    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={{ ...requestable, in_watchlist: true }}
          onToggleWatchlist={onToggleWatchlist}
        />
      </MemoryRouter>,
    );
    const remove = screen.getByRole("button", { name: "Remove Test Movie from your watchlist" });
    expect(remove).toHaveAttribute("aria-pressed", "true");
    expect(remove).toHaveAttribute("title", "On Watchlist");
  });
});

describe("RequestPosterCard (mine variant)", () => {
  const request: MediaRequest = {
    id: "req-1",
    provider: "silo",
    media_type: "movie",
    tmdb_id: 603,
    title: "The Matrix",
    status: "queued",
    outcome: "active",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  it("names the requested seasons and how many are in the library", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{
            ...request,
            media_type: "series",
            status: "completed",
            state: "partially_available",
            seasons: [1, 2, 3],
            season_progress: [
              { season_number: 1, episodes_aired: 8, episodes_available: 8 },
              { season_number: 2, episodes_aired: 10, episodes_available: 4 },
              { season_number: 3, episodes_aired: 10, episodes_available: 0 },
            ],
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("Seasons 1–3")).toBeInTheDocument();
    expect(screen.getByText("12 of 28 episodes in the library")).toBeInTheDocument();
  });

  it("prefers the failure detail over the outcome reason", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{
            ...request,
            status: "approved",
            outcome: "failed",
            last_error: "no fulfillment backend configured",
            outcome_reason: "Automation failed",
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("no fulfillment backend configured")).toHaveClass("text-destructive");
    expect(screen.queryByText("Automation failed")).not.toBeInTheDocument();
  });

  it("shows Cancel request only when the page passes onCancel", () => {
    const onCancel = vi.fn();
    const { rerender } = render(
      <MemoryRouter>
        <RequestPosterCard variant="mine" request={{ ...request, status: "pending" }} />
      </MemoryRouter>,
    );
    expect(screen.queryByRole("button", { name: /Cancel request/ })).not.toBeInTheDocument();

    rerender(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{ ...request, status: "pending" }}
          onCancel={onCancel}
        />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Cancel request for The Matrix" }));

    expect(onCancel).toHaveBeenCalledOnce();
  });

  it("links an available request to its library item", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{ ...request, status: "completed", library_content_id: "movie-603" }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByRole("link", { name: "Open The Matrix in library" })).toHaveAttribute(
      "href",
      "/item/movie-603",
    );
  });
});
