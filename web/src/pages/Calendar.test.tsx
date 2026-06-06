import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockUseCalendarWeek = vi.fn();
const mockUseUserLibraries = vi.fn();

vi.mock("@/hooks/queries/calendar", () => ({
  useCalendarWeek: (...args: unknown[]) => mockUseCalendarWeek(...args),
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: (...args: unknown[]) => mockUseUserLibraries(...args),
}));

vi.mock("@/hooks/useDocumentTitle", () => ({
  useDocumentTitle: () => undefined,
}));

vi.mock("@/components/MediaCarousel", () => ({
  default: ({ children }: { children: ReactNode }) => (
    <div data-kind="media-carousel">{children}</div>
  ),
}));

vi.mock("@/components/calendar/WeekNavigator", () => ({
  default: () => <div data-kind="week-navigator" />,
}));

vi.mock("@/components/calendar/DayGroup", () => ({
  default: ({ day }: { day: { date: string } }) => <div data-kind="day-group">{day.date}</div>,
}));

vi.mock("@/components/ui/button", () => ({
  Button: ({ children }: { children: ReactNode }) => <button>{children}</button>,
}));

vi.mock("@/components/ui/select", () => ({
  Select: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectItem: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectTrigger: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SelectValue: () => null,
}));

import Calendar from "./Calendar";

function renderCalendar(entry = "/calendar?week=2026-04-06") {
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/calendar" element={<Calendar />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("Calendar page", () => {
  beforeEach(() => {
    mockUseCalendarWeek.mockReset();
    mockUseUserLibraries.mockReset();
    mockUseCalendarWeek.mockReturnValue({
      data: [],
      isLoading: false,
    });
    mockUseUserLibraries.mockReturnValue({
      data: [{ id: 1, name: "Library" }],
    });
  });

  it("loads only the current week", () => {
    renderCalendar("/calendar?week=2026-04-06&filter=watchlist");

    expect(mockUseCalendarWeek).toHaveBeenCalledTimes(1);
    expect(mockUseCalendarWeek).toHaveBeenCalledWith("2026-04-06", {
      filter: "watchlist",
      libraryId: undefined,
    });
  });

  it("passes through the selected library", () => {
    renderCalendar("/calendar?week=2026-04-06&library=7");

    expect(mockUseCalendarWeek).toHaveBeenCalledWith("2026-04-06", {
      filter: "following",
      libraryId: 7,
    });
  });
});
