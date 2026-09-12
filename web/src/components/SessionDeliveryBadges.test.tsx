import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AdminSession } from "@/api/types";
import { SessionDeliveryBadges } from "./SessionDeliveryBadges";

function measuredSession(startedAt: string, unclaimedIdle = false): AdminSession {
  return {
    session_id: "session-1",
    started_at: startedAt,
    telemetry: {
      evidence: "measured",
      viewer_bytes: 8192,
      open_observations: 1,
      unclaimed_idle: unclaimedIdle,
    },
  } as AdminSession;
}

describe("SessionDeliveryBadges", () => {
  it("shows unclaimed delivery when the server classified the row and the view is readable", () => {
    render(
      <SessionDeliveryBadges
        session={measuredSession("2020-01-01T00:00:00Z", true)}
        viewBlind={false}
      />,
    );

    expect(screen.getByText("unclaimed")).toBeInTheDocument();
  });

  it("hides unclaimed framing for an incomplete view", () => {
    render(
      <SessionDeliveryBadges session={measuredSession("2020-01-01T00:00:00Z", true)} viewBlind />,
    );

    expect(screen.queryByText("unclaimed")).not.toBeInTheDocument();
  });

  // The badge the operator saw for ~45s after every normal stream ended. The server
  // only calls a row unclaimed once its viewer edge has gone quiet; measured bytes on
  // a stream that is still delivering are an ordinary session, and re-deriving the
  // badge from `evidence` alone painted every one of them red.
  it("does not badge measured delivery the server left unflagged", () => {
    render(
      <SessionDeliveryBadges session={measuredSession("2020-01-01T00:00:00Z")} viewBlind={false} />,
    );

    expect(screen.queryByText("unclaimed")).not.toBeInTheDocument();
  });
});
