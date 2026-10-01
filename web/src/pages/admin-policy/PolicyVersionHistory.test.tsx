// @vitest-environment jsdom
import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.mock("@uiw/react-codemirror", () => ({
  default: ({ value }: { value: string }) => (
    <textarea readOnly aria-label="Historical source" value={value} />
  ),
}));
import { PolicyVersionHistory } from "./PolicyVersionHistory";
import { adminKeys } from "@/hooks/queries/keys";
import {
  installPolicyStorageMocks,
  jsonResponse,
  renderWithPolicyProviders,
} from "./policyTestUtils";
const doc = {
  id: "1",
  domain: "scope",
  name: "Scope",
  enabled: true,
  active_version_id: "90",
  created_at: "2026-09-05T00:00:00.000Z",
  updated_at: "2026-09-05T00:00:00.000Z",
};
const version = (id: string, n: number) => ({
  id,
  document_id: "1",
  version_number: n,
  source_sha256: id,
  compiled_ok: true,
  compile_error: null,
  comment: null,
  created_by_user_id: null,
  created_at: doc.created_at,
});
beforeEach(installPolicyStorageMocks);
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("loads older pages explicitly and activates the saved ID with its captured canonical guard", async () => {
  const writes: Array<{ id: string; tag: string | null }> = [];
  const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/documents/1")) return jsonResponse(doc, 200, '"original"');
    if (url.includes("/versions?"))
      return jsonResponse({
        items: url.includes("cursor=") ? [version("97", 1)] : [version("99", 2)],
        page: {
          has_more: !url.includes("cursor="),
          next_cursor: url.includes("cursor=") ? undefined : "older",
        },
      });
    if (url.endsWith("/versions/99"))
      return jsonResponse({ ...version("99", 2), source: "source99" });
    if (url.endsWith("/versions/97"))
      return jsonResponse({ ...version("97", 1), source: "source97" });
    if (url.endsWith("/active-version")) {
      writes.push({
        id: JSON.parse(String(init?.body)).version_id,
        tag: new Headers(init?.headers).get("If-Match"),
      });
      return jsonResponse({
        persisted: true,
        persisted_generation: 9,
        document: { ...doc, active_version_id: "97" },
        application: { local_applied: true, loaded_generation: 9, publication_failed: true },
      });
    }
    throw new Error(url);
  });
  vi.stubGlobal("fetch", fetchMock);
  const { client } = renderWithPolicyProviders(
    <PolicyVersionHistory documentId="1" activeVersionId="90" />,
  );
  expect(await screen.findByText("v2")).toBeInTheDocument();
  expect(screen.queryByText("v1")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Load older versions" }));
  const older = await screen.findByText("v1");
  fireEvent.click(older);
  expect(await screen.findByDisplayValue("source97")).toBeInTheDocument();
  fireEvent.click(older.closest("tr")!.querySelector("button")!);
  act(() => client.setQueryData(adminKeys.policyDocument("1"), { ...doc, etag: '"newer"' }));
  fireEvent.click(await screen.findByRole("button", { name: "Activate" }));
  await waitFor(() => expect(writes).toEqual([{ id: "97", tag: '"original"' }]));
  expect(await screen.findByText(/notification failed/)).toBeInTheDocument();
});
