// @vitest-environment jsdom

import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PolicyDecisionLogTable } from "./PolicyDecisionLogTable";
import {
  installPolicyStorageMocks,
  jsonResponse,
  renderWithPolicyProviders,
} from "./policyTestUtils";

describe("PolicyDecisionLogTable", () => {
  const requests: string[] = [];

  beforeEach(() => {
    requests.length = 0;
    installPolicyStorageMocks();
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) => {
        const url = String(input);
        requests.push(url);
        if (url.includes("/api/v1/admin/policy/decisions?")) {
          if (url.includes("cursor=cursor-1")) {
            return jsonResponse({
              entries: [
                {
                  id: 2,
                  timestamp: "2026-07-02T13:00:00Z",
                  decision_name: "silo.scope.decision",
                  policy_generation: 4,
                  user_id: 42,
                  allowed: false,
                  eval_time_ns: 9100,
                  input_digest: "digest-page-2",
                },
              ],
            });
          }
          return jsonResponse({
            entries: [
              {
                id: 1,
                timestamp: "2026-07-02T12:00:00Z",
                decision_name: "silo.scope.decision",
                policy_generation: 3,
                user_id: 7,
                allowed: true,
                eval_time_ns: 12000,
                input_digest: "digest-page-1",
              },
            ],
            next_cursor: "cursor-1",
          });
        }
        return jsonResponse({ error: "not_found", message: url }, 404);
      }),
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("fetches the next cursor page", async () => {
    renderWithPolicyProviders(<PolicyDecisionLogTable domains={["scope"]} />);

    expect(await screen.findByText("digest-page-1")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Next" }));

    expect(await screen.findByText("digest-page-2")).toBeInTheDocument();
    expect(requests.some((url) => url.includes("cursor=cursor-1"))).toBe(true);
  });

  it("wires user filters into the decision query", async () => {
    renderWithPolicyProviders(<PolicyDecisionLogTable domains={["scope"]} />);

    expect(await screen.findByText("digest-page-1")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("User ID"), { target: { value: "42" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));

    await waitFor(() => {
      expect(requests.some((url) => url.includes("user_id=42"))).toBe(true);
    });
  });
});
