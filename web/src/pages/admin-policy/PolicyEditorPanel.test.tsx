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

import type { PolicySnapshot } from "@/api/adminPolicy";
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

function documentWithLiveV3(): PolicySnapshot {
  return {
    etag: '"revision-2"',
    id: "1",
    domain: "scope",
    name: "Scope limits",
    enabled: true,
    active_version_id: "11",
    active_version: {
      id: "11",
      document_id: "1",
      version_number: 3,
      source_sha256: "def",
      compiled_ok: true,
      compile_error: null,
      created_by_user_id: null,
      comment: null,
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
      if (url === "/api/v2/admin/policy/documents/1") {
        return jsonResponse({
          id: "1",
          domain: "scope",
          name: "Scope limits",
          enabled: true,
          active_version_id: "10",
          active_version: {
            id: "10",
            document_id: "1",
            version_number: 2,
            source_sha256: "abc",
            compiled_ok: true,
            compile_error: null,
            created_by_user_id: null,
            comment: null,
            created_at: "2026-07-02T12:00:00Z",
            source: "package silo_custom.scope\n\nbad if {\n  x\n}\n",
          },
          created_at: "2026-07-02T12:00:00Z",
          updated_at: "2026-07-02T12:00:00Z",
        });
      }
      if (url.startsWith("/api/v2/admin/policy/documents/1/versions?")) {
        return jsonResponse({ items: [], page: { has_more: false } });
      }
      if (url === "/api/v2/admin/policy/validate") {
        return jsonResponse(
          {
            compiled_ok: false,
            errors: [{ row: 3, col: 3, message: "var x is unsafe" }],
          },
          200,
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
    renderWithPolicyProviders(<PolicyEditorPanel documentId="1" domains={["scope"]} />);

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
      expect(fetchMock).toHaveBeenCalledWith("/api/v2/admin/policy/validate", expect.any(Object));
    });
    expect(await screen.findByText(/var x is unsafe/)).toBeInTheDocument();
    expect(screen.getByText(/3:3/)).toBeInTheDocument();
  });

  it("keeps a dirty draft and shows a notice when a newer version goes live elsewhere", async () => {
    const { client } = renderWithPolicyProviders(
      <PolicyEditorPanel documentId="1" domains={["scope"]} />,
    );

    expect(await screen.findByText("Scope limits")).toBeInTheDocument();
    await waitFor(() => expect(regoTextarea().value).toContain("bad if"));

    const myDraft = "package silo_custom.scope\n\nmy_edit if {\n  input\n}\n";
    fireEvent.change(regoTextarea(), { target: { value: myDraft } });

    // Another admin activates v3 — surfaced here as a background query update.
    act(() => {
      client.setQueryData(adminKeys.policyDocument("1"), documentWithLiveV3());
    });

    // The dirty draft is preserved rather than silently reseeded.
    expect(regoTextarea().value).toBe(myDraft);
    expect(await screen.findByText(/The saved policy changed/)).toBeInTheDocument();
  });

  it("adopts the newer live version when the load button is clicked", async () => {
    const { client } = renderWithPolicyProviders(
      <PolicyEditorPanel documentId="1" domains={["scope"]} />,
    );

    expect(await screen.findByText("Scope limits")).toBeInTheDocument();
    await waitFor(() => expect(regoTextarea().value).toContain("bad if"));

    const myDraft = "package silo_custom.scope\n\nmy_edit if {\n  input\n}\n";
    fireEvent.change(regoTextarea(), { target: { value: myDraft } });
    act(() => {
      client.setQueryData(adminKeys.policyDocument("1"), documentWithLiveV3());
    });

    fireEvent.click(await screen.findByRole("button", { name: /load saved source/i }));

    await waitFor(() => expect(regoTextarea().value).toContain("live_three"));
    expect(screen.queryByText(/The saved policy changed/)).not.toBeInTheDocument();
  });

  it("adopts a newer live version automatically when the editor is clean", async () => {
    const { client } = renderWithPolicyProviders(
      <PolicyEditorPanel documentId="1" domains={["scope"]} />,
    );

    expect(await screen.findByText("Scope limits")).toBeInTheDocument();
    await waitFor(() => expect(regoTextarea().value).toBe(LIVE_V2_SOURCE));

    act(() => {
      client.setQueryData(adminKeys.policyDocument("1"), documentWithLiveV3());
    });

    await waitFor(() => expect(regoTextarea().value).toBe(LIVE_V3_SOURCE));
    expect(screen.queryByText(/The saved policy changed/)).not.toBeInTheDocument();
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

function lifecycleServer({ invalid = false, holdSave = false, holdValidate = false } = {}) {
  let revision = 1;
  let activateFails = true;
  let releaseSave: (() => void) | undefined;
  let releaseValidate: (() => void) | undefined;
  const mutationCalls: Array<{ body: Record<string, unknown>; etag: string | null }> = [];
  const saved = {
    id: "95",
    document_id: "1",
    version_number: 3,
    source_sha256: "saved",
    compiled_ok: !invalid,
    compile_error: invalid ? "Unsafe variable" : null,
    created_by_user_id: null,
    comment: null,
    created_at: "2026-09-05T00:00:00.000Z",
    source: "draft one",
  };
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/validate")) {
      if (holdValidate)
        await new Promise<void>((resolve) => {
          releaseValidate = resolve;
        });
      return jsonResponse({ compiled_ok: true, errors: [] });
    }
    if (url.endsWith("/versions") && init?.method === "POST") {
      if (holdSave)
        await new Promise<void>((resolve) => {
          releaseSave = resolve;
        });
      revision = 2;
      return jsonResponse(saved, 201);
    }
    if (url.endsWith("/active-version")) {
      mutationCalls.push({
        body: JSON.parse(String(init?.body)),
        etag: new Headers(init?.headers).get("If-Match"),
      });
      if (activateFails)
        return jsonResponse(
          {
            type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
            title: "Precondition failed",
            status: 412,
            detail: "Changed",
          },
          412,
          '"revision-3"',
        );
      return jsonResponse({
        persisted: true,
        persisted_generation: 8,
        document: { ...documentWithLiveV3(), active_version_id: "95" },
        application: { local_applied: false, loaded_generation: 7, publication_failed: true },
      });
    }
    if (url.includes("/versions?")) return jsonResponse({ items: [], page: { has_more: false } });
    if (url.endsWith("/documents/1"))
      return jsonResponse(documentWithLiveV3(), 200, `"revision-${revision}"`);
    throw new Error(url);
  });
  vi.stubGlobal("fetch", fetchMock);
  return {
    fetchMock,
    mutationCalls,
    newer: () => {
      revision = 3;
    },
    succeed: () => {
      activateFails = false;
    },
    releaseSave: () => releaseSave?.(),
    releaseValidate: () => releaseValidate?.(),
  };
}
async function prepareSavedDraft() {
  await screen.findByText("Scope limits");
  fireEvent.change(regoTextarea(), { target: { value: "draft one" } });
  fireEvent.click(screen.getByRole("button", { name: /^Validate draft$/ }));
  fireEvent.click(await screen.findByRole("button", { name: "Save as version" }));
}
async function adoptCurrentRevision() {
  fireEvent.click(screen.getByRole("button", { name: "Review current policy" }));
  fireEvent.click(
    await screen.findByRole("button", { name: "Use this revision and keep my draft" }),
  );
}
describe("policy lifecycle revision ownership", () => {
  beforeEach(() => installPolicyStorageMocks());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });
  it("requires explicit post-save adoption, keeps the captured guard after refetch and412, and reports persisted reload failure", async () => {
    const server = lifecycleServer();
    const { client } = renderWithPolicyProviders(
      <PolicyEditorPanel documentId="1" domains={["scope"]} />,
    );
    await prepareSavedDraft();
    expect(await screen.findByText(/Saved v3\. Review/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Activate v3" })).toBeDisabled();
    await adoptCurrentRevision();
    server.newer();
    act(() =>
      client.setQueryData(adminKeys.policyDocument("1"), {
        ...documentWithLiveV3(),
        etag: '"revision-3"',
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Activate v3" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm activation" }));
    expect(
      await screen.findByText(/Your draft and original revision are kept/),
    ).toBeInTheDocument();
    expect(regoTextarea().value).toBe("draft one");
    expect(server.mutationCalls).toEqual([{ body: { version_id: "95" }, etag: '"revision-2"' }]);
    await adoptCurrentRevision();
    expect(server.mutationCalls).toHaveLength(1);
    server.succeed();
    fireEvent.click(screen.getByRole("button", { name: "Activate v3" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm activation" }));
    expect(
      await screen.findByText(/Policy change saved at generation 8.*could not reload/),
    ).toBeInTheDocument();
    expect(regoTextarea().value).toBe("draft one");
    expect(server.mutationCalls[1]?.etag).toBe('"revision-3"');
    expect(server.mutationCalls).toHaveLength(2);
  });
  it("preserves a compile-invalid saved draft without treating it as rejected or activating it", async () => {
    const server = lifecycleServer({ invalid: true });
    renderWithPolicyProviders(<PolicyEditorPanel documentId="1" domains={["scope"]} />);
    await prepareSavedDraft();
    expect(await screen.findByText(/Saved v3, but it did not compile/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Activate v3" })).not.toBeInTheDocument();
    expect(regoTextarea().value).toBe("draft one");
    expect(
      server.fetchMock.mock.calls.filter(
        ([url, init]) => String(url).endsWith("/versions") && init?.method === "POST",
      ),
    ).toHaveLength(1);
  });
  it("does not label later edits as the source saved by an in-flight append", async () => {
    const server = lifecycleServer({ holdSave: true });
    renderWithPolicyProviders(<PolicyEditorPanel documentId="1" domains={["scope"]} />);
    await prepareSavedDraft();
    await waitFor(() =>
      expect(
        server.fetchMock.mock.calls.some(
          ([url, init]) => String(url).endsWith("/versions") && init?.method === "POST",
        ),
      ).toBe(true),
    );
    fireEvent.change(regoTextarea(), { target: { value: "later unsaved edit" } });
    act(() => server.releaseSave());
    expect(await screen.findByText(/Saved v3\. Review/)).toBeInTheDocument();
    expect(regoTextarea().value).toBe("later unsaved edit");
    expect(screen.queryByRole("button", { name: "Activate v3" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Validate draft" })).toBeInTheDocument();
  });
  it("does not label later edits as validated by an earlier in-flight request", async () => {
    const server = lifecycleServer({ holdValidate: true });
    renderWithPolicyProviders(<PolicyEditorPanel documentId="1" domains={["scope"]} />);
    await screen.findByText("Scope limits");
    fireEvent.change(regoTextarea(), { target: { value: "draft one" } });
    fireEvent.click(screen.getByRole("button", { name: "Validate draft" }));
    await waitFor(() =>
      expect(server.fetchMock.mock.calls.some(([url]) => String(url).endsWith("/validate"))).toBe(
        true,
      ),
    );
    fireEvent.change(regoTextarea(), { target: { value: "later edit" } });
    act(() => server.releaseValidate());
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Validate draft" })).toBeEnabled(),
    );
    expect(screen.queryByRole("button", { name: "Save as version" })).not.toBeInTheDocument();
  });
});
