import type { MouseEvent, ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  useQuery: vi.fn(),
  useCanRequest: vi.fn(),
  useRequestSearch: vi.fn(),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQuery: (...args: unknown[]) => mocks.useQuery(...args),
  };
});

vi.mock("@/hooks/useDebounce", () => ({
  useDebounce: <T,>(v: T) => v,
}));

vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => mocks.useCanRequest(),
}));

vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => mocks.navigate,
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: (...args: unknown[]) => mocks.useRequestSearch(...args),
}));

vi.mock("@/components/RequestToAddSection", () => ({
  RequestToAddSection: ({
    variant,
    query,
    libraryHadHits,
  }: {
    variant: string;
    query: string;
    libraryHadHits: boolean;
  }) => (
    <div data-testid="request-section">
      {`variant="${variant}" query="${query}" libraryHadHits="${String(libraryHadHits)}"`}
    </div>
  ),
}));

vi.mock("@/components/ui/dialog", () => ({
  Dialog: ({ children, open }: { children: ReactNode; open: boolean }) =>
    open ? <div data-testid="dialog">{children}</div> : null,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h2>{children}</h2>,
}));

vi.mock("@/lib/thumbhash", () => ({
  decodeThumbhash: () => "",
}));

vi.mock("@/components/CardPlayOverlay", () => ({
  default: ({
    contentId,
    title,
    size,
    onPlaybackStart,
  }: {
    contentId: string;
    title: string;
    size?: string;
    onPlaybackStart?: () => void;
  }) => (
    <a
      href={`/watch/${contentId}`}
      aria-label={`Play ${title}`}
      data-size={size}
      // Mirrors the real overlay: it swallows the click so the surrounding row
      // does not also navigate to the item page.
      onClick={(event: MouseEvent<HTMLAnchorElement>) => {
        event.stopPropagation();
        event.preventDefault();
        onPlaybackStart?.();
      }}
    />
  ),
}));

import { GlobalSearch } from "./GlobalSearch";

const browseFixture = {
  content_id: "movie-99",
  type: "movie" as const,
  title: "Test Movie",
  year: 2020,
  genres: [] as string[],
  content_rating: "PG",
  status: "matched" as const,
  rating_imdb: null as number | null,
  overview: "",
  poster_url: "",
  poster_thumbhash: "",
  backdrop_url: "",
  backdrop_thumbhash: "",
};

