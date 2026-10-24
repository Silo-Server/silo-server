import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { act, render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FileVersion, ItemDetail } from "@/api/types";
import { makeFileVersion, makeItemDetail } from "./itemDetail.test-support";
import MovieContent from "./MovieContent";

const mocks = vi.hoisted(() => {
  type Props = Record<string, unknown> | null;
  return {
    capturedActionBarProps: { value: null as Props },
    capturedMetadataBadgesProps: { value: null as Props },
    capturedQualityBadgesProps: { value: null as Props },
    useIsFavorite: vi.fn(),
    useToggleFavorite: vi.fn(),
    useIsInWatchlist: vi.fn(),
    useToggleWatchlist: vi.fn(),
    useRefreshItemMetadata: vi.fn(),
    useRedetectItemMarkers: vi.fn(),
    useAdminMarkerCapabilities: vi.fn(),
    useLibraryCapabilities: vi.fn(),
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

vi.mock("@/pages/watchtogether/DetailWatchTogether", () => ({
  useDetailWatchTogether: () => ({ menu: undefined, sheet: null }),
}));
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

vi.mock("@/hooks/queries/notInterested", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/notInterested")>()),
  useToggleNotInterested: () => ({ isPending: false, mutate: vi.fn(), mutateAsync: vi.fn() }),
}));

vi.mock("@/hooks/queries/admin/markers", () => ({
  useAdminMarkerCapabilities: mocks.useAdminMarkerCapabilities,
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useLibraryCapabilities: mocks.useLibraryCapabilities,
}));

vi.mock("@/hooks/queries/items", () => ({
  useRefreshItemMetadata: mocks.useRefreshItemMetadata,
  useRedetectItemMarkers: mocks.useRedetectItemMarkers,
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

vi.mock("@/hooks/queries/qualityPreference", () => ({
  // The resolution cap comes from the settings contract; these tests render
  // without a QueryClient, so the hook stands in for the resolved answer.
  useQualityPreference: (fallback?: string | null) => fallback ?? null,
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

vi.mock("@/components/DownloadVersionPicker", () => ({
  default: () => <div />,
}));

vi.mock("@/components/EditMetadataDialog", () => ({
  default: () => <div />,
}));

vi.mock("@/components/MatchItemDialog", () => ({
  default: () => <div />,
}));

vi.mock("@/components/SplitItemDialog", () => ({
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

const movieVersion = (overrides: Partial<FileVersion> = {}) =>
  makeFileVersion({ duration: 7200, ...overrides });

function makeMovieItem(overrides: Partial<ItemDetail> = {}) {
  return makeItemDetail("movie", {
    title: "Example Movie",
    overview: "Movie overview",
    runtime: 120,
    content_rating: "PG-13",
    rating_imdb: 8.2,
    series_id: "",
    series_title: "",
    season_number: null,
    episode_number: null,
    air_date: null,
    versions: [movieVersion()],
    ...overrides,
  });
}

function renderMovie(item = makeMovieItem()) {
  renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={item} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
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
    mocks.useRedetectItemMarkers.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useAdminMarkerCapabilities.mockReturnValue({ data: undefined });
    mocks.useLibraryCapabilities.mockReturnValue({ data: undefined });
    mocks.useWatchedStateMutation.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useRating.mockReturnValue({ data: { rating: 4, rated_at: "2026-03-22T00:00:00Z" } });
    mocks.useSetRating.mockReturnValue({ mutate: vi.fn() });
    mocks.useDeleteRating.mockReturnValue({ mutate: vi.fn() });
    mocks.useSimilarItems.mockReturnValue({ data: { items: [] }, isLoading: false });
    mocks.useAuth.mockReturnValue({ user: null });
    mocks.useCurrentProfile.mockReturnValue({ profile: null });
  });

  // A pick is pinned at start (S3); the default version may still be swapped.
  it("updates hero metadata and marks the version picked when the viewer selects one", () => {
    const standard = movieVersion({
      file_id: 1,
      resolution: "2160p",
      codec_video: "hevc",
      codec_audio: "eac3",
      hdr: true,
      duration: 9780,
    });
    const directorsCut = movieVersion({
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
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={["/item/movie-1"]}>
          <MovieContent
            item={makeMovieItem({ runtime: 163, versions: [standard, directorsCut] })}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(mocks.capturedMetadataBadgesProps.value).toMatchObject({ duration: "2h 43m" });
    expect(mocks.capturedActionBarProps.value).toMatchObject({ versionPicked: false });
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
    expect(mocks.capturedActionBarProps.value).toMatchObject({ versionPicked: true });
    expect(mocks.capturedQualityBadgesProps.value).toEqual({
      summary: {
        durationMinutes: 196,
        resolution: "1080p",
        videoRangeLabel: "",
        audioLabel: "DTS",
      },
    });
  });

  it("opens on the remembered version when the movie has no resume point", () => {
    const uhd = movieVersion({ file_id: 1, resolution: "2160p", codec_audio: "truehd", hdr: true });
    const hd = movieVersion({ file_id: 2, resolution: "1080p", codec_audio: "dts", hdr: false });

    // The server omits a zero position, so the remembered version arrives on
    // its own beside the counts every user_data carries.
    renderMovie(
      makeMovieItem({
        versions: [uhd, hd],
        user_data: {
          played: false,
          watched_count: 0,
          unplayed_count: 0,
          in_progress_count: 0,
          last_file_id: 2,
        } as ItemDetail["user_data"],
      }),
    );

    expect(mocks.capturedQualityBadgesProps.value).toMatchObject({
      summary: { resolution: "1080p" },
    });
  });

  it.each([
    [
      "passes restartHref when the movie is partially watched",
      { played: false, is_in_progress: true, position_seconds: 600, duration_seconds: 3600 },
      { playLabel: "Resume", restartHref: "/watch/movie-1?restart=1" },
    ],
    [
      // Completed rows store position 0 (no resume point).
      "does not pass restartHref when the movie is already completed",
      { played: true, position_seconds: 0, duration_seconds: 3600 },
      { playLabel: "Play", restartHref: undefined },
    ],
    [
      // Rewatch in flight: watched latch stays, live resume point.
      "offers resume and restart during a rewatch of a completed movie",
      { played: true, position_seconds: 600, duration_seconds: 3600 },
      { playLabel: "Resume", restartHref: "/watch/movie-1?restart=1" },
    ],
  ])("%s", (_name, user_data, expected) => {
    renderMovie(makeMovieItem({ user_data }));

    expect(mocks.capturedActionBarProps.value).toMatchObject(expected);
  });

  it("allows marker editing without metadata curation permission", () => {
    mocks.useAuth.mockReturnValue({
      user: { role: "user", permissions: ["marker_edit"], download_allowed: true },
    });

    renderMovie();

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      canCurateMetadata: false,
      canEditMarkers: true,
    });
  });

  it.each([
    [{ role: "admin" }, { data: { trickplay: true, trickplay_supported: true } }, true],
    [{ role: "admin" }, { data: { trickplay: true, trickplay_supported: false } }, false],
  ])("offers seek-preview status to %o with capability %o: %s", (user, capability, offered) => {
    mocks.useAuth.mockReturnValue({ user });
    mocks.useLibraryCapabilities.mockReturnValue(capability);

    renderMovie();

    expect(mocks.capturedActionBarProps.value?.canManageTrickplay).toBe(offered);
    expect(mocks.useLibraryCapabilities).toHaveBeenLastCalledWith(true);
  });

  it("passes credits re-detection only for admins", () => {
    const redetect = vi.fn();
    mocks.useAuth.mockReturnValue({ user: { role: "admin" } });
    mocks.useRedetectItemMarkers.mockReturnValue({ mutate: redetect, isPending: false });
    mocks.useAdminMarkerCapabilities.mockReturnValue({
      data: { redetect_markers: true, movie_credits: true },
    });

    renderMovie();

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      isAdmin: true,
      redetectKind: "credits",
      isRedetectingMarkers: false,
    });
    const onRedetectMarkers = mocks.capturedActionBarProps.value?.onRedetectMarkers;
    expect(typeof onRedetectMarkers).toBe("function");
    (onRedetectMarkers as (kind: string) => void)("credits");
    expect(redetect).toHaveBeenCalledWith({ itemId: "movie-1", kind: "credits" });

    mocks.useAuth.mockReturnValue({ user: null });
    renderMovie();
    expect(mocks.capturedActionBarProps.value?.onRedetectMarkers).toBeUndefined();
    expect(mocks.useLibraryCapabilities).toHaveBeenLastCalledWith(false);
  });

  it.each([
    ["a node without redetect_markers", { data: { movie_credits: true } }],
    ["a node without movie_credits", { data: { redetect_markers: true } }],
  ])("offers no credits re-detection for %s", (_label, capability) => {
    mocks.useAuth.mockReturnValue({ user: { role: "admin" } });
    mocks.useAdminMarkerCapabilities.mockReturnValue(capability);

    renderMovie();

    expect(mocks.capturedActionBarProps.value?.isAdmin).toBe(true);
    expect(mocks.capturedActionBarProps.value?.onRedetectMarkers).toBeUndefined();
  });
});
