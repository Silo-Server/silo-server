import { renderToStaticMarkup } from "react-dom/server";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { EpisodeListItem } from "@/api/types";
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

function episode(number: number, overrides: Partial<EpisodeListItem> = {}): EpisodeListItem {
  return {
    content_id: `ep-${number}`,
    season_number: 1,
    episode_number: number,
    title: "",
    overview: "",
    air_date: null,
    runtime: 42,
    still_url: "",
    still_thumbhash: "",
    files: [],
    ...overrides,
  };
}

const carousel = (currentEpisodeNumber: number, episodes: EpisodeListItem[]) => (
  <MemoryRouter>
    <EpisodeCarousel currentEpisodeNumber={currentEpisodeNumber} episodes={episodes} />
  </MemoryRouter>
);

it("captions each card like the series grid and hides unwatched descriptions on request", () => {
  spoilerPrefs.hideOverviews = true;
  const card = (id: string, number: number, played: boolean) =>
    episode(number, {
      content_id: id,
      title: `Chapter ${number}`,
      overview: `Overview ${number}`,
      runtime: 52,
      user_data: { played },
    });
  try {
    render(carousel(1, [card("ep-1", 1, true), card("ep-2", 2, false)]));
    expect(screen.getByText("S01E01 · Chapter 1")).toBeInTheDocument();
    expect(screen.getAllByText("52m")).toHaveLength(2);
    expect(screen.getByText("Overview 1")).toBeInTheDocument();
    expect(screen.queryByText("Overview 2")).not.toBeInTheDocument();
  } finally {
    spoilerPrefs.hideOverviews = false;
  }
});

it("suppresses a protected still placeholder until the image loads", () => {
  spoilerPrefs.hideImages = true;
  const protectedStill = () => [
    episode(1, {
      content_id: "ep-protected",
      title: "Protected",
      still_url: "https://example.invalid/still.jpg",
      still_thumbhash: "fixture",
      still_is_episode_still: true,
    }),
  ];
  const { container, rerender } = render(carousel(0, protectedStill()));
  const image = container.querySelector("img")!;
  expect(image.parentElement!.style.backgroundImage).toBe("");
  expect(image.className).toContain("blur");
  spoilerPrefs.hideImages = false;
  rerender(carousel(0, protectedStill()));
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
      render(carousel(2, [episode(1, { title: "Pilot" })]));

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
      carousel(2, [
        episode(1, { title: "Pilot" }),
        episode(2, {
          title: "Next",
          runtime: 43,
          user_data: { played: false, position_seconds: 120, duration_seconds: 1800 },
        }),
        episode(3, {
          title: "Rewatch",
          runtime: 44,
          // Watched before and partly rewatched: no progress bar, but a
          // resume position Mark Unwatched can clear.
          user_data: { played: true, position_seconds: 300, duration_seconds: 1800 },
        }),
      ]),
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
      hasResumePosition: false,
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
      hasResumePosition: true,
      quickActionMode: "watched",
    });
    expect(capturedMenuProps[2]).toMatchObject({
      contentId: "ep-3",
      hasPartialProgress: false,
      hasResumePosition: true,
    });
  });
});
