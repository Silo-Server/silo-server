import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AdminSession } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useAdminLiveSessions: vi.fn(),
  useAdminStats: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/stats", () => ({
  useAdminLiveSessions: (includeHidden: boolean) => mocks.useAdminLiveSessions(includeHidden),
  useAdminStats: () => mocks.useAdminStats(),
}));

import AdminStats from "./AdminStats";

describe("AdminStats", () => {
  it("counts sessions from the live endpoint including idle rows", () => {
    mocks.useAdminStats.mockReturnValue({
      data: { total_items: 10, total_files: 12, total_users: 3 },
      isLoading: false,
      error: null,
    });
    mocks.useAdminLiveSessions.mockReturnValue({
      data: {
        sessions: [
          { session_id: "session-1", started_at: "2026-08-27T10:00:00Z" },
          { session_id: "session-2", started_at: "2026-08-27T10:01:00Z" },
        ] as AdminSession[],
      },
      isLoading: false,
      error: null,
    });

    render(<AdminStats />);

    // include_idle stays OFF here: it reveals unclaimed_idle rows too, which are ENDED
    // sessions the registry still remembers. A "live" count must not include them.
    expect(mocks.useAdminLiveSessions).toHaveBeenCalledWith(false);
    expect(screen.getByText("2")).toBeInTheDocument();
  });
});
