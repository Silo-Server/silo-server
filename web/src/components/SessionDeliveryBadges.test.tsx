import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AdminSession } from "@/api/types";
import { SessionDeliveryBadges } from "./SessionDeliveryBadges";

function measuredSession(startedAt: string): AdminSession {
  return {
    session_id: "session-1",
    started_at: startedAt,
    telemetry: {
      evidence: "measured",
      viewer_bytes: 8192,
      open_observations: 1,
    },
  } as AdminSession;
}

describe("SessionDeliveryBadges", () => {
  it("shows unclaimed delivery for an old session in a complete view", () => {
    render(
      <SessionDeliveryBadges session={measuredSession("2020-01-01T00:00:00Z")} viewBlind={false} />,
    );

    expect(screen.getByText("unclaimed")).toBeInTheDocument();
  });

  it("hides unclaimed framing for an incomplete view", () => {
    render(<SessionDeliveryBadges session={measuredSession("2020-01-01T00:00:00Z")} viewBlind />);

    expect(screen.queryByText("unclaimed")).not.toBeInTheDocument();
  });

  it("hides unclaimed framing during the delivery grace window", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-27T12:00:00Z"));
    render(
      <SessionDeliveryBadges session={measuredSession("2026-08-27T11:59:55Z")} viewBlind={false} />,
    );

    expect(screen.queryByText("unclaimed")).not.toBeInTheDocument();
    vi.useRealTimers();
  });
});
