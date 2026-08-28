import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import SeasonEpisodeGrid from "./SeasonEpisodeGrid";

const capturedMenuProps: Record<string, unknown>[] = [];
const prefetchDetail = vi.hoisted(() => vi.fn());

vi.mock("@/components/MediaItemMenu", () => ({
  default: (props: Record<string, unknown>) => {
    capturedMenuProps.push(props);
    return null;
  },
}));

vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ prefs: null, quickActionMode: "watched" }),
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  usePrefetchCatalogItemDetail: () => prefetchDetail,
}));

vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({
    emblaRef: vi.fn(),
    canScrollPrev: false,
    canScrollNext: false,
    scrollPrev: vi.fn(),
    scrollNext: vi.fn(),
  }),
}));

describe("SeasonEpisodeGrid", () => {
  beforeEach(() => {
    capturedMenuProps.length = 0;
    prefetchDetail.mockClear();
  });

  it("enables the watched shortcut on episode cards", () => {
    render(
      <MemoryRouter>
        <SeasonEpisodeGrid
          isLoading={false}
          episodes={[
            {
              content_id: "ep-1",
              season_number: 1,
              episode_number: 1,
              title: "Pilot",
              overview: "A beginning.",
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
  });

  it("places the watched circle-check beside the episode label instead of over the artwork", () => {
    render(
      <MemoryRouter>
        <SeasonEpisodeGrid
          isLoading={false}
          episodes={[
            {
              content_id: "ep-1",
              season_number: 1,
              episode_number: 1,
              title: "Pilot",
              overview: "A beginning.",
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
              overview: "Another episode.",
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
    expect(watchedIndicator.closest(".media-card-image")).toBeNull();
    expect(screen.getByText("Episode 2").parentElement).not.toContainElement(watchedIndicator);
    expect(screen.getAllByLabelText("Watched")).toHaveLength(1);
  });

  it("renders the season in the shared horizontal media carousel", () => {
    render(
      <MemoryRouter>
        <SeasonEpisodeGrid
          isLoading={false}
          episodes={[
            {
              content_id: "ep-1",
              season_number: 1,
              episode_number: 1,
              title: "Pilot",
              overview: "A beginning.",
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
              overview: "Another episode.",
              air_date: null,
              runtime: 43,
              still_url: "",
              still_thumbhash: "",
              files: [],
            },
          ]}
        />
      </MemoryRouter>,
    );

    const viewport = screen.getByLabelText("Media carousel");
    expect(viewport).toHaveClass("embla__viewport");
    expect(viewport.querySelectorAll(".embla__slide")).toHaveLength(2);
    expect(viewport.querySelector(".grid")).toBeNull();
    expect(viewport.querySelector(".embla__container")).not.toHaveClass("cursor-grab");
    expect(viewport.querySelectorAll(".season-episode-card")).toHaveLength(2);
  });

  it("does not prefetch episodes during a fast pointer sweep", () => {
    vi.useFakeTimers();
    try {
      render(
        <MemoryRouter>
          <SeasonEpisodeGrid
            isLoading={false}
            episodes={[
              {
                content_id: "ep-1",
                season_number: 1,
                episode_number: 1,
                title: "Pilot",
                overview: "A beginning.",
                air_date: null,
                runtime: 42,
                still_url: "/pilot.jpg",
                still_thumbhash: "",
                files: [],
              },
            ]}
          />
        </MemoryRouter>,
      );

      const card = screen.getByText("Episode 1").closest(".media-card");
      expect(card).not.toBeNull();
      fireEvent.mouseEnter(card!);
      act(() => vi.advanceTimersByTime(100));
      fireEvent.mouseLeave(card!);
      act(() => vi.advanceTimersByTime(100));
      expect(prefetchDetail).not.toHaveBeenCalled();

      fireEvent.mouseEnter(card!);
      act(() => vi.advanceTimersByTime(140));
      expect(prefetchDetail).toHaveBeenCalledOnce();
      expect(prefetchDetail).toHaveBeenCalledWith("ep-1");
    } finally {
      vi.useRealTimers();
    }
  });
});
