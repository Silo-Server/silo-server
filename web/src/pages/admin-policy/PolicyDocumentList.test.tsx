// @vitest-environment jsdom

import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PolicyDocumentList } from "./PolicyDocumentList";
import {
  installPolicyStorageMocks,
  jsonResponse,
  renderWithPolicyProviders,
} from "./policyTestUtils";

describe("PolicyDocumentList", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (url === "/api/v1/admin/policy/documents" && method === "GET") {
          return jsonResponse([
            {
              id: 1,
              domain: "scope",
              name: "Scope limits",
              enabled: false,
              created_at: "2026-07-02T12:00:00Z",
              updated_at: "2026-07-02T12:00:00Z",
            },
          ]);
        }
        if (url === "/api/v1/admin/policy/documents/1/enabled" && method === "POST") {
          return jsonResponse(
            {
              error: "conflict",
              message: "Policy domain already has an enabled document",
            },
            409,
          );
        }
        return jsonResponse({ error: "not_found", message: url }, 404);
      }),
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("surfaces a 409 conflict when enabling a document fails", async () => {
    renderWithPolicyProviders(<PolicyDocumentList domains={["scope"]} />);

    expect(await screen.findByText("Scope limits")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("switch", { name: "Set Scope limits enabled" }));

    expect(
      await screen.findByText("Policy domain already has an enabled document"),
    ).toBeInTheDocument();
  });
});
