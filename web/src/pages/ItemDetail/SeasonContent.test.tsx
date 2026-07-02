import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";
import SeasonContent from "./SeasonContent";

const mocks = vi.hoisted(() => {
  let capturedActionBarProps: Record<string, unknown> | null = null;
  let capturedDetailHeroProps: Record<string, unknown> | null = null;
  const capturedMediaMenuProps: Record<string, unknown>[] = [];

  return {
    capturedActionBarProps: {
      get value() {
        return capturedActionBarProps;
      },
      set value(value: Record<string, unknown> | null) {
        capturedActionBarProps = value;
      },
    },
    capturedDetailHeroProps: {
      get value() {
        return capturedDetailHeroProps;
      },
      set value(value: Record<string, unknown> | null) {
        capturedDetailHeroProps = value;
      },
    },
    capturedMediaMenuProps,
    useItemEpisodes: vi.fn(),
    useRefreshItemMetadata: vi.fn(),
    useWatchedStateMutation: vi.fn(),
    useOnViewTranslation: vi.fn(),
    useRating: vi.fn(),
    useSetRating: vi.fn(),
    useDeleteRating: vi.fn(),
    useAuth: vi.fn(),
  };
});

vi.mock("@/hooks/queries/episodes", () => ({
  useItemEpisodes: mocks.useItemEpisodes,
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

vi.mock("@/hooks/useAuth", () => ({
  useAuth: mocks.useAuth,
  useOptionalAuth: mocks.useAuth,
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: mocks.useAuth()?.profile ?? null }),
}));

vi.mock("@/hooks/useOnViewTranslation", () => ({
  useOnViewTranslation: mocks.useOnViewTranslation,
}));

vi.mock("@/components/MediaItemMenu", () => ({
  default: (props: Record<string, unknown>) => {
    mocks.capturedMediaMenuProps.push(props);
    return <div />;
  },
}));

vi.mock("@/components/CastCarousel", () => ({
  default: () => <div />,
}));

vi.mock("@/components/CrewList", () => ({
  default: () => <div />,
}));

vi.mock("@/components/ui/skeleton", () => ({
  Skeleton: () => <div />,
}));

vi.mock("./DetailHero", () => ({
  default: (props: { actions?: ReactNode } & Record<string, unknown>) => {
    mocks.capturedDetailHeroProps.value = props;
    return <div>{props.actions}</div>;
  },
}));

vi.mock("./components/MetadataBadges", () => ({
  default: () => <div />,
}));

vi.mock("./components/ActionBar", () => ({
  default: (props: Record<string, unknown>) => {
    mocks.capturedActionBarProps.value = props;
    return <div />;
  },
}));

function makeSeasonItem(
  overrides: Partial<ItemDetail & { type: "season" }> = {},
): ItemDetail & { type: "season" } {
  return {
    content_id: "season-1",
    type: "season",
    title: "Season 1",
    year: 2024,
    overview: "Season overview",
    runtime: 0,
    content_rating: "TV-14",
    genres: [],
    rating_imdb: null,
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
    series_id: "series-1",
    series_title: "Example Series",
    season_number: 1,
    episode_number: null,
    air_date: "2024-01-01",
    episode_count: 8,
    is_specials: false,
    versions: [],
    subtitles: [],
    intro: null,
    credits: null,
    ...overrides,
    release_date: overrides.release_date ?? null,
  };
}

describe("SeasonContent", () => {
  beforeEach(() => {
    mocks.capturedActionBarProps.value = null;
    mocks.capturedDetailHeroProps.value = null;
    mocks.capturedMediaMenuProps.length = 0;
    mocks.useAuth.mockReturnValue({ user: null });
    mocks.useOnViewTranslation.mockReturnValue({ translating: false, onTranslate: undefined });
    mocks.useRefreshItemMetadata.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useWatchedStateMutation.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useItemEpisodes.mockReturnValue({
      data: {
        episodes: [
          {
            content_id: "episode-1",
            title: "Pilot",
            episode_number: 1,
            still_url: "",
            user_data: null,
          },
        ],
      },
      isLoading: false,
      error: null,
    });
    mocks.useRating.mockReturnValue({ data: { rating: 4, rated_at: "2026-03-22T00:00:00Z" } });
    mocks.useSetRating.mockReturnValue({ mutate: vi.fn() });
    mocks.useDeleteRating.mockReturnValue({ mutate: vi.fn() });
  });

  it("does not pass rating props to ActionBar", () => {
    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/season-1"]}>
        <SeasonContent item={makeSeasonItem()} />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).not.toHaveProperty("rating");
    expect(mocks.capturedActionBarProps.value).not.toHaveProperty("onRatingChange");
  });

  it("passes partial-progress restart eligibility to episode menus", () => {
    mocks.useItemEpisodes.mockReturnValue({
      data: {
        episodes: [
          {
            content_id: "episode-1",
            title: "Pilot",
            episode_number: 1,
            still_url: "",
            runtime: 42,
            air_date: null,
            overview: "",
            user_data: {
              played: false,
              position_seconds: 120,
              duration_seconds: 1800,
            },
          },
        ],
      },
      isLoading: false,
      error: null,
    });

    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/season-1"]}>
        <SeasonContent item={makeSeasonItem()} />
      </MemoryRouter>,
    );

    expect(mocks.capturedMediaMenuProps[0]).toMatchObject({
      contentId: "episode-1",
      mediaType: "episode",
      hasPartialProgress: true,
    });
  });

  it("passes on-view translation controls to the hero", () => {
    const onTranslate = vi.fn();
    mocks.useOnViewTranslation.mockReturnValue({
      translating: true,
      onTranslate,
    });

    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/season-1"]}>
        <SeasonContent item={makeSeasonItem({ pending_translation_language: "fr" })} />
      </MemoryRouter>,
    );

    expect(mocks.useOnViewTranslation).toHaveBeenCalledWith(
      expect.objectContaining({ content_id: "season-1", type: "season" }),
    );
    expect(mocks.capturedDetailHeroProps.value).toMatchObject({
      overviewTranslating: true,
      onTranslateOverview: onTranslate,
    });
  });
});
