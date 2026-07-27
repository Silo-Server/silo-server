import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ReadingHistory, ReadingMotivation } from "@/hooks/queries/readingStats";

const mockUseReadingHistory = vi.fn();
const mockUseReadingMotivation = vi.fn();

vi.mock("@/hooks/queries/readingStats", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/readingStats")>(
    "@/hooks/queries/readingStats",
  );
  return {
    ...actual,
    useReadingHistory: (...args: unknown[]) => mockUseReadingHistory(...args),
    useReadingMotivation: (...args: unknown[]) => mockUseReadingMotivation(...args),
  };
});

vi.mock("@/hooks/useDocumentTitle", () => ({
  useDocumentTitle: () => undefined,
}));

vi.mock("@/components/stats/ReadingHeatmap", () => ({
  default: ({
    days,
    from,
    to,
  }: {
    days: { date: string; seconds: number }[];
    from?: string;
    to?: string;
  }) => (
    <div data-kind="reading-heatmap" data-from={from} data-to={to}>
      {days.map((d) => (
        <span key={d.date} data-kind="heatmap-cell">
          {d.date}
        </span>
      ))}
    </div>
  ),
}));

vi.mock("@/components/stats/MotivationSections", () => ({
  StreakChallengeSection: ({ streak }: { streak: unknown }) => (
    <div data-kind="streak-challenge-section" data-has-streak={String(streak != null)} />
  ),
  GoalsSection: ({ goals }: { goals: unknown }) => (
    <div data-kind="goals-section" data-has-goals={String(goals != null)} />
  ),
  AchievementsSection: ({ achievements }: { achievements: unknown[] | null | undefined }) => (
    <div data-kind="achievements-section" data-count={String(achievements?.length ?? 0)} />
  ),
  ReadingDnaSection: ({ dna }: { dna: unknown }) => (
    <div data-kind="reading-dna-section" data-has-dna={String(dna != null)} />
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

const motivationFixture: ReadingMotivation = {
  streak: { current_days: 5, longest_days: 12, today_seconds: 900, today_qualified: true },
  goals: {
    books_per_year: 24,
    hours_per_year: 100,
    books_finished_ytd: 6,
    hours_ytd: 42.5,
    books_on_track_for: 12,
    hours_on_track_for: 85,
  },
  challenge: { target_seconds: 10000, month_seconds: 4000, percent: 40 },
  achievements: [
    {
      id: "first-hour",
      category: "time",
      name: "First Hour",
      description: "Read for 1 hour total",
      achieved_at: "2026-01-05T00:00:00Z",
    },
  ],
  dna: {
    genres: [{ name: "Sci-Fi", seconds: 7200 }],
    authors: [{ name: "Andy Weir", seconds: 5400 }],
    diversity_score: 62,
    avg_session_seconds: 1500,
    hours_by_bucket: { morning: 2, afternoon: 1, evening: 4, night: 0 },
    projected_year_hours: 87.3,
  },
};

describe("ReadingStats page", () => {
  beforeEach(() => {
    mockUseReadingHistory.mockReset();
    mockUseReadingMotivation.mockReset();
    mockUseReadingMotivation.mockReturnValue({ data: undefined, isLoading: false, isError: false });
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

  it("requests the server's default range explicitly (today in the viewer's timezone, minus 365 days)", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-20T12:34:56Z"));
    const resolvedOptionsSpy = vi
      .spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions")
      .mockReturnValue({ timeZone: "UTC" } as Intl.ResolvedDateTimeFormatOptions);
    try {
      mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
      renderPage();
      // Mirrors the server default in HandleHistory: to = today in the
      // viewer's timezone, from = to minus 365 days. Passed explicitly
      // (rather than omitted) so the same range can be handed to the
      // heatmap for densifying gap days. The third arg is the viewer's IANA
      // timezone.
      const [callFrom, callTo] = mockUseReadingHistory.mock.calls[0] ?? [];
      expect(callFrom).toBe("2025-07-20");
      expect(callTo).toBe("2026-07-20");
    } finally {
      vi.useRealTimers();
      resolvedOptionsSpy.mockRestore();
    }
  });

  it("derives today from the viewer's local timezone, not UTC", () => {
    // 2026-07-19T23:30:00Z is local 2026-07-20T01:30 in Europe/Amsterdam
    // (CEST, UTC+2): a UTC-derived "today" would request "2026-07-19" here,
    // one full local day early.
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-19T23:30:00Z"));
    const resolvedOptionsSpy = vi
      .spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions")
      .mockReturnValue({ timeZone: "Europe/Amsterdam" } as Intl.ResolvedDateTimeFormatOptions);
    try {
      mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
      renderPage();
      const [callFrom, callTo] = mockUseReadingHistory.mock.calls[0] ?? [];
      expect(callTo).toBe("2026-07-20");
      expect(callFrom).toBe("2025-07-20");
    } finally {
      vi.useRealTimers();
      resolvedOptionsSpy.mockRestore();
    }
  });

  it("appends the viewer's timezone as the tz argument on the history fetch", () => {
    const mockedZone = "Europe/Amsterdam";
    const resolvedOptionsSpy = vi
      .spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions")
      .mockReturnValue({ timeZone: mockedZone } as Intl.ResolvedDateTimeFormatOptions);
    try {
      mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
      renderPage();
      expect(mockUseReadingHistory).toHaveBeenCalledWith(
        expect.any(String),
        expect.any(String),
        mockedZone,
      );
    } finally {
      resolvedOptionsSpy.mockRestore();
    }
  });

  it("renders the motivation sections wired from the mocked useReadingMotivation hook", () => {
    mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
    mockUseReadingMotivation.mockReturnValue({
      data: motivationFixture,
      isLoading: false,
      isError: false,
    });
    const markup = renderPage();
    expect(markup).toContain('data-kind="streak-challenge-section" data-has-streak="true"');
    expect(markup).toContain('data-kind="goals-section" data-has-goals="true"');
    expect(markup).toContain('data-kind="achievements-section" data-count="1"');
    expect(markup).toContain('data-kind="reading-dna-section" data-has-dna="true"');
  });

  it("renders the motivation sections even when the history query errors", () => {
    // The four motivation sections are driven entirely by useReadingMotivation
    // and must not be gated behind the (independent) reading-history query's
    // error state.
    mockUseReadingHistory.mockReturnValue({ data: undefined, isLoading: false, isError: true });
    mockUseReadingMotivation.mockReturnValue({
      data: motivationFixture,
      isLoading: false,
      isError: false,
    });
    const markup = renderPage();
    expect(markup.toLowerCase()).toContain("failed");
    expect(markup).toContain('data-kind="streak-challenge-section" data-has-streak="true"');
    expect(markup).toContain('data-kind="goals-section" data-has-goals="true"');
    expect(markup).toContain('data-kind="achievements-section" data-count="1"');
    expect(markup).toContain('data-kind="reading-dna-section" data-has-dna="true"');
  });

  it("renders the motivation sections even while the history query is loading", () => {
    mockUseReadingHistory.mockReturnValue({ data: undefined, isLoading: true, isError: false });
    mockUseReadingMotivation.mockReturnValue({
      data: motivationFixture,
      isLoading: false,
      isError: false,
    });
    const markup = renderPage();
    expect(markup).toContain('data-kind="streak-challenge-section" data-has-streak="true"');
    expect(markup).toContain('data-kind="goals-section" data-has-goals="true"');
    expect(markup).toContain('data-kind="achievements-section" data-count="1"');
    expect(markup).toContain('data-kind="reading-dna-section" data-has-dna="true"');
  });

  it("passes null-tolerant undefined slices to the motivation sections before motivation data loads", () => {
    mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
    mockUseReadingMotivation.mockReturnValue({ data: undefined, isLoading: true, isError: false });
    const markup = renderPage();
    expect(markup).toContain('data-kind="streak-challenge-section" data-has-streak="false"');
    expect(markup).toContain('data-kind="goals-section" data-has-goals="false"');
    expect(markup).toContain('data-kind="achievements-section" data-count="0"');
    expect(markup).toContain('data-kind="reading-dna-section" data-has-dna="false"');
  });

  it("passes the same range to the heatmap that it requests from the history hook", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-20T12:34:56Z"));
    try {
      mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
      const markup = renderPage();
      // Both the hook call and the heatmap's from/to props must agree on the
      // exact same range, or the client-side densify (which uses the
      // heatmap's range) would disagree with what the server actually
      // rolled up into `data.days`.
      const [hookFrom, hookTo] = mockUseReadingHistory.mock.calls[0] ?? [];
      expect(markup).toContain(`data-from="${hookFrom}"`);
      expect(markup).toContain(`data-to="${hookTo}"`);
    } finally {
      vi.useRealTimers();
    }
  });

  it("places the totals row before the motivation sections", () => {
    mockUseReadingHistory.mockReturnValue({ data: fixture, isLoading: false, isError: false });
    mockUseReadingMotivation.mockReturnValue({
      data: motivationFixture,
      isLoading: false,
      isError: false,
    });
    const markup = renderPage();
    const totalsIndex = markup.indexOf("All time");
    const motivationIndex = markup.indexOf('data-kind="streak-challenge-section"');
    expect(totalsIndex).toBeGreaterThan(-1);
    expect(motivationIndex).toBeGreaterThan(-1);
    expect(totalsIndex).toBeLessThan(motivationIndex);
  });
});
