import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Opens the real series page (ItemDetail -> SeriesContent) with every data
// hook live and only the v2 request boundary mocked, then counts the requests
// it makes until the page is idle. App-shell hooks (auth, profile, AI status)
// and presentational children are stubbed: they are shared across pages or
// make no requests on mount.

const mocks = vi.hoisted(() => ({
  v2: vi.fn(),
  actionBarProps: { value: null as Record<string, unknown> | null },
  /** Each distinct play button the page showed, in order. */
  playButtonStates: [] as string[],
  watchTogether: { value: null as Record<string, unknown> | null },
}));

vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: (...args: unknown[]) => mocks.v2(...args),
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ user: null, profile: null }),
  useOptionalAuth: () => ({ user: null, profile: null }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: null, hasSelectedProfile: false, isLoading: false }),
}));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => false }));
vi.mock("@/hooks/useOnViewTranslation", () => ({
  useOnViewTranslation: () => ({ translating: false, onTranslate: undefined }),
}));
vi.mock("@/pages/watchtogether/DetailWatchTogether", () => ({
  useDetailWatchTogether: (args: Record<string, unknown>) => {
    mocks.watchTogether.value = args;
    return { menu: undefined, sheet: null };
  },
}));
vi.mock("@/playback/watchPlaybackContext", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/playback/watchPlaybackContext")>()),
  useWatchPlaybackController: () => ({ startPlayback: () => {} }),
}));
vi.mock("./components/ActionBar", () => ({
  default: (props: Record<string, unknown>) => {
    mocks.actionBarProps.value = props;
    const state = `${String(props.playLabel)} ${String(props.playHref ?? "(no link)")}`;
    if (mocks.playButtonStates.at(-1) !== state) mocks.playButtonStates.push(state);
    return <div />;
  },
}));
vi.mock("./DetailHero", () => ({
  default: ({ actions }: { actions?: ReactNode }) => <div>{actions}</div>,
}));
vi.mock("./SeasonCarousel", () => ({ default: () => <div /> }));
vi.mock("./components/SeasonEpisodeGrid", () => ({ default: () => <div /> }));

import ItemDetailPage from "./index";

type WatchState = "watched" | "in_progress" | "unwatched";

interface Fixture {
  /** Episode watch state per season number, in episode order. */
  seasons: Record<number, WatchState[]>;
  /** What the server's play-target resolver answers for the series. */
  seriesPlayContentId: string;
  /** Media item IDs on the first page of GET /api/v2/progress. */
  progressPage: string[];
}

const SERIES_ID = "series-1";

function episodeId(season: number, episode: number) {
  return `s${season}e${episode}`;
}

function rollup(states: WatchState[]) {
  const watched = states.filter((state) => state === "watched").length;
  return {
    played: watched === states.length,
    watched_count: watched,
    unplayed_count: states.length - watched,
    in_progress_count: states.filter((state) => state === "in_progress").length,
  };
}

// The per-season target follows the same server rule as the series target:
// the in-progress episode, else the first unwatched, else the first episode.
function seasonPlayTarget(season: number, states: WatchState[]) {
  const index = [states.indexOf("in_progress"), states.indexOf("unwatched"), 0].find(
    (candidate) => candidate >= 0,
  )!;
  return episodeId(season, index + 1);
}

function detailV2(overrides: Record<string, unknown>) {
  return {
    genres: [],
    cast: [],
    crew: [],
    versions: [],
    subtitles: [],
    ...overrides,
  };
}

