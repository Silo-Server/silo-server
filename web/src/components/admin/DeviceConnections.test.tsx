import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import type { AdminDeviceDetail } from "@/api/types";
import { DeviceConnections } from "./DeviceConnections";

vi.mock("@/api/v2/request", () => ({ v2: vi.fn() }));

it("marks a lower-bound test and shows the best playback throughput", () => {
  const measured = new Date().toISOString();
  const device = {
    connections: [
      {
        id: "remote:abc",
        connection: "remote",
        probe: { kbps: 115000, lower_bound: true, measured_at: measured, transport: "ethernet" },
        playback_sample: { kbps: 51200, measured_at: measured },
        learned_limit: null,
        plan_summary: null,
      },
    ],
  } as unknown as AdminDeviceDetail;
  render(<DeviceConnections device={device} refresh={() => undefined} />);
  expect(screen.getByText(/^Faster than 115 Mbps on Ethernet/)).toBeInTheDocument();
  expect(screen.getByText(/^Faster than 51\.2 Mbps · seen during playback/)).toBeInTheDocument();
});
