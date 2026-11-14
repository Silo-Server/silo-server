import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { act, render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { FileVersion, ItemDetail } from "@/api/types";
import { readRequestedFileId, rememberRequestedFileId } from "@/playback/requestedVersion";
import MovieContent from "./MovieContent";

// The page renders query hooks this file does not mock (the household
// allowance check on the action bar), so every render carries a provider.
const renderInQueryClient = (ui: ReactNode) =>
  render(<QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>);

const renderMarkupInQueryClient = (ui: ReactNode) =>
  renderToStaticMarkup(<QueryClientProvider client={new QueryClient()}>{ui}</QueryClientProvider>);

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
    useRedetectItemMarkers: vi.fn(),
    useItemFiles: vi.fn(),
    useSearchItemMatchCandidates: vi.fn(),
    useSplitItem: vi.fn(),
    useAdminMarkerCapabilities: vi.fn(),
    useLibraryCapabilities: vi.fn(),
    useWatchedStateMutation: vi.fn(),
    useRating: vi.fn(),
    useSetRating: vi.fn(),
    useDeleteRating: vi.fn(),
    useSimilarItems: vi.fn(),
    useAuth: vi.fn(),
    useCurrentProfile: vi.fn(),
    useVersionLiveness: vi.fn(),
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

// The requested-version store is profile-scoped; these tests render with the
// account's resolved profile.
const PROFILE_ID = "profile-test";

const resetProgressMocks = vi.hoisted(() => ({
  useResetProgressAction: vi.fn((): (() => void) | undefined => undefined),
}));

vi.mock("@/hooks/queries/progress", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/progress")>()),
  useResetProgressAction: resetProgressMocks.useResetProgressAction,
}));

vi.mock("@/hooks/queries/items", () => ({
  useRefreshItemMetadata: mocks.useRefreshItemMetadata,
  useRedetectItemMarkers: mocks.useRedetectItemMarkers,
  useItemFiles: mocks.useItemFiles,
  useSearchItemMatchCandidates: mocks.useSearchItemMatchCandidates,
  useSplitItem: mocks.useSplitItem,
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
  // The resolution cap comes from the settings contract; the hook stands in
  // for the resolved answer so the render stays offline and deterministic.
  useQualityPreference: (fallback?: string | null) => fallback ?? null,
}));