function installServer(fixture: Fixture) {
  const seasonNumbers = Object.keys(fixture.seasons).map(Number);
  const allStates = seasonNumbers.flatMap((season) => fixture.seasons[season]!);
  const episodes = (season: number) =>
    fixture.seasons[season]!.map((state, index) => ({
      content_id: episodeId(season, index + 1),
      season_number: season,
      episode_number: index + 1,
      title: `Episode ${season}.${index + 1}`,
      runtime: 40,
      files: [{ file_id: String(season * 100 + index), audio_channels: 2 }],
      user_data: {
        played: state === "watched",
        is_in_progress: state === "in_progress",
        position_seconds: state === "in_progress" ? 600 : 0,
        duration_seconds: 2400,
      },
    }));

  mocks.v2.mockImplementation(async (operation: string, init?: { path?: { id?: string } }) => {
    const id = init?.path?.id;
    switch (operation) {
      case "GET /api/v2/catalog/items/{id}": {
        if (id === SERIES_ID) {
          return detailV2({
            content_id: SERIES_ID,
            type: "series",
            title: "Example Series",
            play_content_id: fixture.seriesPlayContentId,
            season_count: seasonNumbers.length,
            user_data: rollup(allStates),
          });
        }
        // Continue-watching entries: the owning series decides a match.
        const [, season, episode] = /^s(\d+)e(\d+)$/.exec(id ?? "") ?? [];
        return detailV2({
          content_id: id,
          type: "episode",
          title: `Episode ${season}.${episode}`,
          series_id: season ? SERIES_ID : `series-of-${id}`,
          season_number: Number(season ?? 1),
          episode_number: Number(episode ?? 1),
        });
      }
      case "GET /api/v2/catalog/series/{id}/seasons":
        return {
          items: seasonNumbers.map((season) => ({
            content_id: `season-${season}`,
            play_content_id: seasonPlayTarget(season, fixture.seasons[season]!),
            season_number: season,
            title: `Season ${season}`,
            episode_count: fixture.seasons[season]!.length,
            user_data: rollup(fixture.seasons[season]!),
          })),
        };
      case "GET /api/v2/catalog/items/{id}/episodes":
        return { items: episodes(Number(id?.replace("season-", ""))) };
      case "GET /api/v2/progress":
        return {
          items: fixture.progressPage.map((mediaItemId) => ({
            media_item_id: mediaItemId,
            position_seconds: 600,
            duration_seconds: 2400,
            completed: false,
          })),
        };
      case "GET /api/v2/recommendations/similar/{item_id}":
        return { items: [] };
      case "GET /api/v2/settings/values/effective":
        return {
          items: [
            { key: "ui.theme_music_enabled", value: false, source: "default" },
            { key: "ui.theme_music_loop", value: false, source: "default" },
          ],
        };
      default:
        throw new Error(`unexpected request ${operation}`);
    }
  });
}

/** Every request the page made, as `METHOD /path id`. */
function requestLog(): string[] {
  return mocks.v2.mock.calls.map(([operation, init]) => {
    const path = (init as { path?: Record<string, string> } | undefined)?.path;
    const id = path ? Object.values(path).join("/") : "";
    return id ? `${operation} ${id}` : String(operation);
  });
}

