import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ResolvedSection, SectionItem } from "@/api/types";
import SectionRow from "./SectionRow";

const mockTogglePin = vi.fn();
const mockIsPinned = vi.fn();
let latestCarouselProps:
  | {
      titleHref?: string;
      viewAllHref?: string;
      headerActions?: ReactNode;
    }
  | undefined;

vi.mock("@/components/MediaCarousel", () => ({
  default: ({
    children,
    titleHref,
    viewAllHref,
    headerActions,
    title,
  }: {
    children: ReactNode;
    titleHref?: string;
    viewAllHref?: string;
    headerActions?: ReactNode;
    title: string;
  }) => {
    latestCarouselProps = { titleHref, viewAllHref, headerActions };
    return (
      <section data-title={title} data-has-view-all={String(Boolean(viewAllHref))}>
        {headerActions}
        {children}
      </section>
    );
  },
}));

vi.mock("@/components/MediaItemMenu", () => ({
  default: () => <div>More actions</div>,
}));

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: () => {} }),
}));

vi.mock("@/hooks/queries/sidebarPins", () => ({
  useToggleSidebarPin: () => ({
    togglePin: mockTogglePin,
    isPinned: mockIsPinned,
    canToggle: true,
  }),
}));

function continueSection(items: ResolvedSection["items"]): ResolvedSection {
  return {
    id: "continue-watching",
    section_type: "continue_watching",
    title: "Continue Watching",
    featured: false,
    item_limit: 10,
    total_count: items.length,
    is_custom: false,
    customized: false,
    items,
  };
}

function progressItem(
  contentId: string,
  type: ResolvedSection["items"][number]["type"],
  art: { poster_url?: string; backdrop_url?: string },
): ResolvedSection["items"][number] {
  return {
    content_id: contentId,
    type,
    title: contentId,
    position_seconds: 600,
    duration_seconds: 6000,
    year: 2020,
    genres: [],
    status: "matched",
    rating_imdb: null,
    overview: "",
    poster_url: art.poster_url ?? "",
    poster_thumbhash: "",
    backdrop_url: art.backdrop_url ?? "",
    backdrop_thumbhash: "",
    logo_url: "",
  };
}

const pilotEpisode: ResolvedSection["items"][number] = {
  content_id: "ep-001",
  type: "episode",
  title: "Pilot",
  series_title: "Breaking Bad",
  season_number: 1,
  episode_number: 1,
  position_seconds: 120,
  duration_seconds: 3600,
  year: 0,
  genres: [],
  status: "matched",
  rating_imdb: null,
  overview: "Episode overview",
  poster_url: "",
  poster_thumbhash: "",
  backdrop_url: "/episode-backdrop.jpg",
  backdrop_thumbhash: "",
  logo_url: "",
};

function renderRow(section: ResolvedSection, libraryId?: number) {
  return renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <SectionRow section={section} libraryId={libraryId} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("SectionRow", () => {
  beforeEach(() => {
    latestCarouselProps = undefined;
    mockTogglePin.mockReset();
    mockIsPinned.mockReset();
    mockIsPinned.mockReturnValue(false);
  });

  it("renders continue watching sections with separate watch and item links", () => {
    const markup = renderRow(continueSection([pilotEpisode]));

    expect(markup).toContain('href="/watch/ep-001"');
    expect(markup).toContain('href="/item/ep-001"');
    expect(markup).toContain("Breaking Bad");
    expect(markup).toContain("Season 1 Episode 1");
    expect(markup).toContain("Pilot");
    expect(markup).toContain("More actions");
  });

  it("renders all-audiobook continue sections as square poster cards", () => {
    const markup = renderRow({
      ...continueSection([
        {
          ...progressItem("book-001", "audiobook", { poster_url: "/book-cover.jpg" }),
          title: "Project Hail Mary",
          duration_seconds: 58000,
          year: 2021,
        },
      ]),
      id: "continue-listening",
      title: "Continue Listening",
    });

    expect(markup).toContain("aspect-square");
    expect(markup).not.toContain("aspect-video");
  });

  it("falls back to the poster on a wide movie card without a backdrop", () => {
    const markup = renderRow(
      continueSection([progressItem("movie-1", "movie", { poster_url: "/movie-poster.jpg" })]),
    );

    expect(markup).toContain("aspect-video");
    expect(markup).toContain('src="/movie-poster.jpg"');
  });

  it("keeps continue sections that mix movies and books wide", () => {
    const markup = renderRow(
      continueSection([
        progressItem("movie-1", "movie", { backdrop_url: "/movie-backdrop.jpg" }),
        progressItem("book-1", "audiobook", { poster_url: "/book-cover.jpg" }),
      ]),
    );

    expect(markup).toContain("aspect-video");
    expect(markup).not.toContain("aspect-square");
    expect(markup).not.toContain("aspect-[2/3]");
  });

  const movie = (contentId: string, title: string): SectionItem => ({
    content_id: contentId,
    type: "movie",
    title,
    year: 1995,
    genres: [],
    status: "matched",
    rating_imdb: null,
    overview: "",
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
  });

  it("links every home row title to its browse page", () => {
    for (const sectionType of ["recently_added", "recommended_for_you", "trending_on_server"]) {
      renderRow({
        id: "featured-picks",
        section_type: sectionType,
        title: "Featured Picks",
        featured: false,
        item_limit: 1,
        total_count: 3,
        is_custom: false,
        customized: false,
        items: [movie("movie-1", "Heat")],
      });

      const href =
        "/catalog?source=section&scope=home&section_id=featured-picks&title=Featured+Picks";
      expect(latestCarouselProps?.titleHref).toBe(href);
      expect(latestCarouselProps?.viewAllHref).toBe(href);
      expect(String(renderToStaticMarkup(<>{latestCarouselProps?.headerActions}</>))).toBe("");
    }
  });

  it("links and pins library rows of every type", () => {
    const markup = renderRow({ ...continueSection([pilotEpisode]), item_limit: 1 }, 7);

    expect(markup).toContain("Pin to sidebar");
    const href =
      "/catalog?source=section&scope=library&section_id=continue-watching&library_id=7&title=Continue+Watching";
    expect(latestCarouselProps?.titleHref).toBe(href);
    expect(latestCarouselProps?.viewAllHref).toBe(href);
  });
});
