import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
  sourceAccess = false,
  issue = () => json({ node_key: KEY }, 201),
  bodies = [],
}: {
  nodes: StreamNode[];
  nodeKeys?: boolean;
  sourceAccess?: boolean;
  issue?: () => Response;
  /** Collects the JSON body of every write. */
  bodies?: { method: string; body: Record<string, unknown> }[];
}) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((input: unknown, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      calls.push(`${method} ${new URL(url, "http://x").pathname}`);
      if (method !== "GET" && typeof init?.body === "string")
        bodies.push({ method, body: JSON.parse(init.body) as Record<string, unknown> });
      if (url.includes("/admin/nodes/capabilities"))
        return Promise.resolve(
          json({
            state: "available",
            allowed: true,
            revision: "r",
            drain: true,
            summary: true,
            node_keys: nodeKeys,
            source_access: sourceAccess,
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

it("lets an admin choose a transcode node's source access with the capability", async () => {
  // jsdom has no pointer capture or scrolling, which the select uses.
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
  const user = userEvent.setup();
  const bodies: { method: string; body: Record<string, unknown> }[] = [];
  serve({
    nodes: [streamNode({ source_access: "local", config_etag: '"e1"' })],
    sourceAccess: true,
    bodies,
  });
  renderPage();
  await user.click(await screen.findByRole("button", { name: "Edit Transcoder" }));
  const field = await screen.findByRole("combobox", { name: "Source access" });
  expect(field).toHaveTextContent("Library mount on the node");
  expect(
    screen.getByText(/every byte it reads passes through the API servers/),
  ).toBeInTheDocument();
  await user.click(field);
  await user.click(await screen.findByRole("option", { name: "Through the API servers" }));
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(bodies.some((b) => b.method === "PUT")).toBe(true));
  expect(bodies.find((b) => b.method === "PUT")?.body.source_access).toBe("core");
});

it("offers no source access without the capability, or on a proxy", async () => {
  const user = userEvent.setup();
  const calls = serve({ nodes: [streamNode({}), proxy], sourceAccess: false });
  renderPage();
  await user.click(await screen.findByRole("button", { name: "Edit Transcoder" }));
  await screen.findByRole("dialog");
  await waitFor(() => expect(calls).toContain("GET /api/v2/admin/nodes/capabilities"));
  await act(async () => {});
  expect(screen.queryByRole("combobox", { name: "Source access" })).not.toBeInTheDocument();
});
