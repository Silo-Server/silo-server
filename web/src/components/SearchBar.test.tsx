import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { buildCatalogQueryUpdateHref, parseCatalogSearchParams } from "@/pages/catalogSearchParams";

import SearchBar from "./SearchBar";

function LocationProbe() {
  const location = useLocation();
  return <output aria-label="location">{`${location.pathname}${location.search}`}</output>;
}

describe("SearchBar", () => {
  afterEach(() => {
    vi.useRealTimers();
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
      vi.advanceTimersByTime(101);
    });

    const location = new URL(`http://example.test${screen.getByLabelText("location").textContent}`);
    expect(location.searchParams.get("q")).toBe("heater");
    expect(location.searchParams.get("type")).toBe("all");
    expect(parseCatalogSearchParams(location.searchParams).query_definition.groups).toContainEqual({
      match: "all",
      rules: [{ field: "genre", op: "contains", value: "Drama" }],
    });
  });
});
