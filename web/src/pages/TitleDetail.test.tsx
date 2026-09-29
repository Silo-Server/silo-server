import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import type { MediaRequest, RequestMediaDetail, RequestMediaResult } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";

const mocks = vi.hoisted(() => ({
  detail: {} as Record<string, unknown>,
  library: {} as Record<string, unknown>,
  mine: [] as unknown[],
  named: undefined as unknown,
  useRequestMediaDetail: vi.fn(),
  useMediaRequest: vi.fn(),
  useCatalogItemDetail: vi.fn(),
  useMyMediaRequests: vi.fn(),
  create: vi.fn(),
  cancel: vi.fn(),
  toggleFollow: vi.fn(),
  refetch: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestMediaDetail: (...args: unknown[]) => {
    mocks.useRequestMediaDetail(...args);
    return mocks.detail;
  },
  useCreateMediaRequest: () => ({ mutate: mocks.create, isPending: false, variables: undefined }),
  useMyMediaRequests: (...args: unknown[]) => {
    mocks.useMyMediaRequests(...args);
    return { data: mocks.mine };
  },
  useMediaRequest: (...args: unknown[]) => {
    mocks.useMediaRequest(...args);
    return { data: mocks.named };
  },
  useCancelMediaRequest: () => ({ mutate: mocks.cancel, isPending: false }),
  useToggleRequestFollow: () => ({ mutate: mocks.toggleFollow, isPending: false }),
}));
vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: (id: string | undefined) => {
    mocks.useCatalogItemDetail(id);
    return mocks.library;
  },
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { id: 7 } }) }));
vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));
vi.mock("@/pages/ItemDetail/DetailHero", () => ({
  default: ({
    title,
    metadata,
    scoreRow,
    crewLine,
    actions,
  }: {
    title: string;
    metadata: ReactNode;
    scoreRow: ReactNode;
    crewLine: ReactNode;
    actions: ReactNode;
  }) => (
    <section>
      <h1>{title}</h1>
      {metadata}
      {scoreRow}
      {crewLine}
      {actions}
    </section>
  ),
}));
vi.mock("@/pages/ItemDetail/ItemDetailSkeleton", () => ({
  default: () => <div>Loading title</div>,
}));
vi.mock("@/components/CastCarousel", () => ({
  default: ({ cast }: { cast: Array<{ name: string }> }) => (
    <ul>
      {cast.map((member) => (
        <li key={member.name}>{member.name}</li>
      ))}
    </ul>
  ),
}));
vi.mock("@/components/MediaCarousel", () => ({
  default: ({ title, children }: { title: string; children: ReactNode }) => (
    <section aria-label={title}>{children}</section>
  ),
}));

import TitleDetail from "./TitleDetail";
import LegacyRequestDetailRedirect from "./LegacyRequestDetailRedirect";

const baseDetail: RequestMediaDetail = {
  media_type: "movie",
  tmdb_id: 603,
  title: "The Matrix",
  runtime: 136,
  availability: "missing",
  request: {
    requestable: false,
    status: "pending",
    reason: "already_requested",
    request_id: "req-1",
    following: true,
    requested_by_viewer: true,
  },
};

const ownPending: MediaRequest = {
  id: "req-1",
  provider: "silo",
  media_type: "movie",
  tmdb_id: 603,
  title: "The Matrix",
  status: "pending",
  outcome: "active",
  requested_by_user_id: 7,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function detailQuery(data?: RequestMediaDetail, overrides: Record<string, unknown> = {}) {
  return {
    data,
    isLoading: false,
    isError: false,
    isFetching: false,
    error: null,
    refetch: mocks.refetch,
    ...overrides,
  };
}

function libraryQuery(overrides: Record<string, unknown> = {}) {
  return { data: undefined, isLoading: false, isError: false, error: null, ...overrides };
}

function problem(status: number) {
  return new V2ProblemError("getRequestMediaDetail", {
    type: `https://silo.example/problems/${status === 404 ? "not_found" : "internal_error"}`,
    title: status === 404 ? "Not Found" : "Internal Server Error",
    status,
    detail: status === 404 ? "Title not found." : "TMDB is unavailable.",
    instance: "/api/v2/requests/detail/movie/603",
  });
}

function LocationProbe() {
  const location = useLocation();
  return <p data-testid="location">{location.pathname}</p>;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/title/:mediaType/:tmdbId" element={<TitleDetail />} />
        <Route path="/requests/:mediaType/:tmdbId" element={<LegacyRequestDetailRedirect />} />
        <Route path="/item/:id" element={<LocationProbe />} />
      </Routes>
    </MemoryRouter>,
  );
}

