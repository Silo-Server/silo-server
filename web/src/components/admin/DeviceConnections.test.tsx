import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import type { AdminDeviceDetail } from "@/api/types";
import { DeviceConnections } from "./DeviceConnections";

const resetLimit = vi.hoisted(() => ({ isPending: false, mutate: vi.fn() }));
vi.mock("@/hooks/queries/admin/users", () => ({
  useResetAdminDeviceLearnedLimit: () => resetLimit,
}));

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
  render(<DeviceConnections device={device} />);
  expect(screen.getByText(/^Faster than 115 Mbps on Ethernet/)).toBeInTheDocument();
  // Rounded down, as every client shows a rate.
  expect(screen.getByText(/^Faster than 51 Mbps · seen during playback/)).toBeInTheDocument();
});

it("clears a connection's learned limit for that device", () => {
  const device = {
    user_id: 7,
    device_id: "tv-1",
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
  render(<DeviceConnections device={device} />);
  fireEvent.click(screen.getByRole("button", { name: "Clear limit" }));
  expect(resetLimit.mutate).toHaveBeenCalledWith({
    userId: 7,
    deviceId: "tv-1",
    connectionId: "remote:abc",
  });
});
