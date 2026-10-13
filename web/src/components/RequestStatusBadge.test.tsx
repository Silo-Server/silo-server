import type { MediaRequestOutcome, MediaRequestStatus } from "@/api/types";
import { requestDisplayState, type RequestDisplayState } from "@/lib/mediaRequests";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
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
});

describe("RequestReasonBadge", () => {
  it("explains why a title cannot be requested", () => {
    render(<RequestReasonBadge reason="quota_exceeded" />);

    expect(screen.getByText("Request limit reached")).toBeInTheDocument();
  });
});
