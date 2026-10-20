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
  // Rounded down, as every client shows a rate.
  expect(screen.getByText(/^Faster than 51 Mbps · seen during playback/)).toBeInTheDocument();
});

it("shows a learned limit rounded down", () => {
  const device = {
    connections: [
      {
        id: "remote:abc",
        connection: "remote",
        probe: null,
        playback_sample: null,
        learned_limit: {
          kbps: 44_999,
          observed_at: "2026-10-17T12:00:00Z",
          expires_at: "2026-10-24T12:00:00Z",
        },
        plan_summary: null,
      },
    ],
  } as unknown as AdminDeviceDetail;
  render(<DeviceConnections device={device} refresh={() => undefined} />);
  expect(screen.getByText(/^Limited to 44 Mbps after buffering/)).toBeInTheDocument();
});
