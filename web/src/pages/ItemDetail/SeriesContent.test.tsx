import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CrewMember, ItemDetail, Season } from "@/api/types";
import { buildPersonCatalogHref } from "@/pages/catalogSearchParams";
import SeriesContent from "./SeriesContent";

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
    useAuth: vi.fn(),
    useIsActingAdmin: vi.fn(),
    useLibraryCapabilities: vi.fn(),
    useIsFavorite: vi.fn(),
    useToggleFavorite: vi.fn(),
    useIsInWatchlist: vi.fn(),
    useToggleWatchlist: vi.fn(),
    useRefreshItemMetadata: vi.fn(),
    useWatchedStateMutation: vi.fn(),
    useSeasons: vi.fn(),
    useItemEpisodes: vi.fn(),
    useSimilarItems: vi.fn(),
    useSetRating: vi.fn(),
    useDeleteRating: vi.fn(),
    setRatingMutate: vi.fn(),
    deleteRatingMutate: vi.fn(),
  };
});

vi.mock("@/hooks/queries/shuffles", () => ({
  useStartShuffle: () => ({ startShuffle: vi.fn(), isStarting: false }),
}));
vi.mock("@/pages/watchtogether/DetailWatchTogether", () => ({
  useDetailWatchTogether: () => ({ menu: undefined, sheet: null }),
}));
vi.mock("@/hooks/useOnViewTranslation", () => ({
  useOnViewTranslation: () => ({ translating: false, onTranslate: undefined }),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: mocks.useAuth,
  useOptionalAuth: mocks.useAuth,
}));

vi.mock("@/hooks/useIsActingAdmin", () => ({
  useIsActingAdmin: mocks.useIsActingAdmin,
}));

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

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useLibraryCapabilities: mocks.useLibraryCapabilities,
}));

vi.mock("@/hooks/queries/episodes", () => ({
  useSeasons: mocks.useSeasons,
  useItemEpisodes: mocks.useItemEpisodes,
}));

vi.mock("@/hooks/queries/recommendations", () => ({
  useSimilarItems: mocks.useSimilarItems,
}));

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: () => {} }),
}));

vi.mock("@/hooks/queries/ratings", () => ({
  useSetRating: mocks.useSetRating,
  useDeleteRating: mocks.useDeleteRating,
}));

vi.mock("@/components/CastCarousel", () => ({
  default: () => <div />,
}));

vi.mock("./DetailHero", () => ({
  default: ({ actions }: { actions?: ReactNode }) => <div>{actions}</div>,
}));

vi.mock("./SeasonCarousel", () => ({
  default: () => <div />,
}));

vi.mock("./components/MetadataBadges", () => ({
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

function makeSeason(overrides: Partial<Season> = {}): Season {
  return {
    content_id: "season-1",
    season_number: 1,
    is_specials: false,
    title: "Season 1",
    overview: "",
    air_date: null,
    episode_count: 8,
    poster_url: "",
    poster_thumbhash: "",
    ...overrides,
  };
}

function makeSeriesItem(
  overrides: Partial<ItemDetail & { type: "series" }> = {},
): ItemDetail & { type: "series" } {
  return {
    content_id: "series-1",
    type: "series",
    title: "Example Series",
    year: 2024,
    overview: "Series overview",
    runtime: 0,
    content_rating: "TV-14",
    genres: [],
    rating_imdb: 8.5,
    rating_tmdb: null,
    rating_rt_critic: null,
    rating_rt_audience: null,
    ratings: [],
    imdb_id: "",
    tmdb_id: "",
    tvdb_id: "",
    cast: [],
    crew: [],
    studios: [],
    networks: [],
    countries: [],
    first_air_date: "2024-01-01",
    last_air_date: "2024-02-01",
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
    versions: [],
    subtitles: [],
    intro: null,
    credits: null,
    user_rating: 4,
    ...overrides,
    release_date: overrides.release_date ?? null,
  };
}

describe("SeriesContent", () => {
  beforeEach(() => {
    mocks.capturedActionBarProps.value = null;
    mocks.setRatingMutate.mockReset();
    mocks.deleteRatingMutate.mockReset();
    mocks.useAuth.mockReturnValue({ user: null });
    mocks.useIsActingAdmin.mockReturnValue(false);
    mocks.useLibraryCapabilities.mockReset();
    mocks.useLibraryCapabilities.mockReturnValue({ data: undefined });
    mocks.useIsFavorite.mockReturnValue({ data: false });
    mocks.useToggleFavorite.mockReturnValue({ mutate: vi.fn() });
    mocks.useIsInWatchlist.mockReturnValue({ data: false });
    mocks.useToggleWatchlist.mockReturnValue({ mutate: vi.fn() });
    mocks.useRefreshItemMetadata.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useWatchedStateMutation.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useSeasons.mockReturnValue({ data: { seasons: [makeSeason()] } });
    mocks.useItemEpisodes.mockReturnValue({
      data: { episodes: [{ content_id: "episode-1" }] },
    });
    mocks.useSimilarItems.mockReturnValue({ data: undefined, isLoading: false });
    mocks.useSetRating.mockReturnValue({ mutate: mocks.setRatingMutate });
    mocks.useDeleteRating.mockReturnValue({ mutate: mocks.deleteRatingMutate });
  });

  it.each([
    [{ trickplay: true, trickplay_supported: true }, true],
    [{ trickplay: true, trickplay_supported: false }, false],
    [{ trickplay: true }, false],
    [{ trickplay: false }, false],
    [undefined, false],
  ])("offers series seek-preview administration with capability %o: %s", (data, offered) => {
    mocks.useIsActingAdmin.mockReturnValue(true);
    mocks.useLibraryCapabilities.mockReturnValue({ data });
    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <SeriesContent item={makeSeriesItem()} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(mocks.capturedActionBarProps.value?.canManageTrickplay).toBe(offered);
    expect(mocks.useLibraryCapabilities).toHaveBeenLastCalledWith(true);
  });

  it("sets and clears ratings through the existing mutations", () => {
    renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={["/item/series-1"]}>
          <SeriesContent item={makeSeriesItem()} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(mocks.capturedActionBarProps.value?.rating).toBe(4);

    const onRatingChange = mocks.capturedActionBarProps.value?.onRatingChange as
      | ((rating: number | null) => void)
      | undefined;

    expect(onRatingChange).toBeTypeOf("function");

    onRatingChange?.(5);
    onRatingChange?.(null);

    expect(mocks.setRatingMutate).toHaveBeenCalledWith(5);
    expect(mocks.deleteRatingMutate).toHaveBeenCalledTimes(1);
  });

  it("lists every creator in the Crew section, past the two the hero line shows", () => {
    const creators: CrewMember[] = [
      { name: "Creator One", job: "Creator", person_id: "creator-1" },
      { name: "Creator Two", job: "Creator", person_id: "creator-2" },
      { name: "Creator Three", job: "Creator", person_id: "creator-3" },
    ];
    const crew: CrewMember[] = [
      ...creators,
      { name: "Series Director", job: "Director", person_id: "director-1" },
    ];

    // DetailHero and HeroCrewLine are mocked, so these names can only come
    // from the Crew section.
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={["/item/series-1"]}>
          <SeriesContent item={makeSeriesItem({ crew })} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain(">Creators</dt>");
    for (const creator of creators) {
      expect(markup).toContain(`href="${buildPersonCatalogHref(creator.person_id)}"`);
      expect(markup).toContain(creator.name);
    }
    expect(markup).toContain(">Directors</dt>");
    expect(markup.indexOf(">Creators</dt>")).toBeLessThan(markup.indexOf(">Directors</dt>"));
  });
});
