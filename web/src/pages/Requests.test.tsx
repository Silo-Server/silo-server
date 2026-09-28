import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import type { MediaRequest, RequestDiscoverySection, RequestMediaResult } from "@/api/types";

const mocks = vi.hoisted(() => ({
  mine: [] as MediaRequest[],
  sections: [] as RequestDiscoverySection[],
  cancel: vi.fn(),
  create: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", () => {
  const idle = { data: [], isLoading: false, isError: false, refetch: vi.fn() };
  return {
    useRequestDiscovery: () => ({ ...idle, data: mocks.sections }),
    useDiscoverStudios: () => idle,
    useDiscoverNetworks: () => idle,
    useDiscoverGenres: () => idle,
    useMyMediaRequests: () => ({
      data: mocks.mine,
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    }),
    useCreateMediaRequest: () => ({ mutateAsync: mocks.create, isPending: false }),
    useCancelMediaRequest: () => ({ mutate: mocks.cancel, isPending: false, variables: undefined }),
  };
});
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/components/BrandCarousel", () => ({ default: () => null }));
// Embla needs layout APIs jsdom lacks; MediaCarousel.test covers the real row.
vi.mock("@/components/MediaCarousel", () => ({
  default: ({
    title,
    viewAllHref,
    children,
  }: {
    title: string;
    viewAllHref?: string;
    children: ReactNode;
  }) => (
    <section aria-label={title}>
      {viewAllHref ? <a href={viewAllHref}>Explore all {title}</a> : null}
      {children}
    </section>
  ),
}));

import Requests from "./Requests";

function request(id: string, title: string, overrides: Partial<MediaRequest> = {}): MediaRequest {
  return {
    id,
    provider: "silo",
    media_type: "movie",
    tmdb_id: Number(id.replace(/\D/g, "")) || 1,
    title,
    status: "pending",
    outcome: "active",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

function result(tmdbID: number, title: string): RequestMediaResult {
  return {
    media_type: "movie",
    tmdb_id: tmdbID,
    title,
    availability: "missing",
    request: { requestable: true },
  };
}

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>;
}

function renderAt(url: string) {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/requests" element={<Requests />} />
        <Route path="*" element={null} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
  );
}

function currentLocation() {
  return screen.getByTestId("location").textContent;
}

beforeEach(() => {
  mocks.cancel.mockReset();
  mocks.create.mockReset().mockResolvedValue({});
  mocks.mine = [];
  mocks.sections = [];
});

describe("Requests hub", () => {
  it("has the app's page header and hands search to the app's search page", () => {
    renderAt("/requests");

    expect(screen.getByRole("heading", { level: 1, name: "Requests" })).toBeInTheDocument();
    expect(screen.queryByText(/worth waiting for/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Status guide" })).not.toBeInTheDocument();

    const search = screen.getByRole("textbox", { name: "Search movies and series" });
    fireEvent.change(search, { target: { value: "  the bear " } });
    fireEvent.submit(search.closest("form")!);

    expect(currentLocation()).toBe("/catalog?source=query&q=the+bear&type=video");
  });

  it.each([
    ["/requests?q=bear", "/catalog?source=query&q=bear&type=video"],
    ["/requests?q=bear&type=movie", "/catalog?source=query&q=bear&type=movie"],
    ["/requests?q=bear&media_type=series&page=3", "/catalog?source=query&q=bear&type=series"],
    ["/requests?q=bear&type=all", "/catalog?source=query&q=bear&type=video"],
  ])("sends the old hub search URL %s to the app's search page", (from, to) => {
    renderAt(from);

    expect(currentLocation()).toBe(to);
  });

  it("stays on the hub when the old search box was left empty", () => {
    renderAt("/requests?q=%20");

    expect(currentLocation()).toBe("/requests?q=%20");
    expect(screen.getByRole("heading", { level: 1, name: "Requests" })).toBeInTheDocument();
  });

  it("links each Discover row with more pages to its full grid", () => {
    mocks.sections = [
      {
        key: "trending_movies",
        title: "Trending Movies",
        page: 1,
        total_pages: 12,
        total_results: 240,
        results: [result(1, "Heat")],
      },
      {
        key: "upcoming_movies",
        title: "Upcoming Movies",
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [result(2, "Sinners")],
      },
      {
        key: "on_air_series",
        title: "On the Air",
        page: 1,
        total_pages: 0,
        total_results: 0,
        results: [],
      },
    ];
    renderAt("/requests");

    const trending = screen.getByRole("region", { name: "Trending Movies" });
    expect(
      within(trending).getByRole("link", { name: "Explore all Trending Movies" }),
    ).toHaveAttribute("href", "/requests/discover/trending_movies");
    expect(within(trending).getAllByRole("link", { name: "Heat" })[0]).toHaveAttribute(
      "href",
      "/title/movie/1",
    );

    const upcoming = screen.getByRole("region", { name: "Upcoming Movies" });
    expect(within(upcoming).queryByRole("link", { name: /^Explore all/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "On the Air" })).not.toBeInTheDocument();
  });

  it("opens Yours from ?tab=yours and the old ?tab=mine", () => {
    renderAt("/requests?tab=mine");

    expect(screen.getByRole("tab", { name: /Yours/ })).toHaveAttribute("aria-selected", "true");
  });
});

describe("Requests (Yours list)", () => {
  beforeEach(() => {
    mocks.mine = [
      request("r1", "Waiting Movie"),
      request("r2", "Approved Movie", {
        status: "approved",
        targets: [{ quality: "1080p", status: "queued" }] as MediaRequest["targets"],
      }),
      request("r3", "Downloading Movie", { status: "downloading" }),
      request("r4", "Queued Movie", { status: "queued" }),
      request("r5", "Withdrawn Movie", { outcome: "cancelled" }),
    ];
  });

  function renderYours() {
    renderAt("/requests?tab=yours");
  }

  function row(title: string) {
    return screen.getByRole("link", { name: title }).closest("li") as HTMLElement;
  }

  it("lists each request with its title page, type, year, seasons, and progress", () => {
    mocks.mine = [
      request("r10", "The Bear", {
        media_type: "series",
        tmdb_id: 136315,
        year: 2022,
        seasons: [1, 2, 3],
        state: "partially_available",
        season_progress: [
          { season_number: 1, episodes_aired: 8, episodes_available: 8 },
          { season_number: 2, episodes_aired: 10, episodes_available: 3 },
          { season_number: 3, episodes_aired: 10, episodes_available: 0 },
        ],
      }),
    ];
    renderYours();

    const bear = row("The Bear");
    expect(within(bear).getByRole("link", { name: "The Bear" })).toHaveAttribute(
      "href",
      "/title/series/136315",
    );
    expect(bear).toHaveTextContent(
      "Series · 2022 · Seasons 1–3 · 11 of 28 episodes in the library",
    );
    expect(bear.querySelector('[data-request-state="partially_available"]')).toHaveTextContent(
      "Partially available",
    );
  });

  it("shows how far a request's download is, without naming a blocked import", () => {
    mocks.mine = [
      request("r13", "Heat", {
        status: "downloading",
        state: "processing",
        download: {
          phase: "downloading",
          percent: 43,
          downloads: 1,
          updated_at: "2026-01-01T00:00:00Z",
        },
      }),
      request("r14", "Ronin", {
        status: "downloading",
        state: "processing",
        download: {
          phase: "import_blocked",
          percent: 100,
          downloads: 1,
          updated_at: "2026-01-01T00:00:00Z",
        },
      }),
    ];
    renderYours();

    const heat = row("Heat");
    expect(within(heat).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "43");
    expect(within(heat).getByText("Downloading · 43%")).toBeInTheDocument();
    expect(within(row("Ronin")).getByText("Waiting for import")).toBeInTheDocument();
    expect(screen.queryByText("Import blocked")).not.toBeInTheDocument();
  });

  it("shows when a request was made and when it last changed", () => {
    mocks.mine = [
      request("r11", "Old Movie", { updated_at: "2026-01-01T00:00:30Z" }),
      request("r12", "Changed Movie", {
        status: "approved",
        targets: [{ quality: "1080p", status: "queued" }] as MediaRequest["targets"],
        updated_at: "2026-01-03T00:00:00Z",
      }),
    ];
    renderYours();

    const old = row("Old Movie");
    expect(within(old).getByText(/^Requested/)).not.toHaveTextContent("Updated");
    expect(old.querySelector('time[datetime="2026-01-01T00:00:00Z"]')).toBeInTheDocument();

    const changed = row("Changed Movie");
    expect(changed.querySelector('time[datetime="2026-01-03T00:00:00Z"]')).toBeInTheDocument();
    expect(within(changed).getByText(/^Requested/)).toHaveTextContent("Updated");
  });

  it("shows why a request was declined, and an error in place of a reason", () => {
    mocks.mine = [
      request("r13", "Declined Movie", { outcome: "declined", outcome_reason: "Not this month" }),
      request("r14", "Failed Movie", {
        outcome: "failed",
        outcome_reason: "ignored",
        last_error: "no fulfillment backend configured",
      }),
    ];
    renderYours();

    expect(row("Declined Movie")).toHaveTextContent("Reason: Not this month");
    const failed = row("Failed Movie");
    expect(within(failed).getByText("no fulfillment backend configured")).toHaveClass(
      "text-destructive",
    );
    expect(failed).not.toHaveTextContent("ignored");
  });

  it("groups requests by what happens next, in server order within a group", () => {
    mocks.mine = [
      request("r20", "Arrived Movie", { status: "completed", library_content_id: "movie-20" }),
      request("r21", "Declined Movie", { outcome: "declined" }),
      ...mocks.mine,
      request("r22", "Failed Movie", { outcome: "failed" }),
    ];
    renderYours();

    const headings = screen
      .getAllByRole("heading", { level: 2 })
      .map((heading) => heading.textContent);
    expect(headings).toEqual(["Needs attention2", "On the way4", "In your library1", "Cancelled1"]);

    const attention = screen.getByRole("region", { name: /Needs attention/ });
    expect(
      within(attention)
        .getAllByRole("listitem")
        .map((item) => within(item).getAllByRole("link")[0]!.textContent),
    ).toEqual(["Declined Movie", "Failed Movie"]);
    expect(
      within(screen.getByRole("region", { name: /Cancelled/ })).getByText("Withdrawn Movie"),
    ).toBeInTheDocument();
  });

  it("opens a request in the library when it has arrived", () => {
    mocks.mine = [
      request("r30", "Arrived Movie", { status: "completed", library_content_id: "movie 30" }),
      request("r31", "Waiting Movie"),
    ];
    renderYours();

    expect(
      within(row("Arrived Movie")).getByRole("link", { name: "Open Arrived Movie in library" }),
    ).toHaveAttribute("href", "/item/movie%2030");
    expect(within(row("Waiting Movie")).queryByRole("link", { name: /in library/ })).toBeNull();
  });

  it("offers Cancel request until a request is sent to the download automation", () => {
    mocks.mine = [...mocks.mine, request("r6", "Approved Unsent Movie", { status: "approved" })];
    renderYours();

    const buttons = screen.getAllByRole("button", { name: /^Cancel request for / });
    expect(buttons.map((button) => button.getAttribute("aria-label"))).toEqual([
      "Cancel request for Waiting Movie",
      "Cancel request for Approved Unsent Movie",
    ]);
  });

  it("cancels only after the viewer confirms", () => {
    renderYours();

    fireEvent.click(screen.getByRole("button", { name: "Cancel request for Waiting Movie" }));
    const dialog = screen.getByRole("alertdialog");
    expect(dialog).toHaveTextContent(
      'Your request for "Waiting Movie" will be withdrawn before it is sent to the download automation.',
    );
    expect(mocks.cancel).not.toHaveBeenCalled();

    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel request" }));

    expect(mocks.cancel).toHaveBeenCalledExactlyOnceWith("r1");
  });

  it("keeps the request when the viewer backs out", () => {
    renderYours();

    fireEvent.click(screen.getByRole("button", { name: "Cancel request for Waiting Movie" }));
    fireEvent.click(screen.getByRole("button", { name: "Keep request" }));

    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(mocks.cancel).not.toHaveBeenCalled();
  });

  it("explains the shared status vocabulary on request, without a live-update promise", () => {
    renderYours();

    expect(screen.queryByRole("definition")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "What the statuses mean" }));

    const help = screen.getByLabelText("Request statuses");
    for (const label of [
      "Pending",
      "Approved",
      "Processing",
      "Partially available",
      "Available",
      "Declined",
      "Cancelled",
      "Failed",
    ]) {
      expect(within(help).getByText(label)).toBeInTheDocument();
    }
    expect(within(help).queryByText("Queued")).not.toBeInTheDocument();
    expect(within(help).queryByText("Completed")).not.toBeInTheDocument();
    // Cancel lasts until the request is sent, not only while it is pending.
    expect(help).toHaveTextContent(
      "You can cancel it until it is sent to the download automation.",
    );
    expect(help).toHaveTextContent("Withdrawn before it was sent to the download automation.");
    expect(help).toHaveTextContent("Some episodes of the requested seasons have arrived.");
    expect(screen.queryByText(/update automatically/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/live status/i)).not.toBeInTheDocument();
  });

  it("points an empty list at Discover", () => {
    mocks.mine = [];
    renderYours();

    fireEvent.click(screen.getByRole("button", { name: "Browse Discover" }));

    expect(currentLocation()).toBe("/requests");
  });
});
