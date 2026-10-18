import { act, fireEvent, render, screen } from "@testing-library/react";
import { Suspense } from "react";
import { MemoryRouter, useLocation, useNavigate, useSearchParams } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  buildCatalogQueryUpdateHref,
  buildQueryCatalogHref,
  parseCatalogSearchParams,
} from "@/pages/catalogSearchParams";

const mocks = vi.hoisted(() => ({ transitionNavigate: vi.fn() }));

vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => mocks.transitionNavigate,
}));

import SearchBar from "./SearchBar";

function LocationProbe() {
  const location = useLocation();
  return <output aria-label="location">{`${location.pathname}${location.search}`}</output>;
}

function currentQueryParam() {
  const location = new URL(`http://example.test${screen.getByLabelText("location").textContent}`);
  return location.searchParams.get("q");
}

// Searches the test has not finished yet. Rendering results for one of these
// suspends, which holds the router's navigation transition the way a slow
// search response does on a real results page.
const slowSearches = new Map<
  string,
  { promise: Promise<void>; finish: () => void; finished: boolean }
>();

function startSlowSearch(query: string) {
  let finish = () => {};
  const promise = new Promise<void>((resolve) => {
    finish = resolve;
  });
  const search = { promise, finished: false, finish: () => {} };
  search.finish = () => {
    search.finished = true;
    finish();
  };
  slowSearches.set(query, search);
}

async function finishSlowSearch(query: string) {
  const search = slowSearches.get(query);
  if (!search) throw new Error(`no slow search for ${query}`);
  await act(async () => {
    search.finish();
    await search.promise;
  });
}

function SlowResults({ query }: { query: string }) {
  const search = slowSearches.get(query);
  if (search && !search.finished) throw search.promise;
  return <p>{`Results for ${query}`}</p>;
}

// Mirrors the Search page: the query comes from the URL, an empty query shows
// the empty state with its own bar, and a query shows a second bar above the
// results.
function SearchPage() {
  const [searchParams] = useSearchParams();
  const query = searchParams.get("q")?.trim() ?? "";
  if (!query) {
    return (
      <section>
        <SearchBar prominent />
      </section>
    );
  }
  return (
    <div>
      <SearchBar prominent initialQuery={query} />
      <SlowResults query={query} />
    </div>
  );
}

function OpenQueryButton({ query }: { query: string }) {
  const navigate = useNavigate();
  return (
    <button type="button" onClick={() => navigate(buildQueryCatalogHref(query))}>
      {`Open ${query}`}
    </button>
  );
}

function VideoScopeButton() {
  const [searchParams, setSearchParams] = useSearchParams();
  return (
    <button
      type="button"
      onClick={() => {
        const next = new URLSearchParams(searchParams);
        next.set("type", "video");
        setSearchParams(next);
      }}
    >
      Videos only
    </button>
  );
}

function renderSearchPage(initialEntry: string, externalQuery = "dune") {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Suspense fallback={<p>Loading</p>}>
        <SearchPage />
        <LocationProbe />
        <OpenQueryButton query={externalQuery} />
        <VideoScopeButton />
      </Suspense>
    </MemoryRouter>,
  );
}

