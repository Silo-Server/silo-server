import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";
import MovieContent from "./MovieContent";

const mocks = vi.hoisted(() => {
  let capturedActionBarProps: Record<string, unknown> | null = null;

  return {
    capturedActionBarProps: {
      get value() {
        return capturedActionBarProps;
      },
      set value(value: Record<string, unknown> | null) {
        capturedActionBarProps = value;
      },
    },
    useIsFavorite: vi.fn(),
    useToggleFavorite: vi.fn(),
    useIsInWatchlist: vi.fn(),
    useToggleWatchlist: vi.fn(),
    useRefreshItemMetadata: vi.fn(),
    useWatchedStateMutation: vi.fn(),
    useRating: vi.fn(),
    useSetRating: vi.fn(),
    useDeleteRating: vi.fn(),
    useSimilarItems: vi.fn(),
    useAuth: vi.fn(),
    useCurrentProfile: vi.fn(),
    startPlayback: vi.fn(),
  };
});

vi.mock("@/hooks/queries/favorites", () => ({
  useIsFavorite: mocks.useIsFavorite,
  useToggleFavorite: mocks.useToggleFavorite,
}));

vi.mock("@/hooks/queries/watchlist", () => ({
  useIsInWatchlist: mocks.useIsInWatchlist,
  useToggleWatchlist: mocks.useToggleWatchlist,
}));

vi.mock("@/hooks/queries/items", () => ({
  useRefreshItemMetadata: mocks.useRefreshItemMetadata,
  useWatchedStateMutation: mocks.useWatchedStateMutation,
}));

vi.mock("@/hooks/queries/ratings", () => ({
  useRating: mocks.useRating,
  useSetRating: mocks.useSetRating,
  useDeleteRating: mocks.useDeleteRating,
}));

vi.mock("@/hooks/queries/recommendations", () => ({
  useSimilarItems: mocks.useSimilarItems,
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: mocks.useAuth,
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: mocks.useCurrentProfile,
}));

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({
    startPlayback: mocks.startPlayback,
  }),
}));

vi.mock("@/components/CastCarousel", () => ({
  default: () => <div />,
}));

vi.mock("@/components/CrewList", () => ({
  default: () => <div />,
}));

vi.mock("@/components/DownloadVersionPicker", () => ({
  default: () => <div />,
}));

vi.mock("@/components/EditMetadataDialog", () => ({
  default: () => <div />,
}));

vi.mock("@/components/MatchItemDialog", () => ({
  default: () => <div />,
}));

vi.mock("./components/SubtitleSearchDialog", () => ({
  default: () => <div />,
}));

vi.mock("@/components/RecommendationGrid", () => ({
  default: () => <div />,
}));

vi.mock("./DetailHero", () => ({
  default: ({ actions }: { actions?: ReactNode }) => <div>{actions}</div>,
}));

vi.mock("./components/MetadataBadges", () => ({
  default: () => <div />,
}));

vi.mock("./components/QualityBadges", () => ({
  default: () => <div />,
}));

vi.mock("./components/ScoreRow", () => ({
  default: () => <div />,
}));

vi.mock("./components/HeroCrewLine", () => ({
  default: () => <div />,
}));

vi.mock("./components/ActionBar", () => ({
  default: (props: Record<string, unknown>) => {
    mocks.capturedActionBarProps.value = props;
    return <div />;
  },
}));

function makeMovieItem(overrides: Partial<ItemDetail & { type: "movie" }> = {}): ItemDetail & {
  type: "movie";
} {
  return {
    content_id: "movie-1",
    type: "movie",
    title: "Example Movie",
    year: 2024,
    overview: "Movie overview",
    runtime: 120,
    content_rating: "PG-13",
    genres: [],
    rating_imdb: 8.2,
    rating_tmdb: null,
    rating_rt_critic: null,
    rating_rt_audience: null,
    imdb_id: "",
    tmdb_id: "",
    tvdb_id: "",
    cast: [],
    crew: [],
    studios: [],
    networks: [],
    countries: [],
    first_air_date: null,
    last_air_date: null,
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
    season_count: null,
    series_id: "",
    series_title: "",
    season_number: null,
    episode_number: null,
    air_date: null,
    versions: [{ file_id: 1 }] as ItemDetail["versions"],
    subtitles: [],
    intro: null,
    credits: null,
    ...overrides,
    release_date: overrides.release_date ?? null,
  };
}

describe("MovieContent", () => {
  beforeEach(() => {
    mocks.capturedActionBarProps.value = null;
    mocks.useIsFavorite.mockReturnValue({ data: false });
    mocks.useToggleFavorite.mockReturnValue({ mutate: vi.fn() });
    mocks.useIsInWatchlist.mockReturnValue({ data: false });
    mocks.useToggleWatchlist.mockReturnValue({ mutate: vi.fn() });
    mocks.useRefreshItemMetadata.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useWatchedStateMutation.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useRating.mockReturnValue({ data: { rating: 4, rated_at: "2026-03-22T00:00:00Z" } });
    mocks.useSetRating.mockReturnValue({ mutate: vi.fn() });
    mocks.useDeleteRating.mockReturnValue({ mutate: vi.fn() });
    mocks.useSimilarItems.mockReturnValue({ data: { items: [] }, isLoading: false });
    mocks.useAuth.mockReturnValue({ user: null });
    mocks.useCurrentProfile.mockReturnValue({ profile: null });
  });

  it("passes restartHref when the movie is partially watched", () => {
    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent
          item={makeMovieItem({
            user_data: {
              played: false,
              is_in_progress: true,
              position_seconds: 600,
              duration_seconds: 3600,
            },
          })}
        />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      playLabel: "Resume",
      restartHref: "/watch/movie-1?restart=1",
    });
  });

  it("does not pass restartHref when the movie is already completed", () => {
    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent
          item={makeMovieItem({
            user_data: {
              played: true,
              position_seconds: 3600,
              duration_seconds: 3600,
            },
          })}
        />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      playLabel: "Play",
      restartHref: undefined,
    });
  });

  it("allows marker editing without metadata curation permission", () => {
    mocks.useAuth.mockReturnValue({
      user: { role: "user", permissions: ["marker_edit"], download_allowed: true },
    });

    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem()} />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      canCurateMetadata: false,
      canEditMarkers: true,
    });
  });
});
