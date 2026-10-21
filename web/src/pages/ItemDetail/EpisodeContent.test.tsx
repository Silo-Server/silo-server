import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ItemDetail, Season } from "@/api/types";
import EpisodeContent from "./EpisodeContent";
import { makeFileVersion, makeItemDetail } from "./itemDetail.test-support";

const mocks = vi.hoisted(() => {
  type Props = Record<string, unknown> | null;
  return {
    capturedActionBarProps: { value: null as Props },
    capturedDetailHeroProps: { value: null as Props },
    useSeasonDetail: vi.fn(),
    useSeasonEpisodes: vi.fn(),
    useAuth: vi.fn(),
    useCurrentProfile: vi.fn(),
    useRedetectItemMarkers: vi.fn(),
    useRedetectEpisodeIntro: vi.fn(),
    useAdminMarkerCapabilities: vi.fn(),
    useLibraryCapabilities: vi.fn(),
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

vi.mock("@/pages/watchtogether/DetailWatchTogether", () => ({
  useDetailWatchTogether: () => ({ menu: undefined, sheet: null }),
}));
vi.mock("@/hooks/queries/episodes", () => ({
  useSeasonDetail: mocks.useSeasonDetail,
  useSeasonEpisodes: mocks.useSeasonEpisodes,
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: mocks.useAuth,
  useOptionalAuth: mocks.useAuth,
}));

vi.mock("@/hooks/queries/qualityPreference", () => ({
  // The resolution cap comes from the settings contract; these tests render
  // without a QueryClient, so the hook stands in for the resolved answer.
  useQualityPreference: (fallback?: string | null) => fallback ?? null,
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

vi.mock("@/hooks/queries/admin/markers", () => ({
  useAdminMarkerCapabilities: mocks.useAdminMarkerCapabilities,
  useMarkerDetectionKinds: () => undefined,
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useLibraryCapabilities: mocks.useLibraryCapabilities,
}));

const resetProgressMocks = vi.hoisted(() => ({
  useResetProgressAction: vi.fn((): (() => void) | undefined => undefined),
}));

vi.mock("@/hooks/queries/progress", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/progress")>()),
  useResetProgressAction: resetProgressMocks.useResetProgressAction,
}));

vi.mock("@/hooks/queries/items", () => ({
  useRedetectItemMarkers: mocks.useRedetectItemMarkers,
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

const spoilerPrefs = vi.hoisted(() => ({ hideImages: false, hideOverviews: false }));

vi.mock("@/hooks/useEpisodeSpoilerPrefs", () => ({
  useEpisodeSpoilerPrefs: () => spoilerPrefs,
}));

vi.mock("./DetailHero", () => ({
  default: (
    props: { context?: ReactNode; actions?: ReactNode; metadata?: ReactNode } & Record<
      string,
      unknown
    >,
  ) => {
    mocks.capturedDetailHeroProps.value = props;
    return (
      <div>
        {props.context}
        {props.metadata}
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

function makeEpisodeItem(overrides: Partial<ItemDetail> = {}) {
  return makeItemDetail("episode", {
    title: "Pilot",
    overview: "Episode overview",
    runtime: 42,
    content_rating: "TV-14",
    series_id: "series-1",
    series_title: "Example Series",
    season_number: 1,
    episode_number: 1,
    air_date: "2024-01-01",
    versions: [makeFileVersion({ duration: 2520 })],
    ...overrides,
  });
}

function renderEpisode() {
  renderToStaticMarkup(
    <MemoryRouter initialEntries={["/item/episode-1"]}>
      <EpisodeContent item={makeEpisodeItem()} />
    </MemoryRouter>,
  );
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
    mocks.useRedetectItemMarkers.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
    });
    mocks.useRedetectEpisodeIntro.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
    });
    mocks.useAdminMarkerCapabilities.mockReturnValue({ data: undefined });
    mocks.useLibraryCapabilities.mockReturnValue({ data: undefined });
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

  it("hides an unwatched episode's overview until the viewer reveals it", () => {
    spoilerPrefs.hideOverviews = true;
    try {
      render(
        <MemoryRouter initialEntries={["/item/episode-1"]}>
          <EpisodeContent item={makeEpisodeItem({ user_data: { played: false } })} />
        </MemoryRouter>,
      );
      expect(mocks.capturedDetailHeroProps.value?.overviewHidden).toBe(true);

      act(() => {
        (mocks.capturedDetailHeroProps.value?.onRevealOverview as () => void)();
      });
      expect(mocks.capturedDetailHeroProps.value?.overviewHidden).toBe(false);
    } finally {
      spoilerPrefs.hideOverviews = false;
    }
  });

  it("passes marker re-detection only for admins", () => {
    const redetect = vi.fn();
    mocks.useAuth.mockReturnValue({ user: { role: "admin" } });
    mocks.useAdminMarkerCapabilities.mockReturnValue({ data: { redetect_markers: true } });
    mocks.useRedetectItemMarkers.mockReturnValue({
      mutate: redetect,
      isPending: false,
    });

    renderEpisode();

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      isAdmin: true,
      isRedetectingMarkers: false,
    });
    expect(mocks.capturedActionBarProps.value?.redetectKind).toBeUndefined();
    const onRedetectMarkers = mocks.capturedActionBarProps.value?.onRedetectMarkers;
    expect(typeof onRedetectMarkers).toBe("function");
    (onRedetectMarkers as (kind: string) => void)("intro");
    expect(redetect).toHaveBeenCalledWith({ itemId: "episode-1", kind: "intro" });
    expect(mocks.useRedetectItemMarkers).toHaveBeenCalledWith({ introFallback: true });

    mocks.useAuth.mockReturnValue({ user: null });
    renderEpisode();
    expect(mocks.capturedActionBarProps.value?.onRedetectMarkers).toBeUndefined();
  });

  it.each([["a node without redetect_markers", { data: { movie_credits: true } }]])(
    "keeps intro-only re-detection for %s",
    (_label, capability) => {
      const redetectIntro = vi.fn();
      const redetectMarkers = vi.fn();
      mocks.useAuth.mockReturnValue({ user: { role: "admin" } });
      mocks.useAdminMarkerCapabilities.mockReturnValue(capability);
      mocks.useRedetectEpisodeIntro.mockReturnValue({ mutate: redetectIntro, isPending: false });
      mocks.useRedetectItemMarkers.mockReturnValue({ mutate: redetectMarkers, isPending: false });

      renderEpisode();

      expect(mocks.capturedActionBarProps.value).toMatchObject({ redetectKind: "intro" });
      const onRedetectMarkers = mocks.capturedActionBarProps.value?.onRedetectMarkers;
      (onRedetectMarkers as (kind: string) => void)("intro");
      expect(redetectIntro).toHaveBeenCalledWith("episode-1");
      expect(redetectMarkers).not.toHaveBeenCalled();
    },
  );
});
