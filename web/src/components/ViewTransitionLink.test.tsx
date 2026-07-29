import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { describe, expect, it, vi } from "vitest";
import SidebarItemNavigationProvider from "./SidebarItemNavigationProvider";
import ViewTransitionLink from "./ViewTransitionLink";

function LocationOutput() {
  const location = useLocation();
  return <output aria-label="location">{location.pathname}</output>;
}

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
