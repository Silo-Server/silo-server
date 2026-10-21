import type { MediaRequestOutcome, MediaRequestStatus } from "@/api/types";
import { requestDisplayState, type RequestDisplayState } from "@/lib/mediaRequests";
import { describe, expect, it } from "vitest";

describe("requestDisplayState", () => {
  it.each<[MediaRequestStatus | undefined, MediaRequestOutcome | undefined, RequestDisplayState]>([
    ["pending", "active", "pending"],
    ["approved", "active", "approved"],
    ["queued", "active", "processing"],
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
