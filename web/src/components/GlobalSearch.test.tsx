import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { MouseEvent, ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  useQuery: vi.fn(),
  useCanRequest: vi.fn(),
  useRequestSearch: vi.fn(),
  usePersonSearch: vi.fn(),
  // Most tests only check the props GlobalSearch passes; the wiring tests for
  // request rows need the real section.
  renderRealRequestSection: false,
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
  shouldUseRouteViewTransition: () => true,
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: (...args: unknown[]) => mocks.useRequestSearch(...args),
}));

vi.mock("@/hooks/queries/personSearch", () => ({
  usePersonSearch: (...args: unknown[]) => mocks.usePersonSearch(...args),
}));

vi.mock("@/components/RequestToAddSection", async () => {
  const actual =
    await vi.importActual<typeof import("./RequestToAddSection")>("./RequestToAddSection");
  return {
    RequestToAddSection: (props: import("./RequestToAddSection").RequestToAddSectionProps) =>
      mocks.renderRealRequestSection ? (
        <actual.RequestToAddSection {...props} />
      ) : (
        <div data-testid="request-section">
          {`variant="${props.variant}" query="${props.query}" libraryHadHits="${String(props.libraryHadHits)}" libraryResultsKnown="${String(props.libraryResultsKnown)}"`}
        </div>
      ),
  };
});

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

import { buildQueryCatalogHref } from "@/pages/catalogSearchParams";
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

const personFixture = {
  id: "9007199254740993",
  name: "Test Actor",
  photo_url: "",
};

