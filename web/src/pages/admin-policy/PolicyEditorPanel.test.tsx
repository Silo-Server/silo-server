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

import type { PolicyDocument } from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";
import { mapPolicyIssuesToDiagnostics } from "@/lib/policyDiagnostics";

import { PolicyEditorPanel } from "./PolicyEditorPanel";
import {
  installPolicyStorageMocks,
  jsonResponse,
  renderWithPolicyProviders,
} from "./policyTestUtils";

const LIVE_V2_SOURCE = "package silo_custom.scope\n\nbad if {\n  x\n}\n";
const LIVE_V3_SOURCE = "package silo_custom.scope\n\nlive_three if {\n  input\n}\n";

function documentWithLiveV3(): PolicyDocument {
  return {
    id: 1,
    domain: "scope",
    name: "Scope limits",
    enabled: true,
    active_version_id: 11,
    active_version: {
      id: 11,
      document_id: 1,
      version_number: 3,
      source_sha256: "def",
      compiled_ok: true,
      created_at: "2026-07-02T13:00:00Z",
      source: LIVE_V3_SOURCE,
    },
    created_at: "2026-07-02T12:00:00Z",
    updated_at: "2026-07-02T13:00:00Z",
  };
}

function regoTextarea() {
  return screen.getByLabelText("Rego policy source") as HTMLTextAreaElement;
}

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

  it("keeps a dirty draft and shows a notice when a newer version goes live elsewhere", async () => {
    const { client } = renderWithPolicyProviders(
      <PolicyEditorPanel documentId={1} domains={["scope"]} />,
    );

    expect(await screen.findByText("Scope limits")).toBeInTheDocument();
    await waitFor(() => expect(regoTextarea().value).toContain("bad if"));

    const myDraft = "package silo_custom.scope\n\nmy_edit if {\n  input\n}\n";
    fireEvent.change(regoTextarea(), { target: { value: myDraft } });

    // Another admin activates v3 — surfaced here as a background query update.
    act(() => {
      client.setQueryData(adminKeys.policyDocument(1), documentWithLiveV3());
    });

    // The dirty draft is preserved rather than silently reseeded.
    expect(regoTextarea().value).toBe(myDraft);
    expect(await screen.findByText(/Version 3 is now live elsewhere/)).toBeInTheDocument();
  });

  it("adopts the newer live version when the load button is clicked", async () => {
    const { client } = renderWithPolicyProviders(
      <PolicyEditorPanel documentId={1} domains={["scope"]} />,
    );

    expect(await screen.findByText("Scope limits")).toBeInTheDocument();
    await waitFor(() => expect(regoTextarea().value).toContain("bad if"));

    const myDraft = "package silo_custom.scope\n\nmy_edit if {\n  input\n}\n";
    fireEvent.change(regoTextarea(), { target: { value: myDraft } });
    act(() => {
      client.setQueryData(adminKeys.policyDocument(1), documentWithLiveV3());
    });

    fireEvent.click(await screen.findByRole("button", { name: /load live version/i }));

    await waitFor(() => expect(regoTextarea().value).toContain("live_three"));
    expect(screen.queryByText(/now live elsewhere/)).not.toBeInTheDocument();
  });

  it("adopts a newer live version automatically when the editor is clean", async () => {
    const { client } = renderWithPolicyProviders(
      <PolicyEditorPanel documentId={1} domains={["scope"]} />,
    );

    expect(await screen.findByText("Scope limits")).toBeInTheDocument();
    await waitFor(() => expect(regoTextarea().value).toBe(LIVE_V2_SOURCE));

    act(() => {
      client.setQueryData(adminKeys.policyDocument(1), documentWithLiveV3());
    });

    await waitFor(() => expect(regoTextarea().value).toBe(LIVE_V3_SOURCE));
    expect(screen.queryByText(/now live elsewhere/)).not.toBeInTheDocument();
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
