import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation, useNavigationType } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { catalogKeys } from "@/hooks/queries/keys";
import { resetIntentPrefetchState } from "@/lib/intentPrefetch";
import { resetNavigationHistory, resolveCommittedDirection } from "@/lib/navigationHistory";
import SidebarItemNavigationProvider from "./SidebarItemNavigationProvider";
import ViewTransitionLink from "./ViewTransitionLink";

function LocationOutput() {
  const location = useLocation();
  return <output aria-label="location">{location.pathname}</output>;
}

function NavigationTypeOutput() {
  return <output aria-label="navigation type">{useNavigationType()}</output>;
}

/** Stands in for the browser having committed the entry at `idx`. */
function commit(idx: number) {
  window.history.replaceState({ idx }, "");
}

afterEach(() => {
  resetNavigationHistory();
  window.history.replaceState(null, "");
  delete document.documentElement.dataset.navigationDirection;
  vi.unstubAllGlobals();
});

function renderWithSidebar(
  begin: () => boolean,
  link: ReactNode,
  initialEntries: string[] = ["/"],
) {
  render(
    <MemoryRouter initialEntries={initialEntries}>
      <SidebarItemNavigationProvider begin={begin} itemDetailsReady>
        {link}
        <LocationOutput />
      </SidebarItemNavigationProvider>
    </MemoryRouter>,
  );
}

describe("ViewTransitionLink sidebar navigation", () => {
  it("lets Layout intercept an item navigation with its state intact", () => {
    const begin = vi.fn(() => true);
    renderWithSidebar(
      begin,
      <ViewTransitionLink to="/item/movie-1?libraryId=2" state={{ source: "home" }}>
        Movie
      </ViewTransitionLink>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Movie" }));

    expect(begin).toHaveBeenCalledWith({
      href: "/item/movie-1?libraryId=2",
      replace: undefined,
      state: { source: "home" },
    });
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/");
  });

  it("honors a caller that prevents navigation", () => {
    const begin = vi.fn(() => true);
    renderWithSidebar(
      begin,
      <ViewTransitionLink to="/item/movie-1" onClick={(event) => event.preventDefault()}>
        Movie
      </ViewTransitionLink>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Movie" }));
    expect(begin).not.toHaveBeenCalled();
  });

  it.each([
    ["meta-modified click", {}, { metaKey: true }],
    ["control-modified click", {}, { ctrlKey: true }],
    ["shift-modified click", {}, { shiftKey: true }],
    ["middle click", {}, { button: 1 }],
    ["link that opens a new browsing context", { target: "_blank" }, {}],
  ])("does not intercept a %s", (_label, linkProps, init) => {
    const begin = vi.fn(() => true);
    renderWithSidebar(
      begin,
      <ViewTransitionLink to="/item/movie-1" {...linkProps}>
        Movie
      </ViewTransitionLink>,
    );
    const event = new MouseEvent("click", { bubbles: true, cancelable: true, ...init });

    fireEvent(screen.getByRole("link", { name: "Movie" }), event);

    expect(begin).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("falls through to router navigation when Layout declines interception", () => {
    const begin = vi.fn(() => false);
    renderWithSidebar(begin, <ViewTransitionLink to="/item/movie-1">Movie</ViewTransitionLink>);

    fireEvent.click(screen.getByRole("link", { name: "Movie" }));

    expect(begin).toHaveBeenCalledOnce();
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/movie-1");
  });

  it("behaves as a plain router link without a provider", () => {
    render(
      <MemoryRouter initialEntries={["/"]}>
        <ViewTransitionLink to="/item/movie-1">Movie</ViewTransitionLink>
        <LocationOutput />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Movie" }));

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/movie-1");
    expect(document.documentElement.dataset.navigationDirection).toBe("forward");
  });
});

describe("ViewTransitionLink direction", () => {
  it("replaces rather than pushes a second entry for the current URL", () => {
    render(
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <ViewTransitionLink to="/item/movie-1">Movie</ViewTransitionLink>
        <NavigationTypeOutput />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Movie" }));

    expect(screen.getByRole("status", { name: "navigation type" })).toHaveTextContent("REPLACE");
  });

  it("still navigates an `up` link the history map never recorded", () => {
    render(
      <MemoryRouter initialEntries={["/item/season"]}>
        <ViewTransitionLink to="/item/series" up>
          Series
        </ViewTransitionLink>
        <LocationOutput />
        <NavigationTypeOutput />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Series" }));

    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/series");
    expect(screen.getByRole("status", { name: "navigation type" })).toHaveTextContent("PUSH");
    expect(document.documentElement.dataset.navigationDirection).toBe("back");
  });

  it("keeps an `up` link a real anchor so modified clicks are the browser's", () => {
    commit(0);
    resolveCommittedDirection();
    commit(1);

    render(
      <MemoryRouter initialEntries={["/item/series", "/item/season"]} initialIndex={1}>
        <ViewTransitionLink to="/item/series" up>
          Series
        </ViewTransitionLink>
        <LocationOutput />
      </MemoryRouter>,
    );
    const link = screen.getByRole("link", { name: "Series" });
    expect(link).toHaveAttribute("href", "/item/series");

    const event = new MouseEvent("click", { bubbles: true, cancelable: true, metaKey: true });
    fireEvent(link, event);

    expect(event.defaultPrevented).toBe(false);
    expect(screen.getByRole("status", { name: "location" })).toHaveTextContent("/item/season");
  });

  it("does not offer an `up` link to the sidebar item interception", () => {
    const begin = vi.fn(() => true);
    renderWithSidebar(
      begin,
      <ViewTransitionLink to="/item/series" up>
        Series
      </ViewTransitionLink>,
      ["/item/season"],
    );

    fireEvent.click(screen.getByRole("link", { name: "Series" }));

    // The interception exists to stage a descent into an item's heavy detail
    // page; unwinding out of one is the opposite move.
    expect(begin).not.toHaveBeenCalled();
  });
});

describe("ViewTransitionLink prefetch", () => {
  it("warms the detail query an item link opens when it is pressed", () => {
    resetIntentPrefetchState();
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    );
    const queryClient = new QueryClient();
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ViewTransitionLink to="/item/movie-1?libraryId=2">Movie</ViewTransitionLink>
          <ViewTransitionLink to="/library/2">Library</ViewTransitionLink>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    fireEvent.pointerDown(screen.getByRole("link", { name: "Library" }), {
      pointerType: "mouse",
      button: 0,
    });
    expect(queryClient.getQueryCache().getAll()).toHaveLength(0);

    fireEvent.pointerDown(screen.getByRole("link", { name: "Movie" }), {
      pointerType: "mouse",
      button: 0,
    });
    // The key the item page reads, library scope included.
    expect(queryClient.getQueryState(catalogKeys.itemDetail("movie-1", 2))?.fetchStatus).toBe(
      "fetching",
    );
  });

  it("leaves the item page its retry when the page joins a press prefetch", async () => {
    // The page's query dedupes onto the running prefetch and inherits its
    // options, so the prefetch must not turn retries off.
    resetIntentPrefetchState();
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(new Response("{}", { status: 500 }))
      .mockReturnValue(new Promise<Response>(() => {}));
    vi.stubGlobal("fetch", fetch);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: 1, retryDelay: 0 } },
    });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ViewTransitionLink to="/item/movie-1">Movie</ViewTransitionLink>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    fireEvent.pointerDown(screen.getByRole("link", { name: "Movie" }), {
      pointerType: "mouse",
      button: 0,
    });
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  });
});