vi.mock("@/hooks/queries/versionLiveness", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/versionLiveness")>()),
  // The liveness check is a no-op that leaves item metadata untouched.
  useVersionLiveness: (...args: unknown[]) => mocks.useVersionLiveness(...args),
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
    localStorage.clear();
    mocks.capturedActionBarProps.value = null;
    mocks.capturedMetadataBadgesProps.value = null;
    mocks.capturedQualityBadgesProps.value = null;
    mocks.useIsFavorite.mockReturnValue({ data: false });
    mocks.useToggleFavorite.mockReturnValue({ mutate: vi.fn() });
    mocks.useIsInWatchlist.mockReturnValue({ data: false });
    mocks.useToggleWatchlist.mockReturnValue({ mutate: vi.fn() });
    mocks.useRefreshItemMetadata.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useRedetectItemMarkers.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useItemFiles.mockReturnValue({ data: undefined, isLoading: false });
    mocks.useSearchItemMatchCandidates.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      isSuccess: false,
      data: undefined,
    });
    mocks.useSplitItem.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useAdminMarkerCapabilities.mockReturnValue({ data: undefined });
    mocks.useLibraryCapabilities.mockReturnValue({ data: undefined });
    mocks.useWatchedStateMutation.mockReturnValue({ mutate: vi.fn(), isPending: false });
    mocks.useRating.mockReturnValue({ data: { rating: 4, rated_at: "2026-03-22T00:00:00Z" } });
    mocks.useSetRating.mockReturnValue({ mutate: vi.fn() });
    mocks.useDeleteRating.mockReturnValue({ mutate: vi.fn() });
    mocks.useSimilarItems.mockReturnValue({ data: { items: [] }, isLoading: false });
    mocks.useAuth.mockReturnValue({ user: null });
    mocks.useCurrentProfile.mockReturnValue({ profile: null });
    mocks.useVersionLiveness.mockClear();
    mocks.useVersionLiveness.mockReturnValue(new Map<number, boolean>());
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

    renderInQueryClient(
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

  // Exiting a substituted playback writes the requested row to the device
  // store; a fresh server-backed detail still names the effective candidate in
  // `last_file_id` (progress and history record the row that served the bytes).
  it("keeps the viewer's requested row selected over a fresh last-played substitute", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: PROFILE_ID } });
    const substitute = makeFileVersion({ file_id: 1, resolution: "2160p" });
    const requested = makeFileVersion({ file_id: 2, resolution: "1080p" });
    rememberRequestedFileId("movie-1", requested.file_id, PROFILE_ID);

    renderInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent
          item={makeMovieItem({
            versions: [substitute, requested],
            user_data: {
              played: false,
              position_seconds: 600,
              duration_seconds: 7200,
              last_file_id: substitute.file_id,
            },
          })}
        />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      selectedVersion: expect.objectContaining({ file_id: requested.file_id }),
      selectedVersionMissing: false,
    });
  });

  it("remembers an explicit pick as the requested row", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: PROFILE_ID } });
    const first = makeFileVersion({ file_id: 1 });
    const second = makeFileVersion({ file_id: 2, resolution: "1080p" });

    renderInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem({ versions: [first, second] })} />
      </MemoryRouter>,
    );

    act(() => {
      const selectVersion = mocks.capturedActionBarProps.value?.onSelectVersion as
        | ((version: FileVersion) => void)
        | undefined;
      selectVersion?.(second);
    });

    expect(readRequestedFileId("movie-1", PROFILE_ID)).toBe(second.file_id);
    expect(mocks.capturedActionBarProps.value).toMatchObject({
      selectedVersion: expect.objectContaining({ file_id: second.file_id }),
    });
  });

  // A reload keeps the row but loses the click that chose it. Without the
  // remembered standing, Play sends the row as an Auto start and the server is
  // free to start a different file than the one the bar shows.
  it("plays a remembered explicit pick as an explicit selection", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: PROFILE_ID } });
    const other = makeFileVersion({ file_id: 1 });
    const picked = makeFileVersion({ file_id: 2, resolution: "1080p" });
    rememberRequestedFileId("movie-1", picked.file_id, PROFILE_ID, { explicit: true });

    renderInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem({ versions: [other, picked] })} />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      selectedVersion: expect.objectContaining({ file_id: picked.file_id }),
      explicitFileSelection: true,
    });
  });

  // The store also carries the row an Auto start played, so that the picker
  // can follow it. That row is not a pick and must stay substitutable.
  it("keeps a remembered Auto row substitutable", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: PROFILE_ID } });
    const other = makeFileVersion({ file_id: 1 });
    const served = makeFileVersion({ file_id: 2, resolution: "1080p" });
    rememberRequestedFileId("movie-1", served.file_id, PROFILE_ID);

    renderInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem({ versions: [other, served] })} />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      selectedVersion: expect.objectContaining({ file_id: served.file_id }),
      explicitFileSelection: false,
    });
  });

  it("asks for a replacement pick when the requested row is gone", () => {
    mocks.useCurrentProfile.mockReturnValue({ profile: { id: PROFILE_ID } });
    rememberRequestedFileId("movie-1", 3, PROFILE_ID);

    renderInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent
          item={makeMovieItem({
            versions: [makeFileVersion({ file_id: 1 }), makeFileVersion({ file_id: 2 })],
          })}
        />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      selectedVersion: null,
      selectedVersionMissing: true,
    });
  });

  it("passes the reset progress action for the movie's resume position", () => {
    const resetProgress = vi.fn();
    resetProgressMocks.useResetProgressAction.mockImplementation(() => resetProgress);
    try {
      renderMarkupInQueryClient(
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
    } finally {
      resetProgressMocks.useResetProgressAction.mockImplementation(() => undefined);
    }

    expect(resetProgressMocks.useResetProgressAction).toHaveBeenCalledWith(
      expect.objectContaining({ content_id: "movie-1" }),
    );
    expect(mocks.capturedActionBarProps.value?.onResetProgress).toBe(resetProgress);
  });

  it("passes restartHref when the movie is partially watched", () => {
    renderMarkupInQueryClient(
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
    renderMarkupInQueryClient(
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
    renderMarkupInQueryClient(
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

    renderMarkupInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem()} />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      canCurateMetadata: false,
      canEditMarkers: true,
    });
  });

  it.each([
    [{ role: "admin" }, { data: { trickplay: true, trickplay_supported: true } }, true],
    [{ role: "admin" }, { data: { trickplay: true, trickplay_supported: false } }, false],
    [{ role: "admin" }, { data: { trickplay: true } }, false],
    [{ role: "admin" }, { data: { trickplay: false } }, false],
    [{ role: "admin" }, { data: undefined }, false],
  ])("offers seek-preview status to %o with capability %o: %s", (user, capability, offered) => {
    mocks.useAuth.mockReturnValue({ user });
    mocks.useLibraryCapabilities.mockReturnValue(capability);

    renderMarkupInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem()} />
      </MemoryRouter>,
    );

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

    renderMarkupInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem()} />
      </MemoryRouter>,
    );

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
    renderMarkupInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem()} />
      </MemoryRouter>,
    );
    expect(mocks.capturedActionBarProps.value?.onRedetectMarkers).toBeUndefined();
    expect(mocks.useLibraryCapabilities).toHaveBeenLastCalledWith(false);
  });

  it.each([
    ["a pending capability read", { data: undefined }],
    ["a failed capability read", { data: undefined, isError: true }],
    ["a node without redetect_markers", { data: { movie_credits: true } }],
    [
      "a node with redetect_markers off",
      { data: { redetect_markers: false, movie_credits: true } },
    ],
    ["a node without movie_credits", { data: { redetect_markers: true } }],
    ["a node with movie_credits off", { data: { redetect_markers: true, movie_credits: false } }],
  ])("offers no credits re-detection for %s", (_label, capability) => {
    mocks.useAuth.mockReturnValue({ user: { role: "admin" } });
    mocks.useAdminMarkerCapabilities.mockReturnValue(capability);

    renderMarkupInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem()} />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value?.isAdmin).toBe(true);
    expect(mocks.capturedActionBarProps.value?.onRedetectMarkers).toBeUndefined();
  });

  it("does not pass playHref or restartHref when the movie has no versions or play targets", () => {
    renderMarkupInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent
          item={makeMovieItem({
            versions: [],
            playback_variants: [],
            play_content_id: undefined,
          })}
        />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      playHref: undefined,
      restartHref: undefined,
    });
  });

  it("passes playHref when the movie has a play target but no local versions", () => {
    renderMarkupInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent
          item={makeMovieItem({
            versions: [],
            playback_variants: [],
            play_content_id: "virtual-target-1",
          })}
        />
      </MemoryRouter>,
    );

    expect(mocks.capturedActionBarProps.value).toMatchObject({
      playHref: "/watch/movie-1",
    });
  });

  it("checks liveness for the default-selected version before any picker opens", () => {
    const standard = makeFileVersion({ file_id: 1, resolution: "1080p" });
    const uhd = makeFileVersion({ file_id: 2, resolution: "2160p" });

    renderInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem({ versions: [standard, uhd] })} />
      </MemoryRouter>,
    );

    const firstCall = mocks.useVersionLiveness.mock.calls[0];
    expect(firstCall?.[0]).toEqual([expect.objectContaining({ file_id: 2 })]);
    expect(firstCall?.[1]).toBe(true);
  });

  it("renders a Collections row when the movie belongs to collections", () => {
    const markup = renderMarkupInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent
          item={makeMovieItem({
            collections: [
              {
                id: "oscar-winners",
                title: "Oscar Winners",
                poster_url: "/collections/oscar-winners.jpg",
                item_count: 12,
              },
            ],
          })}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("Oscar Winners");
    expect(markup).toContain("12 items");
    expect(markup).toContain("collection_id=oscar-winners");
  });

  it("renders no Collections row when the movie is in no collections", () => {
    const markup = renderMarkupInQueryClient(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <MovieContent item={makeMovieItem({ collections: [] })} />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("collection_id=");
    expect(markup).not.toContain("Collections");
  });
});
