import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { act, cleanup, render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi, beforeEach } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import type { FileVersion, ItemDetail, Season } from "@/api/types";
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
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={["/item/episode-1"]}>
        <EpisodeContent item={makeEpisodeItem()} />
      </MemoryRouter>
    </QueryClientProvider>,
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
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={["/item/episode-1"]}>
          <EpisodeContent item={makeEpisodeItem()} />
        </MemoryRouter>
      </QueryClientProvider>,
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
      <QueryClientProvider client={new QueryClient()}>
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
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(countOccurrences(markup, 'href="/item/season-99"')).toBe(1);
    expect(markup).toContain(">Season 99<");
  });

  it("hides an unwatched episode's overview until the viewer reveals it", () => {
    spoilerPrefs.hideOverviews = true;
    try {
      render(
        <QueryClientProvider client={new QueryClient()}>
          <MemoryRouter initialEntries={["/item/episode-1"]}>
            <EpisodeContent item={makeEpisodeItem({ user_data: { played: false } })} />
          </MemoryRouter>
        </QueryClientProvider>,
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

  describe("pre-play audio choice", () => {
    const tracks = [
      {
        language: "en",
        codec: "eac3",
        channels: 6,
        layout: "5.1",
        title: "English",
        default: true,
      },
      { language: "ja", codec: "aac", channels: 2, layout: "stereo", embedded_title: "Japanese" },
      { language: "und", codec: "ac3", channels: 2 },
    ];
    type AudioProps = {
      audioSelectionMode: string;
      explicitAudioTrackIndex: number | null;
      onSelectAudioTrack: (index: number) => void;
      onResetAudioSelection: () => void;
      onSelectVersion: (version: FileVersion) => void;
    };
    const actionBar = () => mocks.capturedActionBarProps.value as unknown as AudioProps;

    function stubWrites(respond: (call: number) => Promise<Response>) {
      const requests: { method: string; path: string; body: unknown }[] = [];
      const fetchMock = vi.fn<typeof fetch>(async (url, init) => {
        requests.push({
          method: init?.method ?? "GET",
          path: new URL(String(url), "http://silo.test").pathname,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        });
        return respond(requests.length);
      });
      vi.stubGlobal("fetch", fetchMock);
      return requests;
    }

    function renderWithTracks(versions = [makeFileVersion({ audio_tracks: tracks })]) {
      render(
        <QueryClientProvider
          client={new QueryClient({ defaultOptions: { mutations: { retry: false } } })}
        >
          <MemoryRouter initialEntries={["/item/episode-1"]}>
            <EpisodeContent item={makeEpisodeItem({ versions })} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    }

    beforeEach(() => {
      installPolicyStorageMocks();
      setAccessToken("account");
      setProfileId("profile-1");
    });
    afterEach(() => {
      cleanup();
      vi.unstubAllGlobals();
    });

    it("saves picks for the series and forgets them on Auto, in the order chosen", async () => {
      let releaseFirst!: () => void;
      const firstHeld = new Promise<void>((resolve) => (releaseFirst = resolve));
      const requests = stubWrites(async (call) => {
        if (call === 1) await firstHeld;
        return new Response(null, { status: 204 });
      });
      renderWithTracks();

      act(() => actionBar().onSelectAudioTrack(1));
      act(() => actionBar().onResetAudioSelection());
      act(() => actionBar().onSelectAudioTrack(2));
      await waitFor(() => expect(requests).toHaveLength(1));
      // Auto and the second pick wait for the first pick to land.
      await act(async () => {});
      expect(requests).toHaveLength(1);
      releaseFirst();
      await waitFor(() => expect(requests).toHaveLength(3));

      expect(requests).toEqual([
        {
          method: "PUT",
          path: "/api/v2/audio-prefs/series-1",
          body: {
            audio_track_index: 1,
            audio_language: "ja",
            track_signature: {
              language: "ja",
              embedded_title: "Japanese",
              codec: "aac",
              layout: "stereo",
              channels: 2,
            },
          },
        },
        { method: "DELETE", path: "/api/v2/audio-prefs/series-1", body: undefined },
        {
          method: "PUT",
          path: "/api/v2/audio-prefs/series-1",
          // An undetermined track sets no series language.
          body: {
            audio_track_index: 2,
            track_signature: { language: "und", codec: "ac3", channels: 2 },
          },
        },
      ]);
      expect(actionBar()).toMatchObject({
        audioSelectionMode: "explicit",
        explicitAudioTrackIndex: 2,
      });
    });

    it("puts the selector back when a pick is not saved", async () => {
      stubWrites(async () =>
        jsonResponse({ type: "about:blank", title: "Bad Request", status: 400 }, 400),
      );
      renderWithTracks();

      act(() => actionBar().onSelectAudioTrack(1));
      expect(actionBar()).toMatchObject({
        audioSelectionMode: "explicit",
        explicitAudioTrackIndex: 1,
      });
      await waitFor(() =>
        expect(actionBar()).toMatchObject({
          audioSelectionMode: "auto",
          explicitAudioTrackIndex: null,
        }),
      );
    });

    it("does not roll a failed pick back to another version's saved track", async () => {
      stubWrites(async (call) =>
        call === 1
          ? new Response(null, { status: 204 })
          : jsonResponse({ type: "about:blank", title: "Bad Request", status: 400 }, 400),
      );
      const first = makeFileVersion({ file_id: 1, audio_tracks: tracks });
      const second = makeFileVersion({ file_id: 2, audio_tracks: tracks });
      renderWithTracks([first, second]);

      act(() => actionBar().onSelectAudioTrack(1));
      await waitFor(() => expect(actionBar()).toMatchObject({ explicitAudioTrackIndex: 1 }));
      await new Promise((resolve) => setTimeout(resolve, 0));
      act(() => actionBar().onSelectVersion(second));
      act(() => actionBar().onSelectAudioTrack(0));
      await waitFor(() =>
        expect(actionBar()).toMatchObject({
          audioSelectionMode: "auto",
          explicitAudioTrackIndex: null,
        }),
      );
    });

    it("keeps a saved pick when a quicker later pick fails", async () => {
      stubWrites(async (call) =>
        call === 1
          ? new Response(null, { status: 204 })
          : jsonResponse({ type: "about:blank", title: "Bad Request", status: 400 }, 400),
      );
      renderWithTracks();

      act(() => actionBar().onSelectAudioTrack(1));
      act(() => actionBar().onSelectAudioTrack(0));
      await waitFor(() =>
        expect(actionBar()).toMatchObject({
          audioSelectionMode: "explicit",
          explicitAudioTrackIndex: 1,
        }),
      );
    });

    it("puts the selector back to the last saved choice when quick picks all fail", async () => {
      stubWrites(async () =>
        jsonResponse({ type: "about:blank", title: "Bad Request", status: 400 }, 400),
      );
      renderWithTracks();

      act(() => actionBar().onSelectAudioTrack(1));
      act(() => actionBar().onSelectAudioTrack(0));
      await waitFor(() =>
        expect(actionBar()).toMatchObject({
          audioSelectionMode: "auto",
          explicitAudioTrackIndex: null,
        }),
      );
    });
  });
});
