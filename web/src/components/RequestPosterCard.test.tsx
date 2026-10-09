import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import RequestPosterCard from "./RequestPosterCard";
import type { MediaRequest, RequestMediaResult } from "@/api/types";
import { UICustomizationContext } from "@/contexts/uiCustomizationContext";
import type { CardPresentation } from "@/lib/uiCustomization";

const requestable: RequestMediaResult = {
  media_type: "movie",
  tmdb_id: 42,
  title: "Test Movie",
  availability: "missing",
  request: { requestable: true },
};

function withCardPresentation(cardPresentation: CardPresentation, children: ReactNode) {
  return (
    <UICustomizationContext.Provider
      value={{
        cardPresentation,
        cardPresentationSource: "profile_client",
        primaryMenu: null,
        primaryMenuSource: "default",
        shortcuts: { items: [] },
        isSupported: true,
        supportsAtomicShortcuts: true,
        isLoading: false,
        isUnavailable: false,
      }}
    >
      {children}
    </UICustomizationContext.Provider>
  );
}

describe("RequestPosterCard (discover variant)", () => {
  it("draws the title on the library card frame", () => {
    const { container } = render(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={{ ...requestable, year: 2024 }} />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("button", { name: /your watchlist/ })).toBeNull();
    const card = container.firstElementChild;
    expect(card).toHaveClass("media-card", "group/card");
    // A missing poster shows the default artwork, as on library cards.
    const artwork = card?.querySelector(".media-card-image");
    expect(artwork?.querySelector(".default-artwork .lucide-film")).not.toBeNull();
    expect(artwork?.querySelector("[style]")).toBeNull();

    for (const link of screen.getAllByRole("link", { name: /Test Movie/ })) {
      expect(link).toHaveAttribute("href", "/title/movie/42");
    }
    // Caption: the title, then media type and year.
    expect(screen.getByText("Movie · 2024")).toBeInTheDocument();
  });

  it("puts the Request action in the card's Play slot and sends the request", () => {
    const onRequest = vi.fn();
    render(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} onRequest={onRequest} />
      </MemoryRouter>,
    );

    const button = screen.getByRole("button", { name: /^Request Test Movie \(Movie/ });
    expect(button).toHaveTextContent("Request");
    expect(button).toHaveClass("media-card-play-trigger");
    expect(button.closest(".group\\/media")).not.toBeNull();
    // Beside the artwork link, not inside it.
    expect(button.closest("a")).toBeNull();

    // The action sits over the artwork link; clicking it must not navigate.
    const click = new MouseEvent("click", { bubbles: true, cancelable: true });
    fireEvent(button, click);
    expect(onRequest).toHaveBeenCalledOnce();
    expect(click.defaultPrevented).toBe(true);
  });

  it("shows the pending state while the request is sending", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={requestable}
          onRequest={() => {}}
          isSubmitting
        />
      </MemoryRouter>,
    );

    const button = screen.getByRole("button", { name: /^Sending request for Test Movie/ });
    expect(button).toBeDisabled();
    expect(button).toHaveTextContent("Sending");
    // Stays visible after the pointer leaves the card.
    expect(button).toHaveClass("opacity-100");
  });

  it("offers no Request action without a handler or for an unrequestable title", () => {
    const withoutHandler = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} />
      </MemoryRouter>,
    );
    const unrequestable = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={{ ...requestable, request: { requestable: false, reason: "quota_exceeded" } }}
          onRequest={() => {}}
        />
      </MemoryRouter>,
    );

    expect(withoutHandler).not.toContain("<button");
    expect(unrequestable).not.toContain("<button");
  });

  it("shows the media type so same-title movies and series stay distinguishable", () => {
    const movieMarkup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} />
      </MemoryRouter>,
    );
    const seriesMarkup = renderToStaticMarkup(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={{ ...requestable, media_type: "series", tmdb_id: 43 }}
        />
      </MemoryRouter>,
    );

    expect(movieMarkup).toContain(">Movie<");
    expect(seriesMarkup).toContain(">Series<");
    // The default artwork's mark follows the type too.
    expect(movieMarkup).toContain("lucide-film");
    expect(seriesMarkup).toContain("lucide-tv");
  });

  it("marks a title already in the library as Available and links to it", () => {
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

    const badge = screen.getByText("Available").closest("[data-request-state]");
    expect(badge).toHaveAttribute("data-request-state", "available");
    // The badge overlays the artwork; it is not part of a link.
    expect(badge?.closest(".group\\/media")).not.toBeNull();
    expect(badge?.closest("a")).toBeNull();
    expect(screen.getByRole("link", { name: "Open Test Movie in library" })).toHaveAttribute(
      "href",
      "/item/movie%20tmdb%2042",
    );
  });

  it("dims the artwork of a title that can't be requested", () => {
    const withPoster = { ...requestable, poster_path: "/poster.jpg" };
    const { rerender } = render(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={withPoster} />
      </MemoryRouter>,
    );
    expect(screen.getByRole("img", { name: "Test Movie" })).not.toHaveClass("saturate-[0.8]");

    rerender(
      <MemoryRouter>
        <RequestPosterCard
          variant="discover"
          item={{ ...withPoster, request: { requestable: false, reason: "quota_exceeded" } }}
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole("img", { name: "Test Movie" })).toHaveClass("saturate-[0.8]");
    expect(screen.getByText("Request limit reached")).toBeInTheDocument();
  });

  it("follows the viewer's poster size and always names the type under a caption", () => {
    const { container } = render(
      <MemoryRouter>
        {withCardPresentation(
          { poster_size: "large", caption: "title" },
          <RequestPosterCard variant="discover" item={{ ...requestable, year: 2024 }} />,
        )}
      </MemoryRouter>,
    );

    expect(container.firstElementChild).toHaveClass("w-[170px]");
    expect(screen.getAllByText("Test Movie").length).toBeGreaterThan(0);
    // A movie and a series can share a title, so the type stays even when
    // library cards would show the title alone.
    expect(screen.getByText("Movie · 2024")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Test Movie (Movie · 2024)" })).toBeInTheDocument();
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

  it("fills a grid cell when fluid", () => {
    const { container } = render(
      <MemoryRouter>
        <RequestPosterCard variant="discover" item={requestable} fluid />
      </MemoryRouter>,
    );

    expect(container.firstElementChild).toHaveClass("w-full");
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

  it.each<[Partial<MediaRequest>, string]>([
    [{ status: "pending" }, "Pending"],
    [{ status: "pending", outcome: "cancelled" }, "Cancelled"],
    // The server's derived state wins: a downloaded title not yet scanned in
    // is still processing.
    [{ status: "completed", state: "processing" }, "Processing"],
  ])("labels %o as %s", (overrides, label) => {
    render(
      <MemoryRouter>
        <RequestPosterCard variant="mine" request={{ ...request, ...overrides }} />
      </MemoryRouter>,
    );

    expect(screen.getByText(label)).toBeInTheDocument();
  });

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

  it("shows why a request was declined and dims it", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{
            ...request,
            poster_path: "/matrix.jpg",
            status: "pending",
            outcome: "declined",
            outcome_reason: "Not this month",
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("Declined")).toBeInTheDocument();
    expect(screen.getByText("Not this month")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "The Matrix" })).toHaveClass("saturate-[0.8]");
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

  it("disables Cancel request while the cancellation is in flight", () => {
    render(
      <MemoryRouter>
        <RequestPosterCard
          variant="mine"
          request={{ ...request, status: "pending" }}
          onCancel={() => {}}
          isCancelling
        />
      </MemoryRouter>,
    );

    expect(screen.getByRole("button", { name: "Cancel request for The Matrix" })).toBeDisabled();
  });

  it("keeps request details and Cancel when captions are off", () => {
    render(
      <MemoryRouter>
        {withCardPresentation(
          { poster_size: "standard", caption: "artwork" },
          <RequestPosterCard
            variant="mine"
            request={{ ...request, media_type: "series", status: "pending", seasons: [2] }}
            onCancel={() => {}}
          />,
        )}
      </MemoryRouter>,
    );

    // Only the artwork link carries the title; the caption title is gone.
    expect(screen.getAllByRole("link", { name: /^The Matrix/ })).toHaveLength(1);
    expect(screen.getByText("Season 2")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel request for The Matrix" })).toBeEnabled();
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
