import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ItemDetail, Season } from "@/api/types";
import EpisodeContent from "./EpisodeContent";

const mocks = vi.hoisted(() => {
  let capturedActionBarProps: Record<string, unknown> | null = null;
  let capturedDetailHeroProps: Record<string, unknown> | null = null;

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
    useSeasonDetail: vi.fn(),
    useSeasonEpisodes: vi.fn(),
    useAuth: vi.fn(),
    useCurrentProfile: vi.fn(),
    useRedetectEpisodeIntro: vi.fn(),
    useRefreshItemMetadata: vi.fn(),
    useWatchedStateMutation: vi.fn(),
    useRating: vi.fn(),
    useSetRating: vi.fn(),
    useDeleteRating: vi.fn(),
    useOnViewTranslation: vi.fn(),
    setRatingMutate: vi.fn(),
    deleteRatingMutate: vi.fn(),
    startPlayback: vi.fn(),
  };
});

vi.mock("@/hooks/queries/episodes", () => ({
  useSeasonDetail: mocks.useSeasonDetail,
  useSeasonEpisodes: mocks.useSeasonEpisodes,
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: mocks.useAuth,
  useOptionalAuth: mocks.useAuth,
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: mocks.useCurrentProfile,
}));

vi.mock("@/hooks/useOnViewTranslation", () => ({
  useOnViewTranslation: mocks.useOnViewTranslation,
}));

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({
    startPlayback: mocks.startPlayback,
  }),
}));

vi.mock("@/hooks/queries/items", () => ({
  useRedetectEpisodeIntro: mocks.useRedetectEpisodeIntro,
  useRefreshItemMetadata: mocks.useRefreshItemMetadata,
  useWatchedStateMutation: mocks.useWatchedStateMutation,
}));

vi.mock("@/hooks/queries/ratings", () => ({
  useRating: mocks.useRating,
  useSetRating: mocks.useSetRating,
  useDeleteRating: mocks.useDeleteRating,
}));

vi.mock("@/hooks/queries/subtitles", () => ({
  useDeleteSubtitlePreference: () => ({ mutate: vi.fn() }),
  useSetSubtitlePreference: () => ({ mutate: vi.fn() }),
}));

vi.mock("@/components/CastCarousel", () => ({
  default: () => <div />,
}));

vi.mock("@/components/EditMetadataDialog", () => ({
  default: () => <div />,
}));

vi.mock("@/components/DownloadVersionPicker", () => ({
  default: () => <div />,
}));

