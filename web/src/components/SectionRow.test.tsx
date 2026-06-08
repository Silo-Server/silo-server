import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ResolvedSection } from "@/api/types";
import SectionRow from "./SectionRow";

const mockTogglePin = vi.fn();
const mockIsPinned = vi.fn();
const mockNavigate = vi.fn();
let latestCarouselProps:
  | {
      onViewAll?: () => void;
      headerActions?: ReactNode;
    }
  | undefined;

vi.mock("@/components/MediaCarousel", () => ({
  default: ({
    children,
    onViewAll,
    headerActions,
    title,
  }: {
    children: ReactNode;
    onViewAll?: () => void;
    headerActions?: ReactNode;
    title: string;
  }) => {
    latestCarouselProps = { onViewAll, headerActions };
    return (
      <section data-title={title} data-has-view-all={String(Boolean(onViewAll))}>
        {headerActions}
        {children}
      </section>
    );
  },
}));

vi.mock("@/components/MediaItemMenu", () => ({
  default: () => <div>More actions</div>,
}));

vi.mock("@/hooks/queries/sidebarPins", () => ({
  useToggleSidebarPin: () => ({
    togglePin: mockTogglePin,
    isPinned: mockIsPinned,
  }),
}));

vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => mockNavigate,
}));

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));

describe("SectionRow", () => {
  beforeEach(() => {
    latestCarouselProps = undefined;
    mockTogglePin.mockReset();
    mockIsPinned.mockReset();
    mockNavigate.mockReset();
    mockIsPinned.mockReturnValue(false);
  });

  it("renders continue watching sections with separate watch and item links", () => {
    const queryClient = new QueryClient();
    const section: ResolvedSection = {
      id: "continue-watching",
      section_type: "continue_watching",
      title: "Continue Watching",
      featured: false,
      item_limit: 10,
      total_count: 1,
      is_custom: false,
      customized: false,
      items: [
        {
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
        },
      ],
    };

    const markup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <SectionRow section={section} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('href="/watch/ep-001"');
    expect(markup).toContain('href="/item/ep-001"');
    expect(markup).toContain("Breaking Bad");
    expect(markup).toContain("Season 1 Episode 1");
    expect(markup).toContain("Pilot");
    expect(markup).toContain("More actions");
  });

  it("routes supported section view-all actions to browse destinations", () => {
    const queryClient = new QueryClient();
    const section: ResolvedSection = {
      id: "recently-added",
      section_type: "recently_added",
      title: "Recently Added",
      featured: false,
      item_limit: 1,
      total_count: 3,
      is_custom: false,
      customized: false,
      items: [
        {
          content_id: "movie-1",
          type: "movie",
          title: "Alien",
          year: 1979,
          genres: [],
          status: "matched",
          rating_imdb: null,
          overview: "",
          poster_url: "",
          poster_thumbhash: "",
          backdrop_url: "",
          backdrop_thumbhash: "",
          logo_url: "",
        },
      ],
    };

    renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <SectionRow section={section} libraryId={7} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(latestCarouselProps?.onViewAll).toBeTypeOf("function");

    latestCarouselProps?.onViewAll?.();

    expect(mockNavigate).toHaveBeenCalledWith(
      "/catalog?source=section&scope=library&section_id=recently-added&library_id=7&title=Recently+Added",
    );
  });

  it("routes supported home section view-all actions to home browse destinations", () => {
    const queryClient = new QueryClient();
    const section: ResolvedSection = {
      id: "featured-picks",
      section_type: "recently_added",
      title: "Featured Picks",
      featured: false,
      item_limit: 1,
      total_count: 3,
      is_custom: false,
      customized: false,
      items: [
        {
          content_id: "movie-1",
          type: "movie",
          title: "Heat",
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
        },
      ],
    };

    renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <SectionRow section={section} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(latestCarouselProps?.onViewAll).toBeTypeOf("function");

    latestCarouselProps?.onViewAll?.();

    expect(mockNavigate).toHaveBeenCalledWith(
      "/catalog?source=section&scope=home&section_id=featured-picks&title=Featured+Picks",
    );
    expect(String(renderToStaticMarkup(<>{latestCarouselProps?.headerActions}</>))).toBe("");
  });

  it("only shows pin affordances for browse-supported section types", () => {
    const queryClient = new QueryClient();
    const unsupportedSection: ResolvedSection = {
      id: "continue-watching",
      section_type: "continue_watching",
      title: "Continue Watching",
      featured: false,
      item_limit: 1,
      total_count: 1,
      is_custom: false,
      customized: false,
      items: [
        {
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
        },
      ],
    };

    const unsupportedMarkup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <SectionRow section={unsupportedSection} libraryId={7} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(unsupportedMarkup).not.toContain("Pin to sidebar");
    expect(latestCarouselProps?.onViewAll).toBeUndefined();

    const supportedSection: ResolvedSection = {
      ...unsupportedSection,
      id: "recently-added",
      section_type: "recently_added",
      title: "Recently Added",
      total_count: 4,
      items: [
        {
          content_id: "movie-1",
          type: "movie",
          title: "Alien",
          year: 1979,
          genres: [],
          status: "matched",
          rating_imdb: null,
          overview: "",
          poster_url: "",
          poster_thumbhash: "",
          backdrop_url: "",
          backdrop_thumbhash: "",
          logo_url: "",
        },
      ],
    };

    const supportedMarkup = renderToStaticMarkup(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <SectionRow section={supportedSection} libraryId={7} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(supportedMarkup).toContain("Pin to sidebar");
    expect(latestCarouselProps?.onViewAll).toBeTypeOf("function");
  });
});
