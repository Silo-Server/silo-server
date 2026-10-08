// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { EpisodeFile, EpisodeListItem, Season } from "@/api/types";
import { useNextEpisode } from "./useNextEpisode";
import { useSeriesEpisodes } from "./useSeriesEpisodes";

const catalog = vi.hoisted(() => ({ seasons: new Map<number, EpisodeListItem[]>() }));

vi.mock("@/hooks/queries/catalogRead", () => ({
  fetchCatalogSeriesSeasons: async () => ({
    seasons: [...catalog.seasons.keys()].map(
      (season_number) => ({ season_number, is_specials: false }) as Season,
    ),
  }),
  fetchCatalogSeasonEpisodes: async (_seriesId: string, seasonNumber: number) => ({
    episodes: catalog.seasons.get(seasonNumber) ?? [],
  }),
}));

const file = { file_id: 1, resolution: "1080p" } as EpisodeFile;

/** Builds a season from per-episode playability, numbering episodes from 1. */
function season(seasonNumber: number, playable: boolean[]): void {
  catalog.seasons.set(
    seasonNumber,
    playable.map(
      (canPlay, index) =>
        ({
          content_id: `s${seasonNumber}e${index + 1}`,
          season_number: seasonNumber,
          episode_number: index + 1,
          title: `Episode ${index + 1}`,
          runtime: 1800,
          files: canPlay ? [file] : [],
        }) as EpisodeListItem,
    ),
  );
}

/** Renders the player's episode list and the next pick for one episode. */
async function playing(seasonNumber: number, episodeNumber: number) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client: queryClient }, children);
  const { result } = renderHook(
    () => {
      const { episodes, isLoading } = useSeriesEpisodes("series-1", seasonNumber);
      const context = {
        seriesId: "series-1",
        currentSeason: seasonNumber,
        currentEpisode: episodeNumber,
        episodes,
      };
      const { nextEpisode } = useNextEpisode(null, context, 0, vi.fn());
      return { episodes, isLoading, nextEpisode };
    },
    { wrapper },
  );
  await waitFor(() => {
    expect(result.current.isLoading).toBe(false);
    expect(result.current.episodes.length).toBeGreaterThan(0);
  });
  return result.current;
}

describe("useSeriesEpisodes", () => {
  beforeEach(() => catalog.seasons.clear());

  it("steps over an episode the viewer cannot play in the middle of a season", async () => {
    season(1, [true, true, false, true]);

    const { episodes, nextEpisode } = await playing(1, 2);

    // Previous from episode 4 is the entry before it in this list.
    expect(episodes.map((ep) => ep.contentId)).toEqual(["s1e1", "s1e2", "s1e4"]);
    expect(nextEpisode?.contentId).toBe("s1e4");
  });

  it("continues into the next season when the rest of this one cannot be played", async () => {
    season(1, [true, true, false]);
    season(2, [false, true]);

    const { nextEpisode } = await playing(1, 2);

    expect(nextEpisode?.contentId).toBe("s2e2");
  });

  it("offers no next episode when nothing playable follows", async () => {
    season(1, [true, true, false]);
    season(2, [false, false]);

    const { nextEpisode } = await playing(1, 2);

    expect(nextEpisode).toBeNull();
  });
});