vi.mock("./DetailHero", () => ({
  default: (props: { context?: ReactNode; actions?: ReactNode } & Record<string, unknown>) => {
    mocks.capturedDetailHeroProps.value = props;
    return (
      <div>
        {props.context}
        {props.actions}
      </div>
    );
  },
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

vi.mock("./components/SubtitleSearchDialog", () => ({
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

vi.mock("./components/EpisodeCarousel", () => ({
  default: ({
    episodes,
    currentEpisodeNumber,
  }: {
    episodes: { content_id: string; episode_number: number; title: string }[];
    currentEpisodeNumber: number;
  }) => (
    <div data-testid="episode-carousel">
      {episodes.map((ep) => (
        <div
          key={ep.content_id}
          data-episode={ep.episode_number}
          data-current={ep.episode_number === currentEpisodeNumber ? "true" : undefined}
        >
          {ep.title}
        </div>
      ))}
    </div>
  ),
}));

function makeEpisodeItem(
  overrides: Partial<ItemDetail & { type: "episode" }> = {},
): ItemDetail & { type: "episode" } {
  return {
    content_id: "episode-1",
    title: "Pilot",
    year: 2024,
    overview: "Episode overview",
    runtime: 42,
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
    episode_number: 1,
    air_date: "2024-01-01",
    versions: [{ file_id: 1 } as ItemDetail["versions"][number]],
    subtitles: [],
    intro: null,
    credits: null,
    ...overrides,
    release_date: overrides.release_date ?? null,
    type: "episode",
  };
}

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

function countOccurrences(markup: string, fragment: string): number {
  return markup.split(fragment).length - 1;
}

describe("EpisodeContent", () => {
  beforeEach(() => {
    mocks.capturedActionBarProps.value = null;
    mocks.capturedDetailHeroProps.value = null;
    mocks.useAuth.mockReturnValue({ user: null });
    mocks.useCurrentProfile.mockReturnValue({ profile: null });
    mocks.useOnViewTranslation.mockReturnValue({ translating: false, onTranslate: undefined });
    mocks.useRefreshItemMetadata.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
    });
    mocks.useRedetectEpisodeIntro.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
    });
    mocks.useWatchedStateMutation.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
    });
    mocks.useSeasonEpisodes.mockReturnValue({
      data: { episodes: [] },
    });
    mocks.useSeasonDetail.mockReturnValue({
      data: makeSeason(),
    });
    mocks.useRating.mockReturnValue({ data: { rating: 3, rated_at: "2026-03-22T00:00:00Z" } });
  });

  it("links the season breadcrumb segment to the resolved season page", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem()} />
      </MemoryRouter>,
    );

    expect(countOccurrences(markup, 'href="/item/series-1"')).toBe(1);
    expect(countOccurrences(markup, 'href="/item/season-1"')).toBe(1);
    expect(markup).toContain(">Season 1<");
  });

  it("uses season navigation state before season detail finishes loading", () => {
    mocks.useSeasonDetail.mockReturnValue({
      data: undefined,
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter
        initialEntries={[
          {
            pathname: "/item/episode-1",
            state: {
              parentSeasonHref: "/item/season-99",
              parentSeasonLabel: "Season 99",
            },
          },
        ]}
      >
        <EpisodeContent item={makeEpisodeItem()} />
      </MemoryRouter>,
    );

    expect(countOccurrences(markup, 'href="/item/season-99"')).toBe(1);
    expect(markup).toContain(">Season 99<");
  });

  it("passes on-view translation controls to the hero", () => {
    const onTranslate = vi.fn();
    mocks.useOnViewTranslation.mockReturnValue({
      translating: true,
      onTranslate,
    });

    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem({ pending_translation_language: "fr" })} />
      </MemoryRouter>,
    );

    expect(mocks.useOnViewTranslation).toHaveBeenCalledWith(
      expect.objectContaining({ content_id: "episode-1", type: "episode" }),
    );
    expect(mocks.capturedDetailHeroProps.value).toMatchObject({
      overviewTranslating: true,
      onTranslateOverview: onTranslate,
    });
  });

  it("shows all season episodes in the carousel, not just nearby ones", () => {
    const allEpisodes = Array.from({ length: 10 }, (_, i) => ({
      content_id: `ep-${i + 1}`,
      season_number: 1,
      episode_number: i + 1,
      title: `Episode ${i + 1} Title`,
      overview: "",
      air_date: null,
      runtime: 42,
      still_url: "",
      still_thumbhash: "",
      files: [],
    }));

    mocks.useSeasonEpisodes.mockReturnValue({
      data: { episodes: allEpisodes },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem({ episode_number: 5 })} />
      </MemoryRouter>,
    );

    // All 10 episodes should be rendered
    for (let i = 1; i <= 10; i++) {
      expect(markup).toContain(`Episode ${i} Title`);
    }

    // Current episode (5) should be marked
    expect(markup).toContain('data-current="true"');
    expect(countOccurrences(markup, 'data-current="true"')).toBe(1);
  });

  it("hides the carousel when only one episode exists", () => {
    mocks.useSeasonEpisodes.mockReturnValue({
      data: {
        episodes: [
          {
            content_id: "ep-1",
            season_number: 1,
            episode_number: 1,
            title: "Only Episode",
            overview: "",
            air_date: null,
            runtime: 42,
            still_url: "",
            still_thumbhash: "",
            files: [],
          },
        ],
      },
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem({ episode_number: 1 })} />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("More Episodes");
    expect(markup).not.toContain("episode-carousel");
  });

  it("passes restartHref when the episode is partially watched", () => {
    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent
          item={makeEpisodeItem({
            user_data: {
              played: false,
              is_in_progress: true,
              position_seconds: 300,
              duration_seconds: 1800,
            },
          })}
        />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      playLabel: "Resume",
      restartHref: "/watch/episode-1?restart=1",
    });
  });

  it("does not pass restartHref when the episode is completed", () => {
    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent
          item={makeEpisodeItem({
            user_data: {
              // Completed rows store position 0 (no resume point).
              played: true,
              position_seconds: 0,
              duration_seconds: 1800,
            },
          })}
        />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      playLabel: "Play Episode",
    });
    expect(mocks.capturedActionBarProps.value).toMatchObject({
      restartHref: undefined,
    });
  });

  it("does not pass rating props to ActionBar", () => {
    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem()} />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).not.toHaveProperty("rating");
    expect(mocks.capturedActionBarProps.value).not.toHaveProperty("onRatingChange");
  });

  it("passes intro re-detection action only for admins", () => {
    const redetect = vi.fn();
    mocks.useAuth.mockReturnValue({ user: { role: "admin" } });
    mocks.useRedetectEpisodeIntro.mockReturnValue({
      mutate: redetect,
      isPending: false,
    });

    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem()} />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      isAdmin: true,
      isRedetectingIntro: false,
    });
    const onRedetectIntro = mocks.capturedActionBarProps.value?.onRedetectIntro;
    expect(typeof onRedetectIntro).toBe("function");
    (onRedetectIntro as () => void)();
    expect(redetect).toHaveBeenCalledWith("episode-1");

    mocks.useAuth.mockReturnValue({ user: null });
    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem()} />
      </MemoryRouter>,
    );
    expect(mocks.capturedActionBarProps.value?.onRedetectIntro).toBeUndefined();
  });
});
