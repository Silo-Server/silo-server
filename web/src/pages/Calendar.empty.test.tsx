// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

vi.mock("@/hooks/queries/calendar", () => ({
  useCalendarWeek: () => ({ data: [], isLoading: false }),
}));
vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: () => ({ data: [{ id: 1, name: "Library" }] }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => undefined }));
vi.mock("@/components/calendar/WeekNavigator", () => ({ default: () => null }));

import Calendar from "./Calendar";

function LocationProbe() {
  const location = useLocation();
  return <span data-testid="location">{location.search}</span>;
}

function renderCalendar(filter?: string) {
  const query = filter ? `&filter=${filter}` : "";
  render(
    <MemoryRouter initialEntries={[`/calendar?week=2026-04-06${query}`]}>
      <Routes>
        <Route
          path="/calendar"
          element={
            <>
              <Calendar />
              <LocationProbe />
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

// The empty-state panel holds the message and its view links.
function emptyState(message: string) {
  return screen.getByText(message).parentElement!;
}

function linkLabels(panel: HTMLElement) {
  return within(panel)
    .getAllByRole("button")
    .map((button) => button.textContent);
}

beforeEach(() => {
  localStorage.clear();
});
afterEach(cleanup);

it.each([
  [
    "following",
    "Nothing upcoming from shows you follow this week.",
    ["Trending", "Show everything"],
  ],
  ["trending", "Nothing trending this week.", ["Following", "Show everything"]],
  ["everything", "Nothing scheduled this week.", ["Following", "Trending"]],
  ["all", "Nothing scheduled this week.", ["Following", "Trending"]],
])("links an empty %s view to the other two views", (filter, message, links) => {
  renderCalendar(filter);
  expect(linkLabels(emptyState(message))).toEqual(links);
});

it("switches to the linked view", () => {
  renderCalendar("trending");
  const panel = emptyState("Nothing trending this week.");
  fireEvent.click(within(panel).getByRole("button", { name: "Show everything" }));
  expect(screen.getByTestId("location").textContent).toContain("filter=everything");
  expect(screen.getByText("Nothing scheduled this week.")).toBeTruthy();
});
