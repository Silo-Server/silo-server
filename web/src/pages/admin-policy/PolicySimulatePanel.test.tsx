// @vitest-environment jsdom

import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PolicySimulatePanel } from "./PolicySimulatePanel";
import {
  installPolicyStorageMocks,
  jsonResponse,
  renderWithPolicyProviders,
} from "./policyTestUtils";

describe("PolicySimulatePanel", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        expect(String(input)).toBe("/api/v1/admin/policy/simulate");
        expect(JSON.parse(String(init?.body))).toMatchObject({
          domain: "scope",
          source: "package silo_custom.scope",
        });
        return jsonResponse({
          decision: {
            schema_version: 1,
            unrestricted: false,
            allowed_library_ids: [1, 2],
            max_content_rating: "PG",
          },
          eval_time_ns: 14200,
          generation: 8,
        });
      }),
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("renders the simulated decision and eval time", async () => {
    renderWithPolicyProviders(
      <PolicySimulatePanel domains={["scope"]} domain="scope" source="package silo_custom.scope" />,
    );

    fireEvent.click(screen.getByRole("button", { name: /run/i }));

    expect(await screen.findByText(/14.2 µs/)).toBeInTheDocument();
    expect(screen.getAllByText(/allowed_library_ids/).length).toBeGreaterThan(0);
    expect(screen.getByText(/rating ≤ PG/)).toBeInTheDocument();
  });
});
