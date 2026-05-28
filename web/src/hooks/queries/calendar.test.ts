import { beforeEach, describe, expect, it, vi } from "vitest";

const mockUseQuery = vi.fn();
const mockApi = vi.fn();

vi.mock("@tanstack/react-query", () => ({
  useQuery: (...args: unknown[]) => mockUseQuery(...args),
}));

vi.mock("@/api/client", () => ({
  api: (...args: unknown[]) => mockApi(...args),
}));

import { useCalendarWeek } from "./calendar";

describe("useCalendarWeek", () => {
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const encodedTimezone = encodeURIComponent(timezone);

  beforeEach(() => {
    mockUseQuery.mockReset();
    mockApi.mockReset();
    mockUseQuery.mockImplementation((options: unknown) => options);
    mockApi.mockResolvedValue({ events: [] });
  });

  it("requests an inclusive seven-day calendar window", async () => {
    useCalendarWeek("2026-04-06", { filter: "all" });
    const queryOptions = mockUseQuery.mock.calls[0]?.[0] as { queryFn: () => Promise<unknown> };

    await queryOptions.queryFn();

    expect(mockUseQuery).toHaveBeenCalledWith(
      expect.objectContaining({
        queryKey: ["calendar", "week", "2026-04-06", "all", "all", timezone],
        staleTime: 10 * 60 * 1000,
      }),
    );
    expect(mockApi).toHaveBeenCalledWith(
      `/calendar?start=2026-04-06&end=2026-04-12&filter=all&timezone=${encodedTimezone}`,
    );
  });

  it("includes the selected library in the request", async () => {
    useCalendarWeek("2026-04-06", { filter: "favorites", libraryId: 7 });
    const queryOptions = mockUseQuery.mock.calls[0]?.[0] as { queryFn: () => Promise<unknown> };

    await queryOptions.queryFn();

    expect(mockApi).toHaveBeenCalledWith(
      `/calendar?start=2026-04-06&end=2026-04-12&filter=favorites&timezone=${encodedTimezone}&library_id=7`,
    );
  });
});
