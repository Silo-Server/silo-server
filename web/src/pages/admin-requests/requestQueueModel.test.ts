import { describe, expect, it } from "vitest";
import type { MediaRequest } from "@/api/types";
import {
  defaultRequestQueueView,
  formatRequestEventType,
  parseRequestQueueUser,
  parseRequestQueueView,
  requestQueueActions,
  requestQueueView,
} from "./requestQueueModel";

type Fields = Pick<MediaRequest, "status" | "outcome" | "targets">;
const sent = [{ quality: "1080p", status: "queued" }] as MediaRequest["targets"];

describe("requestQueueView", () => {
  it.each<[string, Fields, string]>([
    ["pending", { status: "pending", outcome: "active" }, "needs_approval"],
    ["approved", { status: "approved", outcome: "active" }, "in_progress"],
    ["queued", { status: "queued", outcome: "active" }, "in_progress"],
    ["downloading", { status: "downloading", outcome: "active" }, "in_progress"],
    ["completed", { status: "completed", outcome: "active" }, "done"],
    ["failed", { status: "approved", outcome: "failed" }, "failed"],
    ["declined while pending", { status: "pending", outcome: "declined" }, "done"],
    ["cancelled", { status: "approved", outcome: "cancelled" }, "done"],
  ])("puts a %s request where the server does", (_label, request, view) => {
    expect(requestQueueView(request)).toBe(view);
  });
});

describe("requestQueueActions", () => {
  it.each<[string, Fields, string[]]>([
    ["pending", { status: "pending", outcome: "active" }, ["approve", "decline"]],
    ["approved, nothing sent", { status: "approved", outcome: "active", targets: [] }, ["cancel"]],
    ["approved and sent", { status: "approved", outcome: "active", targets: sent }, []],
    ["queued", { status: "queued", outcome: "active", targets: sent }, []],
    // Admin cancel accepts only an active request, so a failed one offers Retry alone.
    ["failed", { status: "approved", outcome: "failed", targets: [] }, ["retry", "cancel"]],
    ["completed", { status: "completed", outcome: "active" }, []],
    ["declined", { status: "pending", outcome: "declined" }, []],
  ])("offers a %s request only what the server allows", (_label, request, actions) => {
    expect(requestQueueActions(request)).toEqual(actions);
  });

  it("never offers Retry for a pending request", () => {
    expect(requestQueueActions({ status: "pending", outcome: "active" })).not.toContain("retry");
  });
});

describe("queue links", () => {
  it("opens on what needs approval, else on what is under way", () => {
    const counts = { needs_approval: 0, in_progress: 0, failed: 2, done: 9 };
    expect(defaultRequestQueueView(counts)).toBe("in_progress");
    expect(defaultRequestQueueView({ ...counts, needs_approval: 1 })).toBe("needs_approval");
  });

  it("reads only known views and positive account IDs", () => {
    expect(parseRequestQueueView("failed")).toBe("failed");
    expect(parseRequestQueueView("settings")).toBeUndefined();
    expect(parseRequestQueueView(null)).toBeUndefined();
    expect(parseRequestQueueUser("7")).toBe(7);
    for (const value of ["0", "-1", "7a", "01", "", null, "99999999999"]) {
      expect(parseRequestQueueUser(value)).toBeUndefined();
    }
  });
});

describe("formatRequestEventType", () => {
  it.each([
    ["created", "Requested"],
    ["approved", "Approved"],
    ["status_approved", "Approved"],
    ["status_queued", "Sent to the server"],
    ["status_downloading", "Downloading"],
    ["status_completed", "Downloaded"],
    ["available_in_library", "In the library"],
    ["outcome_declined", "Declined"],
    ["outcome_cancelled", "Cancelled"],
    ["outcome_failed", "Failed"],
    ["retried", "Retried"],
    ["submit_deferred", "Couldn't send yet"],
    ["something_new", "something_new"],
  ])("names %s as %s", (type, label) => {
    expect(formatRequestEventType(type)).toBe(label);
  });
});