describe("SearchBar", () => {
  afterEach(() => {
    vi.useRealTimers();
    mocks.transitionNavigate.mockReset();
    slowSearches.clear();
  });

  it("keeps a trailing space when the URL echoes the trimmed query", () => {
    vi.useFakeTimers();
    renderSearchPage("/catalog?source=query&q=th");

    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "the " } });
    act(() => vi.advanceTimersByTime(201));

    expect(currentQueryParam()).toBe("the");
    expect(input).toHaveValue("the ");
  });

  it("keeps text typed while an earlier search is still loading", async () => {
    vi.useFakeTimers();
    renderSearchPage("/catalog?source=query&q=st");

    const input = screen.getByRole("textbox");
    startSlowSearch("sta");
    fireEvent.change(input, { target: { value: "sta" } });
    act(() => vi.advanceTimersByTime(201));
    fireEvent.change(input, { target: { value: "star" } });
    act(() => vi.advanceTimersByTime(100));

    await finishSlowSearch("sta");

    expect(screen.getByText("Results for sta")).toBeInTheDocument();
    expect(screen.getByRole("textbox")).toHaveValue("star");

    act(() => vi.advanceTimersByTime(201));
    expect(currentQueryParam()).toBe("star");
    expect(screen.getByRole("textbox")).toHaveValue("star");
  });

  it("keeps text typed while the first search replaces the empty Search page", async () => {
    vi.useFakeTimers();
    renderSearchPage("/catalog?source=query");

    startSlowSearch("s");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "s" } });
    act(() => vi.advanceTimersByTime(201));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "st" } });
    act(() => vi.advanceTimersByTime(100));

    await finishSlowSearch("s");

    expect(screen.getByText("Results for s")).toBeInTheDocument();
    expect(screen.getByRole("textbox")).toHaveValue("st");

    act(() => vi.advanceTimersByTime(201));
    expect(currentQueryParam()).toBe("st");
    expect(screen.getByRole("textbox")).toHaveValue("st");
  });

  it("does not carry unsent text into a Search page opened later", async () => {
    vi.useFakeTimers();
    const first = renderSearchPage("/catalog?source=query&q=st");

    startSlowSearch("sta");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "sta" } });
    act(() => vi.advanceTimersByTime(201));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "star" } });
    first.unmount();
    await act(async () => {});

    slowSearches.clear();
    renderSearchPage("/catalog?source=query&q=sta");
    expect(screen.getByRole("textbox")).toHaveValue("sta");
  });

  it("keeps text typed while several of its searches land together", async () => {
    vi.useFakeTimers();
    renderSearchPage("/catalog?source=query&q=st");

    const input = screen.getByRole("textbox");
    startSlowSearch("sta");
    startSlowSearch("star");
    fireEvent.change(input, { target: { value: "sta" } });
    act(() => vi.advanceTimersByTime(201));
    fireEvent.change(input, { target: { value: "star" } });
    act(() => vi.advanceTimersByTime(201));
    fireEvent.change(input, { target: { value: "stars" } });

    await finishSlowSearch("sta");
    await finishSlowSearch("star");

    expect(currentQueryParam()).toBe("star");
    expect(screen.getByRole("textbox")).toHaveValue("stars");
  });

  it("follows a link to the query it is still loading", async () => {
    vi.useFakeTimers();
    renderSearchPage("/catalog?source=query&q=st", "sta");

    const input = screen.getByRole("textbox");
    startSlowSearch("sta");
    fireEvent.change(input, { target: { value: "sta" } });
    act(() => vi.advanceTimersByTime(201));
    fireEvent.change(input, { target: { value: "star" } });
    fireEvent.click(screen.getByRole("button", { name: "Open sta" }));

    await finishSlowSearch("sta");

    expect(currentQueryParam()).toBe("sta");
    expect(screen.getByRole("textbox")).toHaveValue("sta");
    act(() => vi.advanceTimersByTime(500));
    expect(currentQueryParam()).toBe("sta");
    expect(screen.getByRole("textbox")).toHaveValue("sta");
  });

  it("keeps typed text when a navigation leaves the query alone", () => {
    vi.useFakeTimers();
    renderSearchPage("/catalog?source=query&q=th");

    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "the " } });
    act(() => vi.advanceTimersByTime(201));
    fireEvent.click(screen.getByRole("button", { name: "Videos only" }));

    const location = new URL(`http://example.test${screen.getByLabelText("location").textContent}`);
    expect(location.searchParams.get("type")).toBe("video");
    expect(location.searchParams.get("q")).toBe("the");
    expect(input).toHaveValue("the ");
  });

  it("shows a query from outside navigation", () => {
    vi.useFakeTimers();
    renderSearchPage("/catalog?source=query&q=heat");

    expect(screen.getByRole("textbox")).toHaveValue("heat");
    fireEvent.click(screen.getByRole("button", { name: "Open dune" }));

    expect(currentQueryParam()).toBe("dune");
    expect(screen.getByRole("textbox")).toHaveValue("dune");
  });

  it("keeps All Media selected when live search changes the query", () => {
    vi.useFakeTimers();
    const state = parseCatalogSearchParams(
      new URLSearchParams("source=query&q=heat&type=all&genre=Drama"),
    );

    render(
      <MemoryRouter initialEntries={["/catalog?source=query&q=heat&type=all&genre=Drama"]}>
        <SearchBar
          prominent
          initialQuery="heat"
          buildSearchHref={(query) => buildCatalogQueryUpdateHref(state, query)}
        />
        <LocationProbe />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByRole("textbox"), { target: { value: "heater" } });
    act(() => {
      vi.advanceTimersByTime(201);
    });

    const location = new URL(`http://example.test${screen.getByLabelText("location").textContent}`);
    expect(location.searchParams.get("q")).toBe("heater");
    expect(location.searchParams.get("type")).toBe("all");
    expect(parseCatalogSearchParams(location.searchParams).query_definition.groups).toContainEqual({
      match: "all",
      rules: [{ field: "genre", op: "contains", value: "Drama" }],
    });
    expect(mocks.transitionNavigate).not.toHaveBeenCalled();
  });

  it("settles rapid typing to one final route without a view transition", () => {
    vi.useFakeTimers();

    render(
      <MemoryRouter initialEntries={["/catalog?source=query&q=l"]}>
        <SearchBar prominent initialQuery="l" />
        <LocationProbe />
      </MemoryRouter>,
    );

    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "la" } });
    act(() => vi.advanceTimersByTime(100));
    fireEvent.change(input, { target: { value: "lan" } });
    act(() => vi.advanceTimersByTime(100));
    fireEvent.change(input, { target: { value: "lant" } });
    act(() => vi.advanceTimersByTime(201));

    const location = new URL(`http://example.test${screen.getByLabelText("location").textContent}`);
    expect(location.searchParams.get("q")).toBe("lant");
    expect(mocks.transitionNavigate).not.toHaveBeenCalled();
  });

  it("keeps a pending non-empty debounce from restoring the route after clear", () => {
    vi.useFakeTimers();

    render(
      <MemoryRouter initialEntries={["/catalog?source=query&q=lanterns"]}>
        <SearchBar prominent initialQuery="lanterns" />
        <LocationProbe />
      </MemoryRouter>,
    );

    fireEvent.change(screen.getByRole("textbox"), { target: { value: "lantern" } });
    act(() => vi.advanceTimersByTime(100));
    fireEvent.click(screen.getByRole("button", { name: "Clear search" }));

    const clearedLocation = new URL(
      `http://example.test${screen.getByLabelText("location").textContent}`,
    );
    expect(clearedLocation.pathname).toBe("/catalog");
    expect(clearedLocation.searchParams.get("source")).toBe("query");
    expect(clearedLocation.searchParams.has("q")).toBe(false);
    expect(mocks.transitionNavigate).not.toHaveBeenCalled();

    act(() => vi.advanceTimersByTime(500));
    const location = new URL(`http://example.test${screen.getByLabelText("location").textContent}`);
    expect(location.pathname).toBe("/catalog");
    expect(location.searchParams.get("source")).toBe("query");
    expect(location.searchParams.has("q")).toBe(false);
    expect(mocks.transitionNavigate).not.toHaveBeenCalled();
  });
});
