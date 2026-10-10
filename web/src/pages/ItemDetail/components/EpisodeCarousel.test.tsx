import { renderToStaticMarkup } from "react-dom/server";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import EpisodeCarousel from "./EpisodeCarousel";

vi.mock("@/lib/thumbhash", () => ({ decodeThumbhash: () => "data:image/png;base64,fixture" }));

const capturedMenuProps: Record<string, unknown>[] = [];
const prefetchEpisodeDetail = vi.hoisted(() => vi.fn());
const spoilerPrefs = vi.hoisted(() => ({ hideImages: false, hideOverviews: false }));

vi.mock("@/hooks/queries/catalogRead", () => ({
  usePrefetchCatalogItemDetail: () => prefetchEpisodeDetail,
}));

vi.mock("@/hooks/useEpisodeSpoilerPrefs", () => ({
  useEpisodeSpoilerPrefs: () => spoilerPrefs,
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

it("suppresses a protected still placeholder until the image loads", () => {
  spoilerPrefs.hideImages = true;
  const { container, rerender } = render(
    <MemoryRouter>
      <EpisodeCarousel
        currentEpisodeNumber={0}
        episodes={[
          {
            content_id: "ep-protected",
            season_number: 1,
            episode_number: 1,
            title: "Protected",
            overview: "",
            air_date: null,
            runtime: 42,
            still_url: "https://example.invalid/still.jpg",
            still_thumbhash: "fixture",
            files: [],
            still_is_episode_still: true,
          },
        ]}
      />
    </MemoryRouter>,
  );
  const image = container.querySelector("img")!;
  expect(image.parentElement!.style.backgroundImage).toBe("");
  expect(image.className).toContain("blur");
  spoilerPrefs.hideImages = false;
  rerender(
    <MemoryRouter>
      <EpisodeCarousel
        currentEpisodeNumber={0}
        episodes={[
          {
            content_id: "ep-protected",
            season_number: 1,
            episode_number: 1,
            title: "Protected",
            overview: "",
            air_date: null,
            runtime: 42,
            still_url: "https://example.invalid/still.jpg",
            still_thumbhash: "fixture",
            files: [],
            still_is_episode_still: true,
          },
        ]}
      />
    </MemoryRouter>,
  );
  expect(container.querySelector("img")!.parentElement!.style.backgroundImage).toContain(
    "data:image/png",
  );
});

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

  it("enables unwatched shortcuts without losing partial-progress restart eligibility", () => {
    renderToStaticMarkup(
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
      mediaType: "episode",
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