function renderDetail(detail: RequestMediaDetail = baseDetail) {
  mocks.detail = detailQuery(detail);
  renderAt(`/title/${detail.media_type}/${detail.tmdb_id}`);
}

function primaryButton(name: string | RegExp) {
  return screen.getByRole("button", { name });
}

describe("TitleDetail", () => {
  beforeEach(() => {
    // The seasons rail is an Embla carousel, which reads media queries and
    // observes its slides; jsdom has none of these.
    class NoopObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
      takeRecords() {
        return [];
      }
    }
    vi.stubGlobal("IntersectionObserver", NoopObserver);
    vi.stubGlobal("ResizeObserver", NoopObserver);
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }));
    mocks.detail = detailQuery(baseDetail);
    mocks.library = libraryQuery();
    mocks.mine = [ownPending];
    mocks.named = ownPending;
    mocks.useRequestMediaDetail.mockReset();
    mocks.useMediaRequest.mockReset();
    mocks.useCatalogItemDetail.mockReset();
    mocks.useMyMediaRequests.mockReset();
    mocks.create.mockReset();
    mocks.cancel.mockReset();
    mocks.toggleFollow.mockReset();
    mocks.refetch.mockReset();
  });

  describe("primary action", () => {
    it.each([
      ["movie", "Request movie"],
      ["series", "Request series"],
    ] as const)("offers to request a requestable %s", (mediaType, label) => {
      renderDetail({
        ...baseDetail,
        media_type: mediaType,
        request: { requestable: true },
      });

      const request = primaryButton(label);
      expect(request).toBeEnabled();
      fireEvent.click(request);

      expect(mocks.create).toHaveBeenCalledExactlyOnceWith(
        expect.objectContaining({ media_type: mediaType, tmdb_id: 603, title: "The Matrix" }),
      );
    });

    it("requests a series season by season", () => {
      renderDetail({
        ...baseDetail,
        media_type: "series",
        title: "Severance",
        request: { requestable: true },
        seasons: [
          {
            season_number: 1,
            episode_count: 9,
            air_date: "2022-02-18",
            availability: "available",
            requested: false,
          },
          {
            season_number: 2,
            episode_count: 10,
            air_date: "2025-01-17",
            availability: "partial",
            requested: false,
          },
          {
            season_number: 3,
            episode_count: 10,
            air_date: "2999-01-01",
            availability: "missing",
            requested: false,
          },
        ],
      });

      // The page lists the seasons with what the library has.
      const seasons = screen.getByRole("heading", { name: "Seasons" }).closest("section")!;
      expect(within(seasons).getByText("In library")).toBeInTheDocument();
      expect(within(seasons).getByText("Partly in library")).toBeInTheDocument();
      expect(within(seasons).getByText("Not aired yet")).toBeInTheDocument();

      fireEvent.click(primaryButton("Request series"));
      const dialog = screen.getByRole("dialog", { name: "Request seasons" });
      // Season 1 is in the library; the aired, incomplete season 2 starts
      // picked; the unaired season 3 can be picked but is not by default.
      expect(within(dialog).getByRole("switch", { name: "Season 1" })).toBeDisabled();
      expect(within(dialog).getByRole("switch", { name: "Season 2" })).toBeChecked();
      const season3 = within(dialog).getByRole("switch", { name: "Season 3" });
      expect(season3).not.toBeChecked();
      fireEvent.click(season3);
      fireEvent.click(within(dialog).getByRole("button", { name: "Request Seasons 2–3" }));

      expect(mocks.create).toHaveBeenCalledOnce();
      expect(mocks.create.mock.calls[0]![0]).toMatchObject({
        media_type: "series",
        tmdb_id: 603,
        seasons: [2, 3],
      });
    });

    it("shows the viewer's own pending request as Requested, with Cancel request", () => {
      renderDetail();

      expect(primaryButton("Requested")).toBeDisabled();
      fireEvent.click(primaryButton("Cancel request"));
      fireEvent.click(
        within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel request" }),
      );

      expect(mocks.cancel).toHaveBeenCalledExactlyOnceWith("req-1");
    });

    it("offers no follow toggle on the viewer's own request", () => {
      renderDetail();

      expect(screen.queryByRole("button", { name: /Notify me/ })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /Stop notifying/ })).not.toBeInTheDocument();
    });

    it("offers Cancel request for a request older than the first page of the viewer's requests", () => {
      mocks.mine = [];
      renderDetail();

      expect(primaryButton("Cancel request")).toBeEnabled();
      expect(mocks.useMediaRequest).toHaveBeenCalledWith("req-1", { enabled: true });
      expect(mocks.useMyMediaRequests).toHaveBeenCalledWith(
        { outcome: "active" },
        { enabled: false, pollDownloads: false },
      );
    });

    it("hides Cancel request from an admin viewing another account's pending request", () => {
      mocks.named = { ...ownPending, requested_by_user_id: 8 };
      renderDetail();

      expect(primaryButton("Requested")).toBeDisabled();
      expect(screen.queryByRole("button", { name: "Cancel request" })).not.toBeInTheDocument();
    });

    it("hides Cancel request once the request has been sent", () => {
      renderDetail({ ...baseDetail, request: { ...baseDetail.request, status: "downloading" } });

      expect(primaryButton("Processing")).toBeDisabled();
      expect(screen.queryByRole("button", { name: "Cancel request" })).not.toBeInTheDocument();
      // The ownership lookup only runs while the request could still be withdrawn.
      expect(mocks.useMyMediaRequests).toHaveBeenCalledWith(
        { outcome: "active" },
        { enabled: false, pollDownloads: false },
      );
      expect(mocks.useMediaRequest).toHaveBeenCalledWith("req-1", { enabled: false });
    });

    it("does not poll the viewer's requests while another of them downloads", () => {
      // A detail that does not name the request falls back to the list.
      const unnamed = { ...baseDetail, request: { ...baseDetail.request, request_id: undefined } };
      mocks.mine = [
        ownPending,
        {
          ...ownPending,
          id: "req-2",
          tmdb_id: 604,
          status: "downloading",
          download: { phase: "downloading", downloads: 1, updated_at: "2026-01-01T00:00:00Z" },
        },
      ];
      renderDetail(unnamed);

      // The title shows only its own progress, which comes from its detail.
      expect(primaryButton("Cancel request")).toBeEnabled();
      expect(mocks.useMyMediaRequests).toHaveBeenCalledWith(
        { outcome: "active" },
        { enabled: true, pollDownloads: false },
      );
    });

    it("shows how far the request's download is under its state", () => {
      renderDetail({
        ...baseDetail,
        request: {
          ...baseDetail.request,
          status: "downloading",
          state: "processing",
          download: {
            phase: "import_blocked",
            percent: 100,
            downloads: 1,
            updated_at: "2026-01-01T00:00:00Z",
          },
        },
      });

      expect(primaryButton("Processing")).toBeDisabled();
      expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
      expect(screen.getByText("Waiting for import")).toBeInTheDocument();
    });

    it("prefers the state the server derived", () => {
      renderDetail({
        ...baseDetail,
        request: { ...baseDetail.request, status: "approved", state: "approved" },
      });

      expect(primaryButton("Approved")).toBeDisabled();
    });

    it("lets the viewer follow a title someone else requested", () => {
      mocks.mine = [];
      renderDetail({
        ...baseDetail,
        request: { requestable: false, status: "approved", reason: "already_requested" },
      });

      expect(primaryButton("Approved")).toBeDisabled();
      const follow = primaryButton("Notify me when available");
      expect(follow).toHaveAttribute("aria-pressed", "false");
      fireEvent.click(follow);

      expect(mocks.toggleFollow).toHaveBeenCalledExactlyOnceWith({
        mediaType: "movie",
        tmdbID: 603,
        follow: true,
      });
      expect(screen.queryByRole("button", { name: "Cancel request" })).not.toBeInTheDocument();
    });

    it("lets a follower stop the notification", () => {
      mocks.mine = [];
      renderDetail({
        ...baseDetail,
        request: {
          requestable: false,
          status: "queued",
          reason: "already_requested",
          following: true,
        },
      });

      const button = primaryButton("Stop notifying me");
      expect(button).toHaveAttribute("aria-pressed", "true");
      fireEvent.click(button);

      expect(mocks.toggleFollow).toHaveBeenCalledExactlyOnceWith({
        mediaType: "movie",
        tmdbID: 603,
        follow: false,
      });
    });

    it("names why a title cannot be requested", () => {
      renderDetail({
        ...baseDetail,
        request: { requestable: false, reason: "quota_exceeded" },
      });

      expect(primaryButton("Request limit reached")).toBeDisabled();
    });

    it("links the title's IMDb and TMDB pages", () => {
      renderDetail({ ...baseDetail, media_type: "series", imdb_id: "tt0944947" });

      expect(screen.getByRole("link", { name: "IMDb" })).toHaveAttribute(
        "href",
        "https://www.imdb.com/title/tt0944947",
      );
      expect(screen.getByRole("link", { name: "TMDB" })).toHaveAttribute(
        "href",
        "https://www.themoviedb.org/tv/603",
      );
    });
  });

  describe("a title in the library", () => {
    const owned: RequestMediaDetail = {
      ...baseDetail,
      availability: "available",
      library_content_id: "movie-603",
      request: { requestable: false, reason: "already_available" },
    };

    it("redirects to the item page when the viewer can open it", () => {
      mocks.library = libraryQuery({ data: { content_id: "movie-603", title: "The Matrix" } });
      renderDetail(owned);

      expect(mocks.useCatalogItemDetail).toHaveBeenCalledWith("movie-603");
      expect(screen.getByTestId("location")).toHaveTextContent("/item/movie-603");
    });

    it("waits for the library check before showing the title", () => {
      mocks.library = libraryQuery({ isLoading: true });
      renderDetail(owned);

      expect(screen.getByText("Loading title")).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "The Matrix" })).not.toBeInTheDocument();
    });

    it("stays on the title without a link when the viewer cannot open it", () => {
      mocks.library = libraryQuery({ isError: true, error: problem(404) });
      renderDetail(owned);

      expect(screen.getByRole("heading", { name: "The Matrix" })).toBeInTheDocument();
      expect(primaryButton("In the library")).toBeDisabled();
      expect(
        screen
          .queryAllByRole("link")
          .filter((link) => link.getAttribute("href")?.includes("/item/")),
      ).toHaveLength(0);
    });

    it("leaves a failed check to the item page", () => {
      mocks.library = libraryQuery({ isError: true, error: problem(500) });
      renderDetail(owned);

      fireEvent.click(primaryButton("Open in library"));

      expect(screen.getByTestId("location")).toHaveTextContent("/item/movie-603");
    });

    it("skips the library check for a title outside the library", () => {
      renderDetail();

      expect(mocks.useCatalogItemDetail).toHaveBeenCalledWith(undefined);
    });
  });

  describe("unavailable titles", () => {
    it.each(["/title/tv/1399", "/title/person/1", "/title/movie/abc", "/title/movie/0"])(
      "shows %s as unavailable without loading anything",
      (path) => {
        renderAt(path);

        expect(screen.getByRole("heading", { name: "This item isn't available" })).toBeVisible();
        expect(mocks.useRequestMediaDetail).not.toHaveBeenCalled();
      },
    );

    it("reads a 404 from the detail API like a missing library item", () => {
      mocks.detail = detailQuery(undefined, { isError: true, error: problem(404) });
      renderAt("/title/movie/603");

      expect(screen.getByRole("heading", { name: "This item isn't available" })).toBeVisible();
      expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
    });

    it("offers a retry when the detail read fails", () => {
      mocks.detail = detailQuery(undefined, { isError: true, error: problem(500) });
      renderAt("/title/movie/603");

      expect(screen.getByRole("heading", { name: "Couldn't load this title" })).toBeVisible();
      fireEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(mocks.refetch).toHaveBeenCalledOnce();
    });

    it("shows the skeleton while the title loads", () => {
      mocks.detail = detailQuery(undefined, { isLoading: true });
      renderAt("/title/movie/603");

      expect(screen.getByText("Loading title")).toBeInTheDocument();
    });
  });

  describe("legacy request links", () => {
    it("redirects /requests/:mediaType/:tmdbId to the title page", () => {
      renderAt("/requests/series/1399");

      expect(mocks.useRequestMediaDetail).toHaveBeenCalledWith("series", 1399);
    });

    it("no longer reads an unknown media type as a movie", () => {
      renderAt("/requests/tv/1399");

      expect(screen.getByRole("heading", { name: "This item isn't available" })).toBeVisible();
      expect(mocks.useRequestMediaDetail).not.toHaveBeenCalled();
    });
  });

  describe("presentation", () => {
    it("formats the runtime with the shared runtime formatter", () => {
      renderDetail();

      expect(screen.getByText("2h 16m")).toBeInTheDocument();
    });

    it("shows a series' air years, seasons, status, and TMDB score", () => {
      renderDetail({
        ...baseDetail,
        media_type: "series",
        first_air_date: "2011-04-17",
        last_air_date: "2019-05-19",
        number_of_seasons: 8,
        status: "Ended",
        vote_average: 8.4,
        vote_count: 24100,
        creators: ["David Benioff", "D. B. Weiss"],
      });

      expect(screen.getByText("2011–2019")).toBeInTheDocument();
      expect(screen.getByText("8 Seasons")).toBeInTheDocument();
      expect(screen.getByText("Ended")).toBeInTheDocument();
      expect(screen.getByText("8.4")).toBeInTheDocument();
      expect(screen.getByText(/TMDB/, { selector: "span" })).toHaveTextContent("24.1K votes");
      expect(screen.getByText("Created by")).toBeInTheDocument();
      expect(screen.getByText("David Benioff")).toBeInTheDocument();
    });

    it("lists the cast and links recommendations to their title pages", () => {
      const recommendation: RequestMediaResult = {
        media_type: "movie",
        tmdb_id: 604,
        title: "The Matrix Reloaded",
        availability: "missing",
        request: { requestable: true },
      };
      renderDetail({
        ...baseDetail,
        cast: [{ name: "Keanu Reeves", character: "Neo", order: 0 }],
        recommendations: [recommendation],
      });

      expect(screen.getByRole("heading", { name: "Cast" })).toBeInTheDocument();
      expect(screen.getByText("Keanu Reeves")).toBeInTheDocument();
      const moreLikeThis = screen.getByRole("region", { name: "More Like This" });
      // Like a library card, the artwork and the caption title both link to the title.
      const links = within(moreLikeThis).getAllByRole("link", { name: /The Matrix Reloaded/ });
      expect(links.length).toBeGreaterThan(0);
      for (const link of links) {
        expect(link).toHaveAttribute("href", "/title/movie/604");
      }
    });
  });
});
