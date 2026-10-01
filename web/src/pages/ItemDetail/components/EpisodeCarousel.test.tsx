import { renderToStaticMarkup } from "react-dom/server";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import EpisodeCarousel from "./EpisodeCarousel";

const capturedMenuProps: Record<string, unknown>[] = [];
const prefetchEpisodeDetail = vi.hoisted(() => vi.fn());

vi.mock("@/hooks/queries/catalogRead", () => ({
  usePrefetchCatalogItemDetail: () => prefetchEpisodeDetail,
}));

vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ quickActionMode: "watched" }),
}));

vi.mock("@/components/MediaItemMenu", () => ({
  default: (props: Record<string, unknown>) => {
    capturedMenuProps.push(props);
    return <div />;
  },
}));

vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({
    emblaApi: null,
    emblaRef: { current: null },
    canScrollPrev: false,
    canScrollNext: false,
    scrollPrev: () => {},
    scrollNext: () => {},
  }),
}));

describe("EpisodeCarousel", () => {
  beforeEach(() => {
    capturedMenuProps.length = 0;
    prefetchEpisodeDetail.mockClear();
  });

  it("prefetches only after a card shows sustained navigation intent", () => {
    // A pointer sweeping the rail crosses every card; prefetching each one it
    // passes would start cache work for the whole season. Intent is a dwell.
    vi.useFakeTimers();
    try {
      render(
        <MemoryRouter>
          <EpisodeCarousel
            currentEpisodeNumber={2}
            episodes={[
              {
                content_id: "ep-1",
                season_number: 1,
                episode_number: 1,
                title: "Pilot",
                overview: "",
                air_date: null,
                runtime: 42,
                still_url: "",
                still_thumbhash: "",
                files: [],
              },
            ]}
          />
        </MemoryRouter>,
      );

      const card = screen.getAllByRole("link", { name: /Pilot/ })[0]!.closest("div.group\\/card")!;

      fireEvent.mouseEnter(card);
      act(() => vi.advanceTimersByTime(100));
      fireEvent.mouseLeave(card);
      act(() => vi.advanceTimersByTime(200));
      expect(prefetchEpisodeDetail).not.toHaveBeenCalled();

      fireEvent.mouseEnter(card);
      act(() => vi.advanceTimersByTime(140));
      expect(prefetchEpisodeDetail).toHaveBeenCalledWith("ep-1");
    } finally {
      vi.useRealTimers();
    }
  });

  it("passes partial-progress restart eligibility to episode menus", () => {
    renderToStaticMarkup(
      <MemoryRouter>
        <EpisodeCarousel
          currentEpisodeNumber={1}
          episodes={[
            {
              content_id: "ep-1",
              season_number: 1,
              episode_number: 1,
              title: "Pilot",
              overview: "",
              air_date: null,
              runtime: 42,
              still_url: "",
              still_thumbhash: "",
              files: [],
              user_data: {
                played: false,
                position_seconds: 120,
                duration_seconds: 1800,
              },
            },
          ]}
        />
      </MemoryRouter>,
    );

    expect(capturedMenuProps[0]).toMatchObject({
      contentId: "ep-1",
      mediaType: "episode",
      hasPartialProgress: true,
    });
  });

  it("places the watched circle-check beside the episode label instead of over the artwork", () => {
    render(
      <MemoryRouter>
        <EpisodeCarousel
          currentEpisodeNumber={2}
          episodes={[
            {
              content_id: "ep-1",
              season_number: 1,
              episode_number: 1,
              title: "Pilot",
              overview: "",
              air_date: null,
              runtime: 42,
              still_url: "",
              still_thumbhash: "",
              files: [],
              user_data: {
                played: true,
                position_seconds: 1800,
                duration_seconds: 1800,
              },
            },
            {
              content_id: "ep-2",
              season_number: 1,
              episode_number: 2,
              title: "Next",
              overview: "",
              air_date: null,
              runtime: 43,
              still_url: "",
              still_thumbhash: "",
              files: [],
              user_data: {
                played: false,
                position_seconds: 0,
                duration_seconds: 1800,
              },
            },
          ]}
        />
      </MemoryRouter>,
    );

    const episodeLabel = screen.getByText("Episode 1");
    const watchedIndicator = screen.getByLabelText("Watched");

    expect(episodeLabel.parentElement).toContainElement(watchedIndicator);
    expect(watchedIndicator).toHaveAttribute("data-watched-indicator", "icon-only");
    expect(watchedIndicator.querySelector(".lucide-circle-check")).toBeTruthy();
    expect(watchedIndicator.closest(".surface-panel-subtle")).toBeNull();
    expect(screen.getByText("Episode 2").parentElement).not.toContainElement(watchedIndicator);
    expect(screen.getAllByLabelText("Watched")).toHaveLength(1);
  });

  it("enables unwatched shortcuts without losing partial-progress restart eligibility", () => {
    renderToStaticMarkup(
      <MemoryRouter>
        <EpisodeCarousel
          currentEpisodeNumber={1}
          episodes={[
            {
              content_id: "ep-1",
              season_number: 1,
              episode_number: 1,
              title: "Pilot",
              overview: "",
              air_date: null,
              runtime: 42,
              still_url: "",
              still_thumbhash: "",
              files: [],
            },
            {
              content_id: "ep-2",
              season_number: 1,
              episode_number: 2,
              title: "Next",
              overview: "",
              air_date: null,
              runtime: 43,
              still_url: "",
              still_thumbhash: "",
              files: [],
              user_data: {
                played: false,
                position_seconds: 120,
                duration_seconds: 1800,
              },
            },
          ]}
        />
      </MemoryRouter>,
    );

    expect(capturedMenuProps[0]).toMatchObject({
      contentId: "ep-1",
      mediaType: "episode",
      userState: {
        played: false,
        is_favorite: false,
        in_watchlist: false,
      },
      showCollectionActions: false,
      showWatchedShortcut: true,
      hasPartialProgress: false,
      quickActionMode: "watched",
    });
    expect(capturedMenuProps[1]).toMatchObject({
      contentId: "ep-2",
      userState: {
        played: false,
        is_favorite: false,
        in_watchlist: false,
      },
      showWatchedShortcut: true,
      hasPartialProgress: true,
      quickActionMode: "watched",
    });
  });
});
