// @vitest-environment jsdom
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("@uiw/react-codemirror", () => ({
  default: ({ value }: { value: string }) => <textarea readOnly value={value} />,
}));
import { PolicyDocumentList } from "./PolicyDocumentList";
import {
  installPolicyStorageMocks,
  jsonResponse,
  renderWithPolicyProviders,
} from "./policyTestUtils";
const doc = {
  id: "1",
  domain: "scope",
  name: "Scope limits",
  enabled: false,
  active_version_id: null,
  created_at: "2026-07-02T12:00:00.000Z",
  updated_at: "2026-07-02T12:00:00.000Z",
};
describe("PolicyDocumentList", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        const url = String(input);
        if (url.includes("/documents?"))
          return jsonResponse({
            items: url.includes("cursor=") ? [{ ...doc, id: "99", name: "Older override" }] : [doc],
            page: {
              has_more: !url.includes("cursor="),
              next_cursor: url.includes("cursor=") ? undefined : "next",
            },
          });
        if (url.endsWith("/documents/1") && init?.method === "PATCH") {
          expect(new Headers(init.headers).get("If-Match")).toBe('"revision-1"');
          return jsonResponse(
            {
              type: "https://siloserver.org/docs/api/v2/problems/conflict",
              title: "Conflict",
              status: 409,
              detail: "Another override is enabled.",
            },
            409,
          );
        }
        if (url.endsWith("/documents/1")) return jsonResponse(doc);
        if (url.endsWith("/documents/99"))
          return jsonResponse({ ...doc, id: "99", name: "Off-page override" });
        if (url.includes("/versions"))
          return jsonResponse({ items: [], page: { has_more: false } });
        throw new Error(url);
      }),
    );
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });
  it("reviews a canonical revision before enabling and preserves conflicts", async () => {
    renderWithPolicyProviders(<PolicyDocumentList domains={["scope"]} />);
    fireEvent.click(await screen.findByRole("button", { name: "Enable override" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm enable" }));
    expect(await screen.findByText("Another override is enabled.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Confirm enable" })).toBeInTheDocument();
  });
  it("loads additional documents only on request", async () => {
    renderWithPolicyProviders(<PolicyDocumentList domains={["scope"]} />);
    expect(await screen.findByText("Scope limits")).toBeInTheDocument();
    expect(screen.queryByText("Older override")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more overrides" }));
    expect(await screen.findByText("Older override")).toBeInTheDocument();
  });
  it("opens a canonical document outside the first list page", async () => {
    renderWithPolicyProviders(<PolicyDocumentList domains={["scope"]} />, ["/?document=99"]);
    expect(await screen.findByRole("heading", { name: "Off-page override" })).toBeInTheDocument();
  });
});
