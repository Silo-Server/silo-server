import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { buildCatalogQueryUpdateHref, parseCatalogSearchParams } from "@/pages/catalogSearchParams";

const mocks = vi.hoisted(() => ({ transitionNavigate: vi.fn() }));

vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => mocks.transitionNavigate,
}));

import SearchBar from "./SearchBar";

function LocationProbe() {
  const location = useLocation();
  return <output aria-label="location">{`${location.pathname}${location.search}`}</output>;
}

describe("SearchBar", () => {
  afterEach(() => {
    vi.useRealTimers();
    mocks.transitionNavigate.mockReset();
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

  it("clears the active result route instead of leaving a stale search mounted", () => {
    vi.useFakeTimers();

    render(
      <MemoryRouter initialEntries={["/catalog?source=query&q=lanterns"]}>
        <SearchBar prominent initialQuery="lanterns" />
        <LocationProbe />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Clear search" }));

    const location = new URL(`http://example.test${screen.getByLabelText("location").textContent}`);
    expect(location.pathname).toBe("/catalog");
    expect(location.searchParams.get("source")).toBe("query");
    expect(location.searchParams.has("q")).toBe(false);
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

    act(() => vi.advanceTimersByTime(500));
    const location = new URL(`http://example.test${screen.getByLabelText("location").textContent}`);
    expect(location.pathname).toBe("/catalog");
    expect(location.searchParams.get("source")).toBe("query");
    expect(location.searchParams.has("q")).toBe(false);
    expect(mocks.transitionNavigate).not.toHaveBeenCalled();
  });
});
