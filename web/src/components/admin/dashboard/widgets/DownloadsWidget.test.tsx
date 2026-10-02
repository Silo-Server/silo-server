import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminDownloadsStats } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useAdminDownloadsStats: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/dashboardInsights", () => ({
  useAdminDownloadsStats: mocks.useAdminDownloadsStats,
}));

import { DownloadsWidget } from "./DownloadsWidget";

function stats(overrides: Partial<AdminDownloadsStats> = {}): AdminDownloadsStats {
  return {
    // 7 rather than 2: the per-user list renders rank numbers, so a small
    // headline value would collide with a rank in getByText.
    users_with_downloads: 7,
    active_downloads: 14,
    total_bytes: 5 * 1024 ** 3,
    downloads_started_24h: 3,
    downloads_completed_24h: 2,
    limit: 10,
    top_users: [
      { user_id: 3, username: "quick", downloads: 11, total_bytes: 4 * 1024 ** 3 },
      { user_id: 5, username: "", downloads: 3, total_bytes: 1024 ** 3 },
    ],
    ...overrides,
  };
}

describe("DownloadsWidget", () => {
  beforeEach(() => {
    mocks.useAdminDownloadsStats.mockReset();
  });

  it("shows the headline numbers and the per-user list", () => {
    mocks.useAdminDownloadsStats.mockReturnValue({
      data: stats(),
      isLoading: false,
      error: null,
    });

    render(<DownloadsWidget />);

    expect(screen.getByText("Users")).toBeTruthy();
    expect(screen.getByText("7")).toBeTruthy();
    expect(screen.getByText("Items")).toBeTruthy();
    expect(screen.getByText("14")).toBeTruthy();
    expect(screen.getByText("On devices")).toBeTruthy();
    expect(screen.getByText("5.0 GB")).toBeTruthy();
    expect(screen.getByText("quick")).toBeTruthy();
    expect(screen.getByText("11 items")).toBeTruthy();
    expect(screen.getByText(/3 started · 2 finished \(24h\)/)).toBeTruthy();
  });

  it("labels an account with no username by its id", () => {
    mocks.useAdminDownloadsStats.mockReturnValue({
      data: stats(),
      isLoading: false,
      error: null,
    });

    render(<DownloadsWidget />);

    expect(screen.getByText("User #5")).toBeTruthy();
  });

  it("reads all zeros as the empty state, not as data", () => {
    mocks.useAdminDownloadsStats.mockReturnValue({
      data: stats({
        users_with_downloads: 0,
        active_downloads: 0,
        total_bytes: 0,
        downloads_started_24h: 0,
        downloads_completed_24h: 0,
        top_users: [],
      }),
      isLoading: false,
      error: null,
    });

    render(<DownloadsWidget />);

    expect(screen.getByText("No offline downloads")).toBeTruthy();
    expect(screen.queryByText("Users")).toBeNull();
  });

  it("still shows the 24h line when only ephemeral web downloads happened", () => {
    mocks.useAdminDownloadsStats.mockReturnValue({
      data: stats({
        users_with_downloads: 0,
        active_downloads: 0,
        total_bytes: 0,
        downloads_started_24h: 1,
        downloads_completed_24h: 1,
        top_users: [],
      }),
      isLoading: false,
      error: null,
    });

    render(<DownloadsWidget />);

    expect(screen.getByText(/1 started · 1 finished \(24h\)/)).toBeTruthy();
    expect(screen.getByText("No offline downloads")).toBeTruthy();
  });

  it("surfaces a failed load", () => {
    mocks.useAdminDownloadsStats.mockReturnValue({
      data: undefined,
      isLoading: false,
      error: new Error("boom"),
    });

    render(<DownloadsWidget />);

    expect(screen.getByText("Failed to load download activity.")).toBeTruthy();
  });

  it("renders a skeleton while loading", () => {
    mocks.useAdminDownloadsStats.mockReturnValue({
      data: undefined,
      isLoading: true,
      error: null,
    });

    const { container } = render(<DownloadsWidget />);

    expect(container.querySelector('[data-slot="skeleton"], .animate-pulse')).toBeTruthy();
    expect(screen.queryByText("No offline downloads")).toBeNull();
  });
});
