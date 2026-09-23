import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { installPolicyStorageMocks, jsonResponse } from "./admin-policy/policyTestUtils";
import AdminAccessGroups from "./AdminAccessGroups";

vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({}) }));
const GROUP = {
  id: "1",
  name: "Kids",
  description: "",
  library_ids: ["2"],
  max_playback_quality: "1080p",
  download_allowed: false,
  download_transcode_allowed: false,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 1,
  max_transcodes: 0,
  max_remote_stream_bitrate_kbps: 0,
  max_local_stream_bitrate_kbps: 0,
  allowed_permissions: [] as string[],
  requests_allowed: false,
  is_default: true,
  member_count: 3,
  created_at: "2026-07-02T12:00:00Z",
  updated_at: "2026-07-02T12:00:00Z",
};

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AdminAccessGroups />
    </QueryClientProvider>,
  );
}

describe("AdminAccessGroups", () => {
  let putBody: unknown;

  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("account");
    setProfileId("owner");
    setProfileToken(null);
    putBody = undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (url === "/api/v2/admin/users/capabilities")
          return jsonResponse({ access_groups: true });
        if (url === "/api/v2/admin/access-groups?limit=200" && method === "GET") {
          return jsonResponse({ items: [GROUP], page: { has_more: false } });
        }
        if (url === "/api/v2/admin/access-groups/1" && method === "GET") {
          return new Response(JSON.stringify(GROUP), {
            headers: { "Content-Type": "application/json", ETag: '"initial"' },
          });
        }
        if (url === "/api/v1/admin/libraries") {
          return jsonResponse([
            { id: 2, name: "Movies", type: "movie", enabled: true },
            { id: 3, name: "Anime", type: "series", enabled: true },
          ]);
        }
        if (url === "/api/v2/admin/access-groups/1" && method === "PUT") {
          putBody = JSON.parse(String(init?.body));
          return new Response(JSON.stringify({ ...GROUP, download_allowed: true }), {
            headers: { "Content-Type": "application/json", ETag: '"saved"' },
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

  it("summarizes a group and saves edited restrictions", async () => {
    renderPage();

    expect(await screen.findByText("Kids")).toBeInTheDocument();
    expect(screen.getByText("3 members")).toBeInTheDocument();
    // Card facts reflect the restriction shape; default groups are labeled.
    expect(screen.getByText("No downloads")).toBeInTheDocument();
    expect(screen.getByText("Default")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Kids/ }));

    // Drill-in editor seeds from the group; toggle downloads on and save.
    fireEvent.click(await screen.findByRole("switch", { name: "Allow downloads" }));
    fireEvent.change(screen.getByLabelText("Max remote stream bitrate (kbps)"), {
      target: { value: "4000" },
    });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    await waitFor(() => {
      expect(putBody).toMatchObject({
        name: "Kids",
        library_ids: ["2"],
        download_allowed: true,
        max_streams: 1,
        max_remote_stream_bitrate_kbps: 4000,
        requests_allowed: false,
        allowed_permissions: [],
        is_default: true,
      });
    });
  });

  it("locks demotion and deletion for the default group", async () => {
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));

    // The server rejects demoting or deleting the default group, so the
    // editor disables both paths and explains the promote-another-group flow.
    expect(await screen.findByRole("switch", { name: "Default for new users" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /delete group/i })).toBeDisabled();
    expect(screen.getByText(/make another group the default first/i)).toBeInTheDocument();
  });

  it("does not truncate a fractional remote bitrate limit", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    const limit = await screen.findByLabelText("Max remote stream bitrate (kbps)");
    fireEvent.change(limit, { target: { value: "1.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => {
      expect(putBody).toMatchObject({ max_remote_stream_bitrate_kbps: 0 });
    });
  });

  it("keeps an existing limit when the number input has invalid intermediate text", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    const limit = await screen.findByLabelText("Max remote stream bitrate (kbps)");
    fireEvent.change(limit, { target: { value: "4000" } });
    Object.defineProperty(limit, "validity", { value: { badInput: true }, configurable: true });
    fireEvent.change(limit, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => {
      expect(putBody).toMatchObject({ max_remote_stream_bitrate_kbps: 4000 });
    });
  });
});