beforeEach(() => {
  mocks.usePersonSearch.mockReset();
  mocks.usePersonSearch.mockReturnValue({ data: [], isFetching: false });
});

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

  it("opens global search with Ctrl+K", () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(screen.queryByTestId("dialog")).toBeNull();
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });
    expect(screen.getByTestId("dialog")).toBeInTheDocument();
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
    const highlighted = screen.getByRole("option", { selected: true }).closest("[data-selected]");
    expect(highlighted).not.toBeNull();
    expect(highlighted).toHaveClass("data-[selected]:bg-accent");
    expect(document.querySelectorAll("[data-selected]")).toHaveLength(1);

    // Keystrokes after selecting must still reach the input, and a new query
    // clears the selection.
    await userEvent.keyboard("aking");

    expect(input).toHaveFocus();
    expect(input).toHaveValue("Testaking");
    expect(input).not.toHaveAttribute("aria-activedescendant");
  });

  it("stops at the last result and returns to the search box above the first", () => {
    const input = renderTwoResults();

    // Nothing sits above the search box.
    fireEvent.keyDown(input, { key: "ArrowUp" });
    expect(input).not.toHaveAttribute("aria-activedescendant");

    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
    expect(screen.getByRole("option", { selected: true })).toHaveTextContent("Second Movie");
    expect(input).toHaveFocus();

    // Past the end stays on the last result.
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");

    // Up from the first result clears the selection, back in the search box.
    fireEvent.keyDown(input, { key: "ArrowUp" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-0");
    fireEvent.keyDown(input, { key: "ArrowUp" });
    expect(input).not.toHaveAttribute("aria-activedescendant");
    expect(screen.queryByRole("option", { selected: true })).not.toBeInTheDocument();
    expect(input).toHaveFocus();
  });

  it("moves the Enter hint from the search box to the selected row and back", async () => {
    const input = renderTwoResults();
    const rowHints = () =>
      screen.getAllByRole("option").filter((option) => option.textContent?.includes("↵"));

    expect(screen.getByText("See all")).toBeInTheDocument();
    expect(rowHints()).toHaveLength(0);

    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(screen.queryByText("See all")).not.toBeInTheDocument();
    expect(rowHints()).toEqual([screen.getByRole("option", { selected: true })]);

    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(rowHints()).toEqual([screen.getByRole("option", { name: /Second Movie/ })]);

    fireEvent.keyDown(input, { key: "ArrowUp" });
    fireEvent.keyDown(input, { key: "ArrowUp" });
    expect(screen.getByText("See all")).toBeInTheDocument();
    expect(rowHints()).toHaveLength(0);

    // Back in the search box, Enter searches instead of opening a row.
    await userEvent.keyboard("{Enter}");
    expect(mocks.navigate).toHaveBeenCalledWith(buildQueryCatalogHref("Test"));
  });

  it("scrolls rows into view for the keyboard but not the pointer", () => {
    const input = renderTwoResults();
    const original = Element.prototype.scrollIntoView;
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    try {
      fireEvent.mouseMove(screen.getByRole("option", { name: /Second Movie/ }));
      expect(scrollIntoView).not.toHaveBeenCalled();
      expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");

      fireEvent.keyDown(input, { key: "ArrowUp" });
      expect(input).toHaveAttribute("aria-activedescendant", "search-result-0");
      expect(input).toHaveFocus();
      expect(scrollIntoView).toHaveBeenCalledTimes(1);
      expect(scrollIntoView.mock.contexts[0]).toHaveAttribute("id", "search-result-0");
    } finally {
      Element.prototype.scrollIntoView = original;
    }
  });

  it("reveals the last row when ArrowDown keeps the same selection", () => {
    const input = renderTwoResults();
    const scrollIntoView = vi.fn();
    const original = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = scrollIntoView;
    try {
      fireEvent.mouseMove(screen.getByRole("option", { name: /Second Movie/ }));
      expect(scrollIntoView).not.toHaveBeenCalled();

      fireEvent.keyDown(input, { key: "ArrowDown" });
      expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
      expect(scrollIntoView).toHaveBeenCalledTimes(1);
      expect(scrollIntoView.mock.contexts[0]).toHaveAttribute("id", "search-result-1");

      // After the viewer scrolls away, the same key must reveal the row again.
      scrollIntoView.mockClear();
      fireEvent.keyDown(input, { key: "ArrowDown" });
      expect(scrollIntoView).toHaveBeenCalledTimes(1);
      expect(scrollIntoView.mock.contexts[0]).toHaveAttribute("id", "search-result-1");
    } finally {
      Element.prototype.scrollIntoView = original;
    }
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
    // With nothing listed there is nothing to "see all" of.
    expect(screen.queryByText("See all")).not.toBeInTheDocument();
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

describe("GlobalSearch people results", () => {
  beforeEach(() => {
    mocks.navigate.mockReset();
    mocks.useQuery.mockReset();
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useRequestSearch.mockReturnValue({ data: undefined, isLoading: false, isError: false });
    mocks.useQuery.mockReturnValue({
      data: { total: 1, has_more: false, items: [browseFixture] },
      isFetching: false,
      isError: false,
    });
    mocks.usePersonSearch.mockReturnValue({ data: [personFixture], isFetching: false });
  });

  function renderOpen(initialQuery = "Test") {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch defaultOpen initialQuery={initialQuery} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const input = screen.getByRole("combobox", { name: "Search" });
    input.focus();
    return input;
  }

  it("searches people with the preview scope only while the dialog is open", () => {
    renderSearchMarkup({ defaultOpen: true, initialQuery: " Test " });
    // The unset profile preference defaults to Media.
    expect(mocks.usePersonSearch).toHaveBeenLastCalledWith("Test", 4, true, "video");

    renderSearchMarkup();
    expect(mocks.usePersonSearch).toHaveBeenLastCalledWith("", 4, false, "video");
  });

  it("lists people in their own group after the title results", () => {
    renderOpen();

    const options = screen.getAllByRole("option");
    expect(options.map((option) => option.getAttribute("aria-label"))).toEqual([
      "Test Movie, 2020, Movie",
      "Test Actor, Person",
    ]);
    const titles = screen.getByRole("group", { name: "Titles" });
    const people = screen.getByRole("group", { name: "People" });
    expect(titles).toContainElement(options[0]!);
    expect(people).toContainElement(options[1]!);
    // Only the lower group draws the divider between the two.
    expect(people).toHaveClass("border-t");
    expect(titles).not.toHaveClass("border-t");
    expect(screen.getByText("TA")).toBeInTheDocument();
  });

  it("leads with exact person matches only, leaving partial names to the search page", () => {
    mocks.usePersonSearch.mockReturnValue({
      data: [
        { ...personFixture, id: "1", name: "Chris" },
        { ...personFixture, id: "2", name: "Aaron Christ" },
      ],
      isFetching: false,
    });
    renderOpen("chris");

    expect(
      screen.getAllByRole("option").map((option) => option.getAttribute("aria-label")),
    ).toEqual(["Chris, Person", "Test Movie, 2020, Movie"]);
  });

  it("opens a person picked with the mouse", async () => {
    renderOpen();

    await userEvent.click(screen.getByRole("option", { name: "Test Actor, Person" }));

    expect(mocks.navigate).toHaveBeenCalledWith("/person/9007199254740993");
    expect(screen.queryByTestId("dialog")).not.toBeInTheDocument();
  });

  it("moves keyboard selection from titles into people and opens the person on Enter", () => {
    const input = renderOpen();

    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
    expect(screen.getByRole("option", { selected: true })).toHaveAccessibleName(
      "Test Actor, Person",
    );

    // Past the last person stays on it.
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
    fireEvent.keyDown(input, { key: "Enter" });

    expect(mocks.navigate).toHaveBeenCalledWith("/person/9007199254740993");
  });

  it("leads with people when a person's name matches the query exactly", () => {
    const input = renderOpen("test actor");

    expect(
      screen.getAllByRole("option").map((option) => option.getAttribute("aria-label")),
    ).toEqual(["Test Actor, Person", "Test Movie, 2020, Movie"]);

    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(mocks.navigate).toHaveBeenCalledWith("/person/9007199254740993");
  });

  it("keeps the selected title selected when an exact person match moves it down", () => {
    mocks.usePersonSearch.mockReturnValue({ data: undefined, isFetching: true });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const tree = () => (
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch defaultOpen initialQuery="Test Actor" />
        </MemoryRouter>
      </QueryClientProvider>
    );
    const { rerender } = render(tree());
    const input = screen.getByRole("combobox", { name: "Search" });
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-0");

    const original = Element.prototype.scrollIntoView;
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    try {
      mocks.usePersonSearch.mockReturnValue({ data: [personFixture], isFetching: false });
      rerender(tree());

      expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
      // The moved row stays in view.
      expect(scrollIntoView.mock.contexts.at(-1)).toHaveAttribute("id", "search-result-1");
    } finally {
      Element.prototype.scrollIntoView = original;
    }
    expect(screen.getByRole("option", { selected: true })).toHaveAccessibleName(
      "Test Movie, 2020, Movie",
    );
    fireEvent.keyDown(input, { key: "Enter" });
    expect(mocks.navigate).toHaveBeenCalledWith("/item/movie-99");
  });

  it("shows people instead of 'No matches' when no title matches", () => {
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });

    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Test Actor" });

    expect(markup).toContain("Test Actor, Person");
    expect(markup).not.toContain("No matches");
    expect(markup).toContain("1 results found");
  });

  it("keeps searching instead of showing 'No matches' while people are still loading", () => {
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.usePersonSearch.mockReturnValue({ data: undefined, isFetching: true });

    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Test Actor" });

    expect(markup).toContain("Searching...");
    expect(markup).not.toContain("No matches");
  });

  it("reports a failed people search instead of 'No matches' when no title matches", () => {
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.usePersonSearch.mockReturnValue({ data: undefined, isFetching: false, isError: true });

    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Test Actor" });

    expect(markup).toContain("Could not load results");
    expect(markup).not.toContain("No matches");
  });

  it("hides the people group when the people search fails", () => {
    mocks.usePersonSearch.mockReturnValue({ data: undefined, isFetching: false, isError: true });

    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Test" });

    expect(markup).toContain("Test Movie");
    expect(markup).not.toContain("People");
  });
});

describe("GlobalSearch request rows", () => {
  const discoveryOn = { discoveryEnabled: true, isResolving: false, submitDisabledReason: null };

  beforeEach(() => {
    mocks.navigate.mockReset();
    mocks.useQuery.mockReset();
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.renderRealRequestSection = true;
    mocks.useCanRequest.mockReturnValue(discoveryOn);
    mocks.useQuery.mockReturnValue({
      data: { total: 1, has_more: false, items: [browseFixture] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 2,
        results: [
          {
            media_type: "series",
            tmdb_id: 7,
            title: "Requested Show",
            availability: "missing",
            request: { requestable: false, status: "queued" },
          },
          {
            media_type: "movie",
            tmdb_id: 8,
            title: "Already Here",
            availability: "available",
            request: { requestable: false },
          },
        ],
      },
      isLoading: false,
      isError: false,
    });
  });
  afterEach(() => {
    mocks.renderRealRequestSection = false;
  });

  // The request rows load lazily; it returns once they are on screen.
  async function renderOpenSearch() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch defaultOpen initialQuery="Show" />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await screen.findByRole("option", { name: /Requested Show/ });
    const input = screen.getByRole("combobox", { name: "Search" });
    input.focus();
    return input;
  }

  it("offers See all when only request suggestions match", async () => {
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    await renderOpenSearch();
    expect(screen.getByText("See all")).toBeInTheDocument();
  });

  it("closes the dialog and opens the request page when a request row is clicked", async () => {
    await renderOpenSearch();

    const row = screen.getByRole("option", { name: /Requested Show/ });
    expect(row.querySelector('[data-request-state="processing"]')).toHaveTextContent("Processing");
    await userEvent.click(row);

    expect(mocks.navigate).toHaveBeenCalledExactlyOnceWith("/title/series/7");
    expect(screen.queryByTestId("dialog")).not.toBeInTheDocument();
  });

  it("walks from library rows into request rows with the arrow keys and opens one with Enter", async () => {
    const input = await renderOpenSearch();

    expect(input).toHaveAttribute(
      "aria-controls",
      "global-search-library-results global-search-request-results",
    );
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "ArrowDown" });

    expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
    expect(screen.getByRole("option", { selected: true })).toHaveTextContent("Requested Show");

    // Past the last request row stays on it.
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
    fireEvent.keyDown(input, { key: "Enter" });

    expect(mocks.navigate).toHaveBeenCalledExactlyOnceWith("/title/series/7");
    expect(screen.queryByTestId("dialog")).not.toBeInTheDocument();
  });

  it("reveals a pointer-selected last request row when ArrowDown takes over", async () => {
    const input = await renderOpenSearch();
    const scrollIntoView = vi.fn();
    const original = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = scrollIntoView;
    try {
      const row = screen.getByRole("option", { name: /Requested Show/ });
      fireEvent.mouseMove(row);
      expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
      expect(row).toHaveTextContent("↵");
      expect(scrollIntoView).not.toHaveBeenCalled();

      fireEvent.keyDown(input, { key: "ArrowDown" });
      expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
      expect(scrollIntoView).toHaveBeenCalledTimes(1);
      expect(scrollIntoView.mock.contexts[0]).toBe(row);
    } finally {
      Element.prototype.scrollIntoView = original;
    }
  });

  it("keeps the dialog and search when a request row opens in another tab", async () => {
    const input = await renderOpenSearch();
    const row = screen.getByRole("option", { name: /Requested Show/ });
    const preventNavigation = (event: Event) => event.preventDefault();
    document.addEventListener("click", preventNavigation);
    try {
      fireEvent.click(row, { metaKey: true });
      fireEvent.click(row, { ctrlKey: true });
      fireEvent.click(row, { shiftKey: true });
    } finally {
      document.removeEventListener("click", preventNavigation);
    }
    expect(input).toHaveValue("Show");
    expect(screen.getByTestId("dialog")).toBeInTheDocument();
    expect(mocks.navigate).not.toHaveBeenCalled();
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

  it("does not mount RequestToAddSection when discovery is disabled", async () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch defaultOpen initialQuery="Dune" />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    // Let the lazy section load, so a mounted one would be on screen.
    await act(async () => {
      await import("./RequestToAddSection");
    });

    expect(screen.queryByTestId("request-section")).not.toBeInTheDocument();
    const call = mocks.useRequestSearch.mock.calls[mocks.useRequestSearch.mock.calls.length - 1];
    expect(call?.[3]).toEqual({
      enabled: false,
      requireProfile: true,
      staleTime: 5 * 60 * 1000,
      gcTime: 30_000,
      retry: false,
    });
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

  it("marks library results unknown while the local preview is still pending", async () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useQuery.mockReturnValue({
      data: undefined,
      isFetching: true,
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

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch defaultOpen initialQuery="Dune" />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const section = (await screen.findByTestId("request-section")).textContent ?? "";

    expect(section).toContain('libraryHadHits="false"');
    expect(section).toContain('libraryResultsKnown="false"');
  });
});