function renderSearchMarkup(props: Partial<Parameters<typeof GlobalSearch>[0]> = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <GlobalSearch {...props} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("GlobalSearch", () => {
  beforeEach(() => {
    mocks.navigate.mockReset();
    mocks.useQuery.mockReset();
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: false,
    });
    mocks.useQuery.mockReturnValue({
      data: {
        total: 50,
        has_more: true,
        items: [browseFixture],
      },
      isFetching: false,
      isError: false,
    });
  });

  it("renders preview rows and an approximate more-results hint", () => {
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Test" });

    expect(markup).toContain('data-testid="dialog"');
    expect(markup).toContain('placeholder="Search library..."');
    expect(markup).toContain("Test Movie");
    expect(markup).toContain("Showing top results");
    expect(markup).not.toContain("of 50");
    expect(markup).toContain("Press Enter for all results");
  });

  it("shows a compact independent play target for playable library results", () => {
    mocks.useQuery.mockReturnValue({
      data: {
        total: 1,
        has_more: false,
        items: [{ ...browseFixture, play_content_id: "movie-99" }],
      },
      isFetching: false,
      isError: false,
    });

    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Test" });
    expect(markup).toContain('href="/watch/movie-99"');
    expect(markup).toContain('aria-label="Play Test Movie"');
    expect(markup).toContain('data-size="compact"');
  });

  it("labels ebook preview rows with title case media type", () => {
    mocks.useQuery.mockReturnValue({
      data: {
        total: 1,
        has_more: false,
        items: [
          {
            ...browseFixture,
            type: "ebook",
            title: "A Reader",
          },
        ],
      },
      isFetching: false,
      isError: false,
    });

    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Reader" });

    expect(markup).toContain("2020 · Ebook");
  });

  it("disables the preview query when the dialog is closed", () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    renderToStaticMarkup(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(mocks.useQuery).toHaveBeenCalled();
    const lastCall = mocks.useQuery.mock.calls[mocks.useQuery.mock.calls.length - 1]![0] as {
      enabled: boolean;
    };
    expect(lastCall.enabled).toBe(false);
  });

  it("encodes picked item IDs before navigating", async () => {
    mocks.useQuery.mockReturnValue({
      data: {
        total: 1,
        has_more: false,
        items: [
          {
            ...browseFixture,
            content_id: "ebook 1",
            type: "ebook",
            title: "A Reader",
          },
        ],
      },
      isFetching: false,
      isError: false,
    });
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch defaultOpen initialQuery="Reader" />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await userEvent.click(screen.getByRole("option", { name: /A Reader/i }));

    expect(mocks.navigate).toHaveBeenCalledWith("/item/ebook%201");
  });

  function renderTwoResults() {
    mocks.useQuery.mockReturnValue({
      data: {
        total: 2,
        has_more: false,
        items: [
          { ...browseFixture, play_content_id: "movie-99" },
          { ...browseFixture, content_id: "movie-100", title: "Second Movie" },
        ],
      },
      isFetching: false,
      isError: false,
    });

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch defaultOpen initialQuery="Test" />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const input = screen.getByRole("combobox", { name: "Search" });
    input.focus();
    return input;
  }

  it("tracks selection with aria-activedescendant while keeping typing focus in the input", async () => {
    const input = renderTwoResults();

    expect(input).toHaveAttribute("aria-controls", "global-search-library-results");
    expect(input).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("listbox", { name: "Library search results" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { selected: true })).not.toBeInTheDocument();

    fireEvent.keyDown(input, { key: "ArrowDown" });

    expect(input).toHaveFocus();
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-0");
    expect(screen.getByRole("option", { selected: true })).toHaveAttribute("id", "search-result-0");

    // Keystrokes after selecting must still reach the input, and a new query
    // clears the selection.
    await userEvent.keyboard("aking");

    expect(input).toHaveFocus();
    expect(input).toHaveValue("Testaking");
    expect(input).not.toHaveAttribute("aria-activedescendant");
  });

  it("wraps ArrowUp from an unselected input to the last result and wraps at both ends", () => {
    const input = renderTwoResults();

    fireEvent.keyDown(input, { key: "ArrowUp" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
    expect(screen.getByRole("option", { selected: true })).toHaveTextContent("Second Movie");
    expect(input).toHaveFocus();

    // Past the end wraps back to the first result.
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-0");

    // Before the start wraps back to the last result.
    fireEvent.keyDown(input, { key: "ArrowUp" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
  });

  it("opens the selected result when Enter is pressed", () => {
    const input = renderTwoResults();

    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(mocks.navigate).toHaveBeenCalledWith("/item/movie-99");
  });

  it("clears selection and keeps focus in the input when there are no results", () => {
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch defaultOpen initialQuery="Test" />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const input = screen.getByRole("combobox", { name: "Search" });
    input.focus();
    fireEvent.keyDown(input, { key: "ArrowDown" });

    expect(input).toHaveFocus();
    expect(input).not.toHaveAttribute("aria-activedescendant");
  });

  it("keeps Play an independent control alongside the selectable row", async () => {
    renderTwoResults();

    const play = screen.getByRole("link", { name: "Play Test Movie" });
    expect(play).toBeInTheDocument();
    const option = screen.getByRole("option", { name: "Test Movie, 2020, Movie" });
    expect(option).toBeInTheDocument();

    // role="option" is "Children Presentational: True": a link INSIDE the
    // option would be stripped of its role and name by conforming browsers and
    // AT, leaving screen-reader users no way to reach Play. jsdom does not
    // implement that rule, so getByRole above cannot catch the regression —
    // assert the DOM relationship directly instead.
    expect(option.contains(play)).toBe(false);

    await userEvent.click(play);

    // Play starts playback and closes the dialog without navigating to the
    // item page the surrounding row points at.
    expect(mocks.navigate).not.toHaveBeenCalled();
    expect(screen.queryByTestId("dialog")).not.toBeInTheDocument();
  });
});

describe("GlobalSearch + RequestToAddSection wiring", () => {
  beforeEach(() => {
    mocks.navigate.mockReset();
    mocks.useQuery.mockReset();
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: false,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 50, has_more: true, items: [browseFixture] },
      isFetching: false,
      isError: false,
    });
  });

  it("renders the section with libraryHadHits=true when library returned results", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          {
            media_type: "movie",
            tmdb_id: 1,
            title: "X",
            availability: "missing",
            request: { requestable: true },
          },
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Dune" });

    expect(markup).toContain('data-testid="request-section"');
    expect(markup).toContain("libraryHadHits=&quot;true&quot;");
    expect(markup).toContain("variant=&quot;dialog&quot;");
  });

  it("renders the section with libraryHadHits=false when library returned 0 results", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          {
            media_type: "movie",
            tmdb_id: 1,
            title: "X",
            availability: "missing",
            request: { requestable: true },
          },
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "ThisDoesNotExist" });

    expect(markup).toContain("libraryHadHits=&quot;false&quot;");
  });

  it("does not call useRequestSearch with enabled=true when discoveryEnabled is false", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    renderSearchMarkup({ defaultOpen: true, initialQuery: "Dune" });

    const call = mocks.useRequestSearch.mock.calls[mocks.useRequestSearch.mock.calls.length - 1];
    expect(call?.[3]).toEqual({
      enabled: false,
      requireProfile: true,
      staleTime: 5 * 60 * 1000,
    });
  });

  it("does not mount RequestToAddSection when discovery is disabled", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Dune" });

    expect(markup).not.toContain('data-testid="request-section"');
  });

  it("suppresses 'No matches' when library is empty and TMDB is still loading", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Pending" });

    expect(markup).not.toContain("No matches");
  });

  it("suppresses 'No matches' when library is empty and TMDB has missing results", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          {
            media_type: "movie",
            tmdb_id: 1,
            title: "X",
            availability: "missing",
            request: { requestable: true },
          },
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "FoundOnTmdb" });

    expect(markup).not.toContain("No matches");
  });

  it("still shows 'No matches' when both library and TMDB are empty", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 0, results: [] },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "ZzzNothing" });

    expect(markup).toContain("No matches");
  });
});
