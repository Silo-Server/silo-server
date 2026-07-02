// @vitest-environment jsdom

import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

interface MockCodeMirrorProps {
  value: string;
  onChange?: (value: string) => void;
  "aria-label"?: string;
}

vi.mock("@uiw/react-codemirror", () => ({
  default: ({ value, onChange, "aria-label": ariaLabel }: MockCodeMirrorProps) => (
    <textarea
      aria-label={ariaLabel ?? "Rego policy source"}
      value={value}
      onChange={(event) => onChange?.(event.target.value)}
    />
  ),
}));

import { mapPolicyIssuesToDiagnostics } from "@/lib/policyDiagnostics";

import { PolicyEditorPanel } from "./PolicyEditorPanel";
import {
  installPolicyStorageMocks,
  jsonResponse,
  renderWithPolicyProviders,
} from "./policyTestUtils";

describe("PolicyEditorPanel", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    installPolicyStorageMocks();
    fetchMock = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      if (url === "/api/v1/admin/policy/documents/1") {
        return jsonResponse({
          id: 1,
          domain: "scope",
          name: "Scope limits",
          enabled: true,
          active_version_id: 10,
          active_version: {
            id: 10,
            document_id: 1,
            version_number: 2,
            source_sha256: "abc",
            compiled_ok: true,
            created_at: "2026-07-02T12:00:00Z",
            source: "package silo_custom.scope\n\nbad if {\n  x\n}\n",
          },
          created_at: "2026-07-02T12:00:00Z",
          updated_at: "2026-07-02T12:00:00Z",
        });
      }
      if (url === "/api/v1/admin/policy/documents/1/versions") {
        return jsonResponse([]);
      }
      if (url === "/api/v1/admin/policy/validate") {
        return jsonResponse(
          {
            errors: [{ row: 3, col: 3, message: "var x is unsafe" }],
          },
          422,
        );
      }
      return jsonResponse({ error: "not_found", message: url }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("renders compile issues from a validate error response", async () => {
    renderWithPolicyProviders(<PolicyEditorPanel documentId={1} domains={["scope"]} />);

    expect(await screen.findByText("Scope limits")).toBeInTheDocument();
    await waitFor(() => {
      expect((screen.getByLabelText("Rego policy source") as HTMLTextAreaElement).value).toContain(
        "bad if",
      );
    });

    // The unedited live source shows no actions; editing starts a new draft
    // and surfaces the Validate step.
    expect(screen.queryByRole("button", { name: /validate/i })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Rego policy source"), {
      target: { value: "package silo_custom.scope\n\nbad if {\n  y\n}\n" },
    });

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: /validate/i }));
      await Promise.resolve();
    });

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith("/api/v1/admin/policy/validate", expect.any(Object));
    });
    expect(await screen.findByText(/var x is unsafe/)).toBeInTheDocument();
    expect(screen.getByText(/3:3/)).toBeInTheDocument();
  });

  it("maps compile issues to clamped CodeMirror diagnostics", () => {
    const diagnostics = mapPolicyIssuesToDiagnostics("package x\nallow if {\n  true\n}", [
      { row: 2, col: 1, message: "expected expression" },
      { row: 200, col: 200, message: "out of range" },
    ]);

    expect(diagnostics).toHaveLength(2);
    expect(diagnostics[0]).toMatchObject({
      severity: "error",
      message: "expected expression",
    });
    expect(diagnostics[0]!.from).toBeGreaterThan(0);
    expect(diagnostics[1]!.from).toBeLessThanOrEqual("package x\nallow if {\n  true\n}".length);
  });
});
