// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { FileVersion } from "@/api/types";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import getItemOk from "../../../../../contracts/api/v2/fixtures/get_admin_item_trickplay_ok.json";
import regenerateAccepted from "../../../../../contracts/api/v2/fixtures/regenerate_admin_item_trickplay_accepted.json";

import { TrickplayStatusDialog } from "./TrickplayStatusDialog";

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast }));

beforeEach(installPolicyStorageMocks);
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  toast.success.mockClear();
});

function renderDialog(respond: (method: string, path: string) => Response) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const path = decodeURIComponent(new URL(String(input), "http://localhost").pathname);
      calls.push(`${method} ${path}`);
      return respond(method, path);
    }),
  );
  const client = new QueryClient();
  const versions = [{ file_id: 42, file_name: "Heat (1995) Remux.mkv" }] as FileVersion[];
  render(
    <QueryClientProvider client={client}>
      <TrickplayStatusDialog
        open
        onOpenChange={() => {}}
        itemId="movie:heat-1995"
        versions={versions}
      />
    </QueryClientProvider>,
  );
  return calls;
}

describe("TrickplayStatusDialog", () => {
  it("queues the item's previews again", async () => {
    const calls = renderDialog((method) =>
      method === "POST" ? jsonResponse(regenerateAccepted, 202) : jsonResponse(getItemOk),
    );
    expect(await screen.findByText("Heat (1995) Remux.mkv")).toBeTruthy();
    expect(screen.getByText("Ready")).toBeTruthy();
    expect(screen.getByText(/^720 previews · 300 px every 10 s · 2\.0 MB · made /)).toBeTruthy();
    // No version names file 43, so it shows its id.
    expect(screen.getByText("File 43")).toBeTruthy();
    expect(screen.getByText("Failed")).toBeTruthy();
    expect(screen.getByText("ffmpeg sampling failed (invalid_data)")).toBeTruthy();

    const button = await screen.findByRole("button", { name: /Make Again/ });
    await waitFor(() => expect(button.hasAttribute("disabled")).toBe(false));

    fireEvent.click(button);

    await waitFor(() =>
      expect(calls).toContain("POST /api/v2/admin/items/movie:heat-1995/trickplay/regenerate"),
    );
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        `Seek previews queued for ${regenerateAccepted.requeued} ${
          regenerateAccepted.requeued === 1 ? "file" : "files"
        }`,
      ),
    );
  });

  it("points to the library setting when previews are off", async () => {
    renderDialog(() =>
      jsonResponse({ files: [{ file_id: "42", state: "off", servable: false, failures: 0 }] }),
    );

    expect(await screen.findByText("Off for this library")).toBeTruthy();
    expect(screen.getByText(/Turn on Generate seek previews/)).toBeTruthy();
    expect(screen.getByRole("button", { name: /Make Again/ }).hasAttribute("disabled")).toBe(true);
  });
});

it("reports zero regenerated files without claiming work is running", async () => {
  renderDialog((method) =>
    method === "POST"
      ? jsonResponse({ requeued: 0 }, 202)
      : jsonResponse({
          files: [{ file_id: "42", state: "unusable", servable: false, failures: 0 }],
        }),
  );
  const button = await screen.findByRole("button", { name: /Make Again/ });
  await waitFor(() => expect(button.hasAttribute("disabled")).toBe(false));
  fireEvent.click(button);
  await waitFor(() => expect(toast.success).toHaveBeenCalledWith("No seek previews were queued"));
});
