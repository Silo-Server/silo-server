import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminTimeseries } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useAdminTimeseries: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/dashboardInsights", () => ({
  useAdminTimeseries: mocks.useAdminTimeseries,
}));

import { WidgetChromeProvider } from "../widgetChrome";
import { EgressWidget } from "./EgressWidget";

/** The grid's part: the chosen view lives in the layout, not in the widget. */
function PlacedEgress({ initialView }: { initialView?: string }) {
  const [view, setView] = useState(initialView);
  return (
    <WidgetChromeProvider
      id="egress-24h"
      range="day"
      setRange={() => {}}
      view={view}
      setView={(_, next) => setView(next)}
    >
      <EgressWidget />
    </WidgetChromeProvider>
  );
}

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
    mockNodeEgress({ withNode: false });
    const { rerender } = render(<PlacedEgress />);
    expect(screen.queryByRole("button", { name: "Show egress by node" })).toBeNull();

    mockNodeEgress({ withNode: true });
    rerender(<PlacedEgress />);
    expect(screen.queryByText("proxy-ams")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Show egress by node" }));

    expect(screen.getByText("proxy-ams")).toBeTruthy();
    expect(screen.getByText("API server")).toBeTruthy();
  });

  it("opens on the view stored with its placement", () => {
    mockNodeEgress({ withNode: true });

    render(<PlacedEgress initialView="nodes" />);

    expect(screen.getByText("proxy-ams")).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "Show egress by node" }).getAttribute("aria-pressed"),
    ).toBe("true");
  });
});

/** One sampled hour; with a node, most of it served by proxy node 4. */
function mockNodeEgress({ withNode }: { withNode: boolean }) {
  const point = {
    t: new Date(Date.now() - HOUR_MS).toISOString(),
    streams: 1,
    direct: 1,
    remux: 0,
    transcode: 0,
    egress_kbps: 56_000,
    egress_by_source: (withNode ? { api: 6_000, "node:4": 50_000 } : { api: 56_000 }) as Record<
      string,
      number
    >,
  };
  const sources: AdminTimeseries["egress_sources"] = [
    { key: "api", kind: "api", name: "API server", egress_bytes: 0 },
  ];
  if (withNode) {
    sources.push({ key: "node:4", kind: "node", node_id: "4", name: "proxy-ams", egress_bytes: 0 });
  }
  mocks.useAdminTimeseries.mockReturnValue({
    data: series({ egress_sources: sources, points: [point] }),
  });
}
