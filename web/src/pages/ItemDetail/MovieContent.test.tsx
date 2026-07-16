import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { act, render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FileVersion, ItemDetail } from "@/api/types";
import MovieContent from "./MovieContent";

const mocks = vi.hoisted(() => {
  let capturedActionBarProps: Record<string, unknown> | null = null;
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

vi.mock("@/hooks/useOnViewTranslation", () => ({
  useOnViewTranslation: () => ({ translating: false, onTranslate: undefined }),
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

vi.mock("@/hooks/queries/ratings", () => ({
  useRating: mocks.useRating,
  useSetRating: mocks.useSetRating,
  useDeleteRating: mocks.useDeleteRating,
}));

vi.mock("@/hooks/queries/recommendations", () => ({
  useSimilarItems: mocks.useSimilarItems,
}));

vi.mock("@/hooks/queries/subtitles", () => ({
  useDeleteSubtitlePreference: () => ({ mutate: vi.fn() }),
  useSetSubtitlePreference: () => ({ mutate: vi.fn() }),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: mocks.useAuth,
  useOptionalAuth: mocks.useAuth,
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
  default: ({ actions, metadata }: { actions?: ReactNode; metadata?: ReactNode }) => (
    <div>
      {metadata}
      {actions}
    </div>
  ),
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

vi.mock("./components/HeroCrewLine", () => ({
  default: () => <div />,
}));

vi.mock("./components/ActionBar", () => ({
  default: (props: Record<string, unknown>) => {
    mocks.capturedActionBarProps.value = props;
    return <div />;
  },
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
    duration: overrides.duration ?? 7200,
    bitrate: overrides.bitrate ?? 0,
    edition_raw: overrides.edition_raw,
    edition_key: overrides.edition_key,
    audio_tracks: overrides.audio_tracks,
  };
}

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
    versions: [makeFileVersion()],
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
    mocks.capturedMetadataBadgesProps.value = null;
    mocks.capturedQualityBadgesProps.value = null;
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

  it("updates hero metadata when the selected version changes", () => {
    const standard = makeFileVersion({
      file_id: 1,
      resolution: "2160p",
      codec_video: "hevc",
      codec_audio: "eac3",
      hdr: true,
      duration: 9780,
    });
    const directorsCut = makeFileVersion({
      file_id: 2,
      resolution: "1080p",
      codec_video: "h264",
      codec_audio: "dts",
      hdr: false,
      duration: 11760,
      edition_raw: "Director's Cut",
      edition_key: "directors_cut",
    });

    render(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem({ runtime: 163, versions: [standard, directorsCut] })} />
      </MemoryRouter>,
    );

    expect(mocks.capturedMetadataBadgesProps.value).toMatchObject({ duration: "2h 43m" });
    expect(mocks.capturedQualityBadgesProps.value).toEqual({
      summary: {
        durationMinutes: 163,
        resolution: "2160p",
        videoRangeLabel: "HDR",
        audioLabel: "EAC3",
      },
    });

    act(() => {
      const selectVersion = mocks.capturedActionBarProps.value?.onSelectVersion as
        | ((version: FileVersion) => void)
        | undefined;
      selectVersion?.(directorsCut);
    });

    expect(mocks.capturedMetadataBadgesProps.value).toMatchObject({ duration: "3h 16m" });
    expect(mocks.capturedQualityBadgesProps.value).toEqual({
      summary: {
        durationMinutes: 196,
        resolution: "1080p",
        videoRangeLabel: "",
        audioLabel: "DTS",
      },
    });
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
              // Completed rows store position 0 (no resume point).
              played: true,
              position_seconds: 0,
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

  it("offers resume and restart during a rewatch of a completed movie", () => {
    renderToStaticMarkup(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent
          item={makeMovieItem({
            user_data: {
              // Rewatch in flight: watched latch stays, live resume point.
              played: true,
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
