import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import ContinueWatchingCard from "./ContinueWatchingCard";

const capturedMenuProps: Record<string, unknown>[] = [];

const startPlayback = () => {};

vi.mock("@/components/MediaItemMenu", () => ({
  default: (props: Record<string, unknown>) => {
    capturedMenuProps.push(props);
    return <div />;
  },
}));

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback }),
}));

describe("ContinueWatchingCard restart eligibility", () => {
  it("passes partial-progress restart eligibility to the media menu", () => {
    capturedMenuProps.length = 0;
    const queryClient = new QueryClient();

    renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ContinueWatchingCard
            sectionItem={{
              content_id: "ep-001",
              type: "episode",
              title: "Pilot",
              series_id: "series-1",
              series_title: "Breaking Bad",
              season_number: 1,
              episode_number: 1,
              year: 2008,
              genres: [],
              status: "matched",
              rating_imdb: 9.1,
              overview: "Episode overview",
              item_source: "continue_watching",
              position_seconds: 120,
              duration_seconds: 3600,
              progress_updated_at: "2026-03-07T00:00:00Z",
              poster_url: "/episode-poster.jpg",
              poster_thumbhash: "",
              backdrop_url: "/episode-backdrop.jpg",
              backdrop_thumbhash: "",
              logo_url: "",
            }}
            quickActionMode="both"
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(capturedMenuProps[0]).toMatchObject({
      contentId: "ep-001",
      mediaType: "episode",
      hasPartialProgress: true,
    });
  });
});
