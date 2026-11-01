import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Season } from "@/api/types";
import { buildDefaultPrefs, type CardOverlayPrefs, type OverlayId } from "@/lib/overlays";
import SeasonCarousel from "./SeasonCarousel";

const overlay = vi.hoisted(() => ({ prefs: null as CardOverlayPrefs | null }));
vi.mock("@/hooks/useOverlayPrefs", () => ({ useOverlayPrefs: () => ({ prefs: overlay.prefs }) }));

vi.mock("@/components/CardPlayOverlay", () => ({
  default: ({ contentId, title }: { contentId: string; title: string }) => (
    <a href={`/watch/${contentId}`} aria-label={`Play ${title}`} />
  ),
}));

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

function renderSeasons(seasons: Season[]) {
  return renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <SeasonCarousel seasons={seasons} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("SeasonCarousel", () => {
  beforeEach(() => {
    overlay.prefs = null;
  });

  it("renders independent season detail and direct-play targets", () => {
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <SeasonCarousel seasons={[makeSeason({ play_content_id: "episode-2" })]} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('href="/item/season-1"');
    expect(markup).toContain('href="/watch/episode-2"');
    expect(markup).toContain('aria-label="Play Season 1"');
  });

  it("shows the unwatched-episodes badge from each season's counts, and none once finished", () => {
    const prefs = buildDefaultPrefs();
    for (const key of Object.keys(prefs.items) as OverlayId[]) {
      prefs.items[key] = { ...prefs.items[key], enabled: key === "unwatched_episodes" };
    }
    overlay.prefs = prefs;
    const markup = renderSeasons([
      makeSeason({
        user_data: { played: false, watched_count: 5, unplayed_count: 3, in_progress_count: 1 },
      }),
      makeSeason({
        content_id: "season-2",
        season_number: 2,
        title: "Season 2",
        user_data: { played: true, watched_count: 8, unplayed_count: 0, in_progress_count: 0 },
      }),
    ]);

    expect(markup.match(/data-overlay-badge/g)).toHaveLength(1);
    expect(markup).toContain(">3</span>");
  });
});
