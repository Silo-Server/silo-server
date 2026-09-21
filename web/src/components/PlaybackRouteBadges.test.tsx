import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AdminSession } from "@/api/types";
import { PlaybackRouteBadges } from "./PlaybackRouteBadges";

vi.mock("@/hooks/queries/admin/networkAccess", () => ({
  useNetworkAccessCapabilities: () => ({
    data: { providers: [{ provider: "tailscale", display_name: "Tailscale" }] },
  }),
}));

const session = {
  reporting_node: "70709751cdba",
  routing_workload: "remux",
  routing_execution: "api",
  routing_egress: "api",
} as AdminSession;

describe("PlaybackRouteBadges", () => {
  it("names the overlay and API server, retaining container identity in the tooltip", () => {
    render(<PlaybackRouteBadges session={{ ...session, routing_network_provider: "tailscale" }} />);
    expect(screen.getByLabelText("Network: Tailscale")).toBeVisible();
    expect(screen.getByText("API server")).toBeVisible();
    expect(screen.queryByText(session.reporting_node)).not.toBeInTheDocument();
    expect(screen.getByTitle(`API server: ${session.reporting_node} (serves media)`)).toBeVisible();
  });

  it("shows both a transcode executor and its proxy", () => {
    render(
      <PlaybackRouteBadges
        session={{
          ...session,
          routing_network_provider: "tailscale",
          routing_execution: "transcode",
          routing_execution_node_id: 7,
          routing_execution_node_name: "GPU worker",
          routing_egress: "proxy",
          routing_egress_node_id: 11,
          routing_egress_node_name: "Edge proxy",
        }}
      />,
    );
    expect(screen.getByLabelText("Network: Tailscale")).toBeVisible();
    expect(screen.getByLabelText("Transcode: GPU worker")).toBeVisible();
    expect(screen.getByLabelText("Proxy: Edge proxy")).toBeVisible();
    expect(screen.queryByText("API server")).not.toBeInTheDocument();
  });

  it("distinguishes the default network from missing route telemetry", () => {
    const { rerender } = render(
      <PlaybackRouteBadges session={{ ...session, routing_network_provider: "" }} />,
    );
    expect(screen.getByLabelText("Network: Default")).toHaveAttribute(
      "title",
      expect.stringContaining("LAN, public URL, or reverse proxy"),
    );
    rerender(<PlaybackRouteBadges session={{ ...session, client_ip: "100.64.0.1" }} />);
    expect(screen.getByLabelText("Network: Unknown")).toBeVisible();
    expect(screen.queryByLabelText("Network: Tailscale")).not.toBeInTheDocument();
  });

  it("retains the provider identifier when its plugin is no longer installed", () => {
    render(
      <PlaybackRouteBadges session={{ ...session, routing_network_provider: "other-provider" }} />,
    );
    expect(screen.getByLabelText("Network: other-provider")).toBeVisible();
  });
});