it("keeps a stale draft and requires explicit canonical reload before resubmission", async () => {
  installPolicyStorageMocks();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  let reads = 0;
  const tags: (string | null)[] = [];
  const bodies: Record<string, unknown>[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url === "/api/v2/admin/users/capabilities") return jsonResponse({ access_groups: true });
      if (url.includes("/admin/access-groups?"))
        return jsonResponse({ items: [GROUP], page: { has_more: false } });
      if (url === "/api/v2/admin/access-groups/1" && method === "GET") {
        reads++;
        return new Response(
          JSON.stringify({ ...GROUP, name: reads === 1 ? "Canonical" : "Someone else's name" }),
          {
            headers: {
              "Content-Type": "application/json",
              ETag: reads === 1 ? '"old"' : '"fresh"',
            },
          },
        );
      }
      if (method === "PUT") {
        tags.push(new Headers(init?.headers).get("If-Match"));
        bodies.push(JSON.parse(String(init?.body)));
        if (tags.length === 1)
          return new Response(
            JSON.stringify({
              type: "https://silo.example/problems/precondition_failed",
              title: "Changed",
              status: 412,
              detail: "Reload current state",
            }),
            {
              status: 412,
              headers: { "Content-Type": "application/problem+json", ETag: '"must-not-adopt"' },
            },
          );
        return new Response(JSON.stringify(GROUP), {
          headers: { "Content-Type": "application/json", ETag: '"saved"' },
        });
      }
      if (url.includes("libraries")) return jsonResponse([]);
      return jsonResponse({}, 404);
    }),
  );
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
  const name = await screen.findByLabelText("Name");
  expect(name).toHaveValue("Canonical");
  fireEvent.change(name, { target: { value: "My draft" } });
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await screen.findByText(/Your draft is preserved/);
  expect(name).toHaveValue("My draft");
  expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  expect(reads).toBe(1);
  fireEvent.click(screen.getByRole("button", { name: "Reload current group" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled());
  expect(name).toHaveValue("My draft");
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(tags).toEqual(['"old"', '"fresh"']));
  expect(bodies.map((body) => body.name)).toEqual(["My draft", "My draft"]);
  cleanup();
  vi.unstubAllGlobals();
});

it("keeps delete confirmation after conflict and reloads before retry", async () => {
  installPolicyStorageMocks();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  let reads = 0;
  const tags: (string | null)[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url === "/api/v2/admin/users/capabilities") return jsonResponse({ access_groups: true });
      if (url.includes("/admin/access-groups?"))
        return jsonResponse({ items: [GROUP], page: { has_more: false } });
      if (url === "/api/v2/admin/access-groups/1" && method === "GET") {
        reads++;
        return new Response(JSON.stringify({ ...GROUP, is_default: false }), {
          headers: { "Content-Type": "application/json", ETag: reads === 1 ? '"old"' : '"fresh"' },
        });
      }
      if (method === "DELETE") {
        tags.push(new Headers(init?.headers).get("If-Match"));
        if (tags.length === 1)
          return new Response(
            JSON.stringify({
              type: "https://silo.example/problems/precondition_failed",
              title: "Changed",
              status: 412,
              detail: "Reload current state",
            }),
            { status: 412, headers: { "Content-Type": "application/problem+json" } },
          );
        return new Response(null, { status: 204 });
      }
      return jsonResponse([]);
    }),
  );
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
  fireEvent.click(await screen.findByRole("button", { name: "Delete group" }));
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled());
  expect(screen.getByRole("alertdialog")).toBeInTheDocument();
  expect(reads).toBe(1);
  fireEvent.click(screen.getByRole("button", { name: "Reload current group" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  expect(tags).toEqual(['"old"', '"fresh"']);
  cleanup();
  vi.unstubAllGlobals();
});

it("blocks configuration controls when capability is unavailable", async () => {
  installPolicyStorageMocks();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) =>
    String(input).includes("capabilities")
      ? jsonResponse({ access_groups: false })
      : jsonResponse({ items: [GROUP], page: { has_more: false } }),
  );
  vi.stubGlobal("fetch", fetch);
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
  expect(screen.getByText("Access group editing is unavailable.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "New group" })).not.toBeInTheDocument();
  expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
  expect(fetch.mock.calls.every(([url]) => !String(url).endsWith("/1"))).toBe(true);
  cleanup();
  vi.unstubAllGlobals();
});