async function openSeriesPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/item/${SERIES_ID}`]}>
        <Routes>
          <Route path="/item/:id" element={<ItemDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  // Idle means no query in flight and no new request for several turns, so
  // requests that only start once an earlier response lands are counted too.
  let quietTurns = 0;
  let lastCount = -1;
  for (let turn = 0; turn < 100 && quietTurns < 3; turn++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    const count = mocks.v2.mock.calls.length;
    quietTurns = client.isFetching() === 0 && count === lastCount ? quietTurns + 1 : 0;
    lastCount = count;
  }
  expect(quietTurns).toBe(3);
}

function playButton() {
  const props = mocks.actionBarProps.value;
  return { href: props?.playHref, label: props?.playLabel };
}

const twoSeasonsInProgress: Fixture["seasons"] = {
  1: ["watched", "watched", "watched"],
  2: ["watched", "in_progress", "unwatched"],
};

describe("series page request budget and play target", () => {
  beforeEach(() => {
    mocks.v2.mockReset();
    mocks.actionBarProps.value = null;
    mocks.playButtonStates.length = 0;
    mocks.watchTogether.value = null;
  });

  afterEach(() => {
    cleanup();
  });

  it("resumes the in-progress episode", async () => {
    installServer({
      seasons: twoSeasonsInProgress,
      seriesPlayContentId: "s2e2",
      progressPage: ["s2e2"],
    });

    await openSeriesPage();

    expect(playButton()).toEqual({ href: "/watch/s2e2", label: "Resume" });
    // The series detail alone decides the button, so it never changes after
    // the first render.
    expect(mocks.playButtonStates).toEqual(["Resume /watch/s2e2"]);
    expect(mocks.watchTogether.value).toMatchObject({
      target: { content_id: "s2e2", title: "Episode 2.2", subtitle: "S2 E2" },
      initialSeasonNumber: 2,
    });
    expect(requestLog()).toEqual([
      `GET /api/v2/catalog/items/{id} ${SERIES_ID}`,
      "GET /api/v2/settings/values/effective",
      `GET /api/v2/catalog/series/{id}/seasons ${SERIES_ID}`,
      `GET /api/v2/recommendations/similar/{item_id} ${SERIES_ID}`,
      "GET /api/v2/catalog/items/{id}/episodes season-2",
    ]);
  });

  it("resumes when more than 20 other titles are in progress", async () => {
    // The first progress page is full of other shows, so this series' resume
    // point is not on it. The server's play target still finds it.
    installServer({
      seasons: twoSeasonsInProgress,
      seriesPlayContentId: "s2e2",
      progressPage: Array.from({ length: 20 }, (_, index) => `other-${index + 1}`),
    });

    await openSeriesPage();

    expect(playButton()).toEqual({ href: "/watch/s2e2", label: "Resume" });
    // Detail, theme settings, seasons and similar titles, plus the target
    // season's episodes for Watch Together. Nothing scales with the profile's history.
    expect(requestLog()).toHaveLength(5);
    expect(requestLog().filter((request) => request.startsWith("GET /api/v2/progress"))).toEqual(
      [],
    );
  });

  it("starts again from episode 1 once the whole series is watched", async () => {
    installServer({
      seasons: {
        1: ["watched", "watched", "watched"],
        2: ["watched", "watched", "watched"],
      },
      seriesPlayContentId: "s1e1",
      progressPage: [],
    });

    await openSeriesPage();

    expect(playButton()).toEqual({
      href: "/watch/s1e1",
      label: "Start From Episode 1",
    });
    expect(requestLog()).toHaveLength(5);
  });

  it("plays the first unwatched episode after a finished one", async () => {
    installServer({
      seasons: {
        1: ["watched", "watched", "watched"],
        2: ["unwatched", "unwatched", "unwatched"],
      },
      seriesPlayContentId: "s2e1",
      progressPage: [],
    });

    await openSeriesPage();

    expect(playButton()).toEqual({ href: "/watch/s2e1", label: "Play Next" });
    expect(mocks.watchTogether.value).toMatchObject({
      target: { content_id: "s2e1", subtitle: "S2 E1" },
      initialSeasonNumber: 2,
    });
    expect(requestLog()).toHaveLength(5);
  });

  it("starts an unstarted series from episode 1", async () => {
    installServer({
      seasons: {
        1: ["unwatched", "unwatched", "unwatched"],
        2: ["unwatched", "unwatched", "unwatched"],
      },
      seriesPlayContentId: "s1e1",
      progressPage: [],
    });

    await openSeriesPage();

    expect(playButton()).toEqual({
      href: "/watch/s1e1",
      label: "Start From Episode 1",
    });
    expect(requestLog()).toHaveLength(5);
  });

  it("plays the specials once every regular episode is watched", async () => {
    // The series rollup counts specials, so the series is not fully watched,
    // and the server ranks unwatched specials after every regular episode.
    installServer({
      seasons: {
        0: ["unwatched", "unwatched"],
        1: ["watched", "watched", "watched"],
        2: ["watched", "watched", "watched"],
      },
      seriesPlayContentId: "s0e1",
      progressPage: [],
    });

    await openSeriesPage();

    expect(playButton()).toEqual({ href: "/watch/s0e1", label: "Play Next" });
    expect(mocks.watchTogether.value).toMatchObject({
      target: { content_id: "s0e1", subtitle: "S0 E1" },
      initialSeasonNumber: 0,
    });
    expect(requestLog()).toHaveLength(5);
  });

  it("starts an unstarted series at season 1, not the specials", async () => {
    installServer({
      seasons: {
        0: ["unwatched", "unwatched"],
        1: ["unwatched", "unwatched", "unwatched"],
        2: ["unwatched", "unwatched", "unwatched"],
      },
      seriesPlayContentId: "s1e1",
      progressPage: [],
    });

    await openSeriesPage();

    expect(playButton()).toEqual({ href: "/watch/s1e1", label: "Start From Episode 1" });
    expect(mocks.watchTogether.value).toMatchObject({
      target: { content_id: "s1e1", subtitle: "S1 E1" },
      initialSeasonNumber: 1,
    });
    expect(requestLog()).toHaveLength(5);
  });

  it("shares the single season's episode list with the episode grid", async () => {
    installServer({
      seasons: { 1: ["watched", "in_progress", "unwatched"] },
      seriesPlayContentId: "s1e2",
      progressPage: Array.from({ length: 20 }, (_, index) => `other-${index + 1}`),
    });

    await openSeriesPage();

    expect(playButton()).toEqual({ href: "/watch/s1e2", label: "Resume" });
    expect(requestLog()).toEqual([
      `GET /api/v2/catalog/items/{id} ${SERIES_ID}`,
      "GET /api/v2/settings/values/effective",
      `GET /api/v2/catalog/series/{id}/seasons ${SERIES_ID}`,
      `GET /api/v2/recommendations/similar/{item_id} ${SERIES_ID}`,
      "GET /api/v2/catalog/items/{id}/episodes season-1",
    ]);
  });
});
