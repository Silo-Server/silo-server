import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi, beforeEach } from "vitest";
import type { FileVersion, ItemDetail, Season } from "@/api/types";
import EpisodeContent from "./EpisodeContent";

const mocks = vi.hoisted(() => {
  let capturedActionBarProps: Record<string, unknown> | null = null;
  let capturedDetailHeroProps: Record<string, unknown> | null = null;
  let capturedMetadataBadgesProps: Record<string, unknown> | null = null;
  let capturedQualityBadgesProps: Record<string, unknown> | null = null;

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
    capturedMetadataBadgesProps: {
      get value() {
        return capturedMetadataBadgesProps;
      },
      set value(value: Record<string, unknown> | null) {
        capturedMetadataBadgesProps = value;
      },
    },
    capturedQualityBadgesProps: {
      get value() {
        return capturedQualityBadgesProps;
      },
      set value(value: Record<string, unknown> | null) {
        capturedQualityBadgesProps = value;
      },
    },
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
  default: (props: Record<string, unknown>) => {
    mocks.capturedMetadataBadgesProps.value = props;
    return <div />;
  },
}));

vi.mock("./components/QualityBadges", () => ({
  default: (props: Record<string, unknown>) => {
    mocks.capturedQualityBadgesProps.value = props;
    return <div />;
  },
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

function makeFileVersion(overrides: Partial<FileVersion> = {}): FileVersion {
  return {
    file_id: overrides.file_id ?? 1,
    resolution: overrides.resolution ?? "1080p",
    codec_video: overrides.codec_video ?? "h264",
    codec_audio: overrides.codec_audio ?? "aac",
    hdr: overrides.hdr ?? false,
    container: overrides.container ?? "mkv",
    file_size: overrides.file_size ?? 0,
    duration: overrides.duration ?? 2520,
    bitrate: overrides.bitrate ?? 0,
    edition_raw: overrides.edition_raw,
    edition_key: overrides.edition_key,
    audio_tracks: overrides.audio_tracks,
  };
}

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
    ratings: [],
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
    versions: [makeFileVersion()],
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
  it("disables collection membership without removing the item identity", () => {
    renderToStaticMarkup(
      <MemoryRouter>
        <EpisodeContent item={makeEpisodeItem()} />
      </MemoryRouter>,
    );
    expect(mocks.capturedActionBarProps.value).toMatchObject({
      contentId: "episode-1",
      canAddToCollection: false,
    });
  });

  it.each([
    [{ trickplay: true, trickplay_supported: true }, true],
    [{ trickplay: true, trickplay_supported: false }, false],
    [{ trickplay: true }, false],
    [undefined, false],
  ])("offers episode seek-preview administration with capability %o: %s", (data, offered) => {
    mocks.useAuth.mockReturnValue({ user: { role: "admin" } });
    mocks.useLibraryCapabilities.mockReturnValue({ data });
    renderToStaticMarkup(
      <MemoryRouter>
        <EpisodeContent item={makeEpisodeItem()} />
      </MemoryRouter>,
    );
    expect(mocks.capturedActionBarProps.value?.canManageTrickplay).toBe(offered);
  });

  beforeEach(() => {
    mocks.capturedActionBarProps.value = null;
    mocks.capturedDetailHeroProps.value = null;
    mocks.capturedMetadataBadgesProps.value = null;
    mocks.capturedQualityBadgesProps.value = null;
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

  it("updates hero metadata when the selected episode version changes", () => {
    const hdrVersion = makeFileVersion({
      file_id: 1,
      resolution: "2160p",
      codec_video: "hevc",
      codec_audio: "eac3",
      hdr: true,
      duration: 2520,
    });
    const sdrVersion = makeFileVersion({
      file_id: 2,
      resolution: "1080p",
      codec_video: "h264",
      codec_audio: "aac",
      hdr: false,
      duration: 2700,
    });

    render(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem({ versions: [hdrVersion, sdrVersion] })} />
      </MemoryRouter>,
    );

    expect(mocks.capturedMetadataBadgesProps.value).toMatchObject({ duration: "42m" });
    expect(mocks.capturedQualityBadgesProps.value).toEqual({
      summary: {
        durationMinutes: 42,
        resolution: "2160p",
        videoRangeLabel: "HDR",
        audioLabel: "EAC3",
      },
    });

    act(() => {
      const selectVersion = mocks.capturedActionBarProps.value?.onSelectVersion as
        | ((version: FileVersion) => void)
        | undefined;
      selectVersion?.(sdrVersion);
    });

    expect(mocks.capturedMetadataBadgesProps.value).toMatchObject({ duration: "45m" });
    expect(mocks.capturedQualityBadgesProps.value).toEqual({
      summary: {
        durationMinutes: 45,
        resolution: "1080p",
        videoRangeLabel: "",
        audioLabel: "AAC",
      },
    });
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

  it("passes marker re-detection only for admins", () => {
    const redetect = vi.fn();
    mocks.useAuth.mockReturnValue({ user: { role: "admin" } });
    mocks.useAdminMarkerCapabilities.mockReturnValue({ data: { redetect_markers: true } });
    mocks.useRedetectItemMarkers.mockReturnValue({
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
      isRedetectingMarkers: false,
    });
    expect(mocks.capturedActionBarProps.value?.redetectKind).toBeUndefined();
    const onRedetectMarkers = mocks.capturedActionBarProps.value?.onRedetectMarkers;
    expect(typeof onRedetectMarkers).toBe("function");
    (onRedetectMarkers as (kind: string) => void)("intro");
    expect(redetect).toHaveBeenCalledWith({ itemId: "episode-1", kind: "intro" });
    expect(mocks.useRedetectItemMarkers).toHaveBeenCalledWith({ introFallback: true });

    mocks.useAuth.mockReturnValue({ user: null });
    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem()} />
      </MemoryRouter>,
    );
    expect(mocks.capturedActionBarProps.value?.onRedetectMarkers).toBeUndefined();
  });

  it.each([
    ["a pending capability read", { data: undefined }],
    ["a failed capability read", { data: undefined, isError: true }],
    ["a node without redetect_markers", { data: { movie_credits: true } }],
    ["a node with redetect_markers off", { data: { redetect_markers: false } }],
  ])("keeps intro-only re-detection for %s", (_label, capability) => {
    const redetectIntro = vi.fn();
    const redetectMarkers = vi.fn();
    mocks.useAuth.mockReturnValue({ user: { role: "admin" } });
    mocks.useAdminMarkerCapabilities.mockReturnValue(capability);
    mocks.useRedetectEpisodeIntro.mockReturnValue({ mutate: redetectIntro, isPending: false });
    mocks.useRedetectItemMarkers.mockReturnValue({ mutate: redetectMarkers, isPending: false });

    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem()} />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({ redetectKind: "intro" });
    const onRedetectMarkers = mocks.capturedActionBarProps.value?.onRedetectMarkers;
    (onRedetectMarkers as (kind: string) => void)("intro");
    expect(redetectIntro).toHaveBeenCalledWith("episode-1");
    expect(redetectMarkers).not.toHaveBeenCalled();
  });
});
