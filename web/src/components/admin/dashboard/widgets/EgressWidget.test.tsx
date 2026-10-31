import { fireEvent, render, screen } from "@testing-library/react";
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

  it("offers a per-node view only once a stream node served egress", () => {
    const at = new Date(Date.now() - HOUR_MS).toISOString();
    const point = {
      t: at,
      streams: 1,
      direct: 1,
      remux: 0,
      transcode: 0,
      egress_kbps: 56_000,
      egress_by_source: { api: 6_000, "node:4": 50_000 },
    };
    mocks.useAdminTimeseries.mockReturnValue({
      data: series({
        egress_sources: [{ key: "api", kind: "api", name: "API server", egress_bytes: 0 }],
        points: [{ ...point, egress_by_source: { api: 56_000 } }],
      }),
    });
    const { rerender } = render(<EgressWidget />);
    expect(screen.queryByRole("button", { name: "Show egress by node" })).toBeNull();

    mocks.useAdminTimeseries.mockReturnValue({
      data: series({
        egress_sources: [
          { key: "api", kind: "api", name: "API server", egress_bytes: 0 },
          { key: "node:4", kind: "node", node_id: "4", name: "proxy-ams", egress_bytes: 0 },
        ],
        points: [point],
      }),
    });
    rerender(<EgressWidget />);
    expect(screen.queryByText("proxy-ams")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Show egress by node" }));

    expect(screen.getByText("proxy-ams")).toBeTruthy();
    expect(screen.getByText("API server")).toBeTruthy();
  });
});
