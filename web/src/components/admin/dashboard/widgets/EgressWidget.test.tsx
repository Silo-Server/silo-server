import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminTimeseries } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useAdminTimeseries: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/dashboardInsights", () => ({
  useAdminTimeseries: mocks.useAdminTimeseries,
}));

import { EgressWidget } from "./EgressWidget";

const HOUR_MS = 3_600_000;

function series(overrides: Partial<AdminTimeseries> = {}): AdminTimeseries {
  const to = Date.now();
  const from = to - 24 * HOUR_MS;
  return {
    resolution_seconds: 300,
    from: new Date(from).toISOString(),
    to: new Date(to).toISOString(),
    oldest_sample_at: new Date(from - 7 * 24 * HOUR_MS).toISOString(),
    points: [
      {
        t: new Date(to - HOUR_MS).toISOString(),
        streams: 1,
        direct: 1,
        remux: 0,
        transcode: 0,
        egress_kbps: 56_000,
      },
    ],
    ...overrides,
  };
}

describe("EgressWidget", () => {
  beforeEach(() => {
    mocks.useAdminTimeseries.mockReset();
  });

  it("shows a zero total, which is a real answer", () => {
    mocks.useAdminTimeseries.mockReturnValue({ data: series({ egress_bytes: 0 }) });

    render(<EgressWidget />);

    expect(screen.getByText("Total 0 B")).toBeTruthy();
  });

  it("hides the total when the server does not send one", () => {
    mocks.useAdminTimeseries.mockReturnValue({ data: series() });

    render(<EgressWidget />);

    expect(screen.queryByText(/^Total /)).toBeNull();
    expect(screen.getByText("Peak 56 Mbps")).toBeTruthy();
  });
});
