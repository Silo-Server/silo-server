import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";
import type { StreamNode } from "@/api/types";
import AdminNodes from "./AdminNodes";

vi.mock("@/pages/admin-settings/NodeRoutingSettings", () => ({
  NodeRoutingSettings: () => null,
}));
vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: { "server.public_url": "https://media.example.test/" } }),
}));

const KEY = "snk1_17_syntheticsecret";

function streamNode(overrides: Partial<StreamNode>): StreamNode {
  return {
    id: "17",
    name: "Transcoder",
    type: "transcode",
    url: "http://transcoder.invalid",
    enabled: true,
    healthy: true,
    active_jobs: 0,
    group: null,
    max_jobs: null,
    max_bandwidth_kbps: null,
    egress_kbps: 0,
    last_health_check: null,
    created_at: "2026-09-06T00:00:00Z",
    key_issued: false,
    ...overrides,
  };
}
const proxy = streamNode({ id: "3", name: "Relay", type: "proxy", url: "http://relay.invalid" });

const json = (body: unknown, status = 200, type = "application/json") =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": type } });

/** Serves the node list and capabilities; `issue` answers POST .../key. */
function serve({
  nodes,
  nodeKeys = true,
  issue = () => json({ node_key: KEY }, 201),
}: {
  nodes: StreamNode[];
  nodeKeys?: boolean;
  issue?: () => Response;
}) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((input: unknown, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      calls.push(`${method} ${new URL(url, "http://x").pathname}`);
      if (url.includes("/admin/nodes/capabilities"))
        return Promise.resolve(
          json({
            state: "available",
            allowed: true,
            revision: "r",
            drain: true,
            summary: true,
            node_keys: nodeKeys,
          }),
        );
      if (method === "POST" && url.includes("/key")) return Promise.resolve(issue());
      return Promise.resolve(json({ items: nodes, page: { has_more: false } }));
    }),
  );
  return calls;
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <AdminNodes />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic");
  setRefreshToken("synthetic-refresh");
  setProfileId("a");
  setProfileToken("pin-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("offers no key controls without the node_keys capability", async () => {
  const calls = serve({ nodes: [streamNode({})], nodeKeys: false });
  renderPage();
  // The status still shows: it is a fact about the node, not an action.
  expect(await screen.findByText(/this node takes no work/)).toBeInTheDocument();
  await waitFor(() => expect(calls).toContain("GET /api/v2/admin/nodes/capabilities"));
  await act(async () => {});
  expect(screen.queryByRole("button", { name: /key for/ })).not.toBeInTheDocument();
});

it("shows no key status or control on a proxy", async () => {
  serve({ nodes: [streamNode({}), proxy] });
  renderPage();
  // The transcode node's button proves the capability loaded.
  await screen.findByRole("button", { name: "Issue a key for Transcoder" });
  expect(screen.getAllByRole("button", { name: /key for/ })).toHaveLength(1);
  expect(screen.getAllByText(/Key issued|No key/)).toHaveLength(1);
});

it("shows an issued key once, with the node settings, and drops it on close", async () => {
  const calls = serve({ nodes: [streamNode({})] });
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: "Issue a key for Transcoder" }));
  const offer = await screen.findByRole("dialog");
  const listReads = calls.filter((c) => c === "GET /api/v2/admin/nodes").length;
  fireEvent.click(within(offer).getByRole("button", { name: "Issue key" }));

  const settings = await screen.findByText(/SILO_NODE_KEY=/);
  expect(settings.textContent).toBe(
    `SILO_CORE_URL=https://media.example.test\nSILO_NODE_KEY=${KEY}`,
  );
  expect(screen.getAllByText(KEY, { exact: false })).toHaveLength(2);
  expect(calls.filter((c) => c === "POST /api/v2/admin/nodes/17/key")).toHaveLength(1);
  // The list refreshes so key_issued updates.
  await waitFor(() =>
    expect(calls.filter((c) => c === "GET /api/v2/admin/nodes").length).toBeGreaterThan(listReads),
  );

  fireEvent.click(screen.getByRole("button", { name: "I saved the key" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(screen.queryByText(KEY, { exact: false })).not.toBeInTheDocument();

  // Opening the dialog again starts over; the old key is not kept anywhere.
  fireEvent.click(screen.getByRole("button", { name: "Issue a key for Transcoder" }));
  expect(await screen.findByRole("dialog")).not.toHaveTextContent(KEY);
});

it("rotates a key only after confirmation", async () => {
  const calls = serve({ nodes: [streamNode({ key_issued: true })] });
  renderPage();
  const rotate = await screen.findByRole("button", { name: "Rotate the key for Transcoder" });

  fireEvent.click(rotate);
  let dialog = await screen.findByRole("dialog");
  expect(dialog).toHaveTextContent(/stops working immediately/);
  expect(dialog).toHaveTextContent(/drain the node first/);
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(calls.some((c) => c.startsWith("POST"))).toBe(false);

  fireEvent.click(rotate);
  dialog = await screen.findByRole("dialog");
  fireEvent.click(within(dialog).getByRole("button", { name: "Rotate key" }));
  expect(await screen.findByText(/SILO_NODE_KEY=/)).toBeInTheDocument();
  expect(calls.filter((c) => c === "POST /api/v2/admin/nodes/17/key")).toHaveLength(1);
});

it("shows a refusal from the server and no key", async () => {
  serve({
    nodes: [streamNode({})],
    issue: () =>
      json(
        {
          type: "https://silo.dev/problems/conflict",
          title: "Conflict",
          status: 409,
          detail: "Only transcode nodes take a key.",
        },
        409,
        "application/problem+json",
      ),
  });
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: "Issue a key for Transcoder" }));
  fireEvent.click(
    within(await screen.findByRole("dialog")).getByRole("button", { name: "Issue key" }),
  );
  expect(await screen.findByRole("alert")).toHaveTextContent("Only transcode nodes take a key.");
  expect(screen.queryByText(/SILO_NODE_KEY=/)).not.toBeInTheDocument();
});
