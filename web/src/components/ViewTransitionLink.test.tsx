import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation, useNavigationType } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
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
});

describe("ViewTransitionLink sidebar navigation", () => {
  it("lets Layout intercept an item navigation with its state intact", () => {
    const begin = vi.fn(() => true);
    render(
      <MemoryRouter initialEntries={["/"]}>
        <SidebarItemNavigationProvider begin={begin} itemDetailsReady>
          <ViewTransitionLink to="/item/movie-1?libraryId=2" state={{ source: "home" }}>
            Movie
          </ViewTransitionLink>
          <LocationOutput />
        </SidebarItemNavigationProvider>
      </MemoryRouter>,
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
    render(
      <MemoryRouter>
        <SidebarItemNavigationProvider begin={begin} itemDetailsReady>
          <ViewTransitionLink to="/item/movie-1" onClick={(event) => event.preventDefault()}>
            Movie
          </ViewTransitionLink>
        </SidebarItemNavigationProvider>
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Movie" }));
    expect(begin).not.toHaveBeenCalled();
  });

  it.each([
    ["meta", { metaKey: true }],
    ["control", { ctrlKey: true }],
    ["shift", { shiftKey: true }],
    ["alt", { altKey: true }],
  ])("does not intercept a %s-modified click", (_label, init) => {
    const begin = vi.fn(() => true);
    render(
      <MemoryRouter>
        <SidebarItemNavigationProvider begin={begin} itemDetailsReady>
          <ViewTransitionLink to="/item/movie-1">Movie</ViewTransitionLink>
        </SidebarItemNavigationProvider>
      </MemoryRouter>,
    );
    const event = new MouseEvent("click", { bubbles: true, cancelable: true, ...init });

    fireEvent(screen.getByRole("link", { name: "Movie" }), event);

    expect(begin).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("does not intercept a middle click", () => {
    const begin = vi.fn(() => true);
    render(
      <MemoryRouter>
        <SidebarItemNavigationProvider begin={begin} itemDetailsReady>
          <ViewTransitionLink to="/item/movie-1">Movie</ViewTransitionLink>
        </SidebarItemNavigationProvider>
      </MemoryRouter>,
    );
    const event = new MouseEvent("click", { bubbles: true, cancelable: true, button: 1 });

    fireEvent(screen.getByRole("link", { name: "Movie" }), event);

    expect(begin).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("does not intercept a link that opens a new browsing context", () => {
    const begin = vi.fn(() => true);
    render(
      <MemoryRouter>
        <SidebarItemNavigationProvider begin={begin} itemDetailsReady>
          <ViewTransitionLink to="/item/movie-1" target="_blank">
            Movie
          </ViewTransitionLink>
        </SidebarItemNavigationProvider>
      </MemoryRouter>,
    );
    const event = new MouseEvent("click", { bubbles: true, cancelable: true });

    fireEvent(screen.getByRole("link", { name: "Movie" }), event);

    expect(begin).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("falls through to router navigation when Layout declines interception", () => {
    const begin = vi.fn(() => false);
    render(
      <MemoryRouter initialEntries={["/"]}>
        <SidebarItemNavigationProvider begin={begin} itemDetailsReady>
          <ViewTransitionLink to="/item/movie-1">Movie</ViewTransitionLink>
          <LocationOutput />
        </SidebarItemNavigationProvider>
      </MemoryRouter>,
    );

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
  });
});

describe("ViewTransitionLink direction", () => {
  it("moves the page forward on an ordinary click", () => {
    render(
      <MemoryRouter initialEntries={["/"]}>
        <ViewTransitionLink to="/item/movie-1">Movie</ViewTransitionLink>
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Movie" }));

    expect(document.documentElement.dataset.navigationDirection).toBe("forward");
  });

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
    render(
      <MemoryRouter initialEntries={["/item/season"]}>
        <SidebarItemNavigationProvider begin={begin} itemDetailsReady>
          <ViewTransitionLink to="/item/series" up>
            Series
          </ViewTransitionLink>
        </SidebarItemNavigationProvider>
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("link", { name: "Series" }));

    // The interception exists to stage a descent into an item's heavy detail
    // page; unwinding out of one is the opposite move.
    expect(begin).not.toHaveBeenCalled();
  });
});
