import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ReadingHistory } from "@/hooks/queries/readingStats";

const mockUseReadingHistory = vi.fn();

vi.mock("@/hooks/queries/readingStats", () => ({
  useReadingHistory: (...args: unknown[]) => mockUseReadingHistory(...args),
}));

vi.mock("@/hooks/useDocumentTitle", () => ({
  useDocumentTitle: () => undefined,
}));

vi.mock("@/components/stats/ReadingHeatmap", () => ({
  default: ({ days }: { days: { date: string; seconds: number }[] }) => (
    <div data-kind="reading-heatmap">
      {days.map((d) => (
        <span key={d.date} data-kind="heatmap-cell">
          {d.date}
        </span>
      ))}
    </div>
  ),
}));

import ReadingStats from "./ReadingStats";

function renderPage() {
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={["/reading-stats"]}>
      <Routes>
        <Route path="/reading-stats" element={<ReadingStats />} />
      </Routes>
    </MemoryRouter>,
  );
}

const fixture: ReadingHistory = {
  totals: {
    today_seconds: 600,
    week_seconds: 3600,
    month_seconds: 36000,
    all_time_seconds: 360000,
  },
  days: [
    { date: "2026-07-18", seconds: 0 },
    { date: "2026-07-19", seconds: 1800 },
    { date: "2026-07-20", seconds: 600 },
  ],
  books: [
    {
      content_id: "book-1",
      title: "Project Hail Mary",
      seconds: 7200,
      last_read_at: "2026-07-20T10:00:00Z",
    },
    {
      content_id: "book-2",
      title: "Removed book",
      seconds: 1200,
      last_read_at: "2026-07-15T10:00:00Z",
    },
  ],
  sessions: [
    {
      content_id: "book-1",
      title: "Project Hail Mary",
      started_at: "2026-07-20T09:00:00Z",
      duration_seconds: 1800,
      start_fraction: 0.1,
      end_fraction: 0.25,
    },
    {
      content_id: "book-2",
      title: "Removed book",
      started_at: "2026-07-19T09:00:00Z",
      duration_seconds: 600,
      start_fraction: 0.4,
      end_fraction: 0.42,
    },
  ],
};

describe("ReadingStats page", () => {
  beforeEach(() => {
    mockUseReadingHistory.mockReset();
  });

  it("shows a loading state while the history query is in flight", () => {
    mockUseReadingHistory.mockReturnValue({ data: undefined, isLoading: true, isError: false });
    const markup = renderPage();
    expect(markup).toContain("Reading stats");
  });

  it("shows an error state when the history query fails", () => {
    mockUseReadingHistory.mockReturnValue({ data: undefined, isLoading: false, isError: true });
    const markup = renderPage();
    expect(markup.toLowerCase()).toContain("failed");
  });

  it("renders the totals row with formatDuration applied", () => {
    mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
    const markup = renderPage();
    // today_seconds=600 -> 10m, week=3600 -> 1h 0m, month=36000 -> 10h 0m, all-time=360000 -> 100h 0m
    expect(markup).toContain("10m");
    expect(markup).toContain("1h 0m");
    expect(markup).toContain("10h 0m");
    expect(markup).toContain("100h 0m");
  });

  it("renders the top books list with a Removed book fallback and item links", () => {
    mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
    const markup = renderPage();
    expect(markup).toContain("Project Hail Mary");
    expect(markup).toContain("/item/book-1");
    expect(markup).toContain("Removed book");
    expect(markup).not.toContain("/item/book-2");
  });

  it("renders the sessions timeline with duration and fraction range", () => {
    mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
    const markup = renderPage();
    // session 1: duration 1800s -> 30m, fraction 10%-25%
    expect(markup).toContain("30m");
    expect(markup).toContain("10%");
    expect(markup).toContain("25%");
    // session 2 (removed book): shows title but suppresses item link
    expect(markup).toContain("Removed book");
    const bookTwoLinks = (markup.match(/\/item\/book-2/g) ?? []).length;
    expect(bookTwoLinks).toBe(0);
  });

  it("renders one heatmap cell per fixture day", () => {
    mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
    const markup = renderPage();
    const matches = markup.match(/data-kind="heatmap-cell"/g) ?? [];
    expect(matches).toHaveLength(fixture.days.length);
  });

  it("requests the default range (no explicit from/to)", () => {
    mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
    renderPage();
    expect(mockUseReadingHistory).toHaveBeenCalledWith();
  });
});
