import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import type { MediaRequestOutcome, MediaRequestStatus } from "@/api/types";
import { requestDisplayState, type RequestDisplayState } from "@/lib/mediaRequests";
import { RequestReasonBadge, RequestStatusBadge } from "./RequestStatusBadge";

describe("requestDisplayState", () => {
  it.each<[MediaRequestStatus | undefined, MediaRequestOutcome | undefined, RequestDisplayState]>([
    ["pending", "active", "pending"],
    ["approved", "active", "approved"],
    ["queued", "active", "processing"],
    ["downloading", "active", "processing"],
    ["completed", "active", "available"],
    ["completed", undefined, "available"],
    // A closed outcome wins over the status the request closed at.
    ["pending", "declined", "declined"],
    ["pending", "cancelled", "cancelled"],
    ["downloading", "failed", "failed"],
  ])("maps status %s with outcome %s to %s", (status, outcome, state) => {
    expect(requestDisplayState(status, outcome)).toBe(state);
  });

  it("has no state without a status or a closed outcome", () => {
    expect(requestDisplayState(undefined, "active")).toBeUndefined();
    expect(requestDisplayState()).toBeUndefined();
  });
});

describe("RequestStatusBadge", () => {
  it.each<[RequestDisplayState, string, string]>([
    ["pending", "Pending", "outline"],
    ["approved", "Approved", "secondary"],
    ["processing", "Processing", "secondary"],
    ["available", "Available", "default"],
    ["declined", "Declined", "outline"],
    ["cancelled", "Cancelled", "outline"],
    ["failed", "Failed", "destructive"],
  ])("labels %s as %s on the %s badge", (state, label, variant) => {
    render(<RequestStatusBadge state={state} />);

    const badge = screen.getByText(label).closest("[data-slot='badge']");
    expect(badge).toHaveAttribute("data-variant", variant);
    expect(badge).toHaveAttribute("data-request-state", state);
  });

  it("uses theme tokens rather than fixed palette colors", () => {
    const states: RequestDisplayState[] = [
      "pending",
      "approved",
      "processing",
      "available",
      "declined",
      "cancelled",
      "failed",
    ];
    const { container } = render(
      <>
        {states.map((state) => (
          <RequestStatusBadge key={state} state={state} overlay />
        ))}
      </>,
    );

    expect(container.innerHTML).not.toMatch(/(amber|emerald|sky|zinc|red)-\d/);
  });

  it("gives an outline badge a backing only when it sits over artwork", () => {
    const { rerender } = render(<RequestStatusBadge state="pending" />);
    expect(screen.getByText("Pending").closest("[data-slot='badge']")).not.toHaveClass(
      "bg-background/85",
    );

    rerender(<RequestStatusBadge state="pending" overlay />);
    expect(screen.getByText("Pending").closest("[data-slot='badge']")).toHaveClass(
      "bg-background/85",
    );
  });

  it("leads the label with a count", () => {
    render(<RequestStatusBadge state="processing" count={3} />);

    expect(screen.getByText("3").closest("[data-slot='badge']")).toHaveTextContent("3Processing");
  });
});

describe("RequestReasonBadge", () => {
  it("explains why a title cannot be requested", () => {
    render(<RequestReasonBadge reason="quota_exceeded" />);

    expect(screen.getByText("Request limit reached")).toBeInTheDocument();
  });
});
