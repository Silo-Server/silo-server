import { beforeEach, describe, expect, it, vi } from "vitest";

import getCalendarOk from "../../../../contracts/api/v2/fixtures/get_calendar_ok.json";

const mockUseQuery = vi.fn();
const mockFetchWithSession = vi.fn();

vi.mock("@tanstack/react-query", () => ({
  useQuery: (...args: unknown[]) => mockUseQuery(...args),
}));

vi.mock("@/api/client", () => ({
  fetchWithSession: (...args: unknown[]) => mockFetchWithSession(...args),
  reportProfileUnverified: vi.fn(),
}));

function calendarResponse() {
  return {
    res: new Response(JSON.stringify(getCalendarOk), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
    requestProfileId: "p-owner",
    requestProfileToken: null,
  };
}

import { useCalendarWeek } from "./calendar";

describe("useCalendarWeek", () => {
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const encodedTimezone = encodeURIComponent(timezone);

  beforeEach(() => {
    mockUseQuery.mockReset();
    mockFetchWithSession.mockReset();
    mockUseQuery.mockImplementation((options: unknown) => options);
    mockFetchWithSession.mockImplementation(() => Promise.resolve(calendarResponse()));
  });

  it("requests an inclusive seven-day calendar window", async () => {
    useCalendarWeek("2026-04-06", { filter: "all" });
    const queryOptions = mockUseQuery.mock.calls[0]?.[0] as {
      queryFn: (ctx: { signal?: AbortSignal }) => Promise<unknown>;
    };

    const days = await queryOptions.queryFn({});

    expect(days).toEqual(getCalendarOk.events);
    expect(mockUseQuery).toHaveBeenCalledWith(
      expect.objectContaining({
        queryKey: ["calendar", "week", "2026-04-06", "all", "all", timezone],
        staleTime: 10 * 60 * 1000,
      }),
    );
    expect(mockFetchWithSession.mock.calls[0]?.[0]).toBe(
      `/api/v2/calendar?start=2026-04-06&end=2026-04-12&filter=all&timezone=${encodedTimezone}`,
    );
  });

  it("includes the selected library in the request", async () => {
    useCalendarWeek("2026-04-06", { filter: "favorites", libraryId: 7 });
    const queryOptions = mockUseQuery.mock.calls[0]?.[0] as {
      queryFn: (ctx: { signal?: AbortSignal }) => Promise<unknown>;
    };

    await queryOptions.queryFn({});

    expect(mockFetchWithSession.mock.calls[0]?.[0]).toBe(
      `/api/v2/calendar?start=2026-04-06&end=2026-04-12&filter=favorites&timezone=${encodedTimezone}&library_id=7`,
    );
  });
});
