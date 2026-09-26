import { describe, expect, it } from "vitest";

import { ApiClientError } from "@/api/client";
import { V2ProblemError, V2TransportError } from "@/api/v2/request";
import { queryClient } from "./query-client";

function problem(status: number): V2ProblemError {
  return new V2ProblemError("listProfiles", {
    type: `https://siloserver.org/docs/api/v2/problems/status_${status}`,
    title: `HTTP ${status}`,
    status,
    detail: `The server answered ${status}.`,
    instance: "/api/v2/profiles",
  });
}

const retry = queryClient.getDefaultOptions().queries?.retry as (
  failureCount: number,
  error: unknown,
) => boolean;

describe("query retry policy", () => {
  it("retries a transient failure once", () => {
    expect(retry(0, problem(503))).toBe(true);
    expect(retry(0, new TypeError("Failed to fetch"))).toBe(true);
    expect(retry(1, problem(503))).toBe(false);
  });

  it.each([
    ["a v2 problem", problem(401)],
    ["a v2 problem", problem(403)],
    ["a v2 transport error", new V2TransportError("listProfiles", 401, "not a problem")],
    ["a v1 error", new ApiClientError(403, "forbidden", "Forbidden")],
  ])("does not retry %s that refuses the caller", (_kind, error) => {
    expect(retry(0, error)).toBe(false);
  });

  it("does not retry a 404 problem, which answers the same every time", () => {
    expect(retry(0, problem(404))).toBe(false);
  });

  it("retries a 404 without a problem document, which says nothing about the resource", () => {
    expect(retry(0, new V2TransportError("listProfiles", 404, "not a problem"))).toBe(true);
  });
});
