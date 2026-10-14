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

  it("shows the server's transfer total beside the peak", () => {
    mocks.useAdminTimeseries.mockReturnValue({ data: series({ egress_bytes: 1_400_000_000_000 }) });

    render(<EgressWidget />);

    expect(screen.getByText("Total 1.4 TB")).toBeTruthy();
    expect(screen.getByText("Peak 56 Mbps")).toBeTruthy();
  });

  // jsdom does not evaluate container queries, so this pins the contract
  // instead: hidden by default, shown only once the card header is wide
  // enough, and never truncated or carrying hover-only details.
  it("drops the total, rather than truncating it, in a narrow header", () => {
    mocks.useAdminTimeseries.mockReturnValue({ data: series({ egress_bytes: 1_400_000_000_000 }) });

    render(<EgressWidget />);

    const total = screen.getByText("Total 1.4 TB");
    expect(total.classList).toContain("hidden");
    expect(total.classList).toContain("@md/card-header:inline");
    expect(total.classList).toContain("whitespace-nowrap");
    expect(total.classList).not.toContain("truncate");
    expect(total.getAttribute("title")).toBeNull();
    expect(total.closest('[data-slot="card-header"]')?.className).toContain(
      "@container/card-header",
    );
    // With the total gone, Peak must still be free to wrap; holding it on one
    // line pushes the range picker out of a minimum-span card.
    const peak = screen.getByText("Peak 56 Mbps");
    expect(peak.classList).not.toContain("whitespace-nowrap");
    expect(peak.classList).not.toContain("shrink-0");
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
