import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";
import type { StreamNode } from "@/api/types";
import AdminNodes from "./AdminNodes";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
// The routing section has its own tests and needs a data router; these cover
// the node list and editor around it.
vi.mock("@/pages/admin-settings/NodeRoutingSettings", () => ({ NodeRoutingSettings: () => null }));

const NODE_KEY = "snk1_17_c3ludGhldGljLWtleS1ieXRlcy1mb3ItdGVzdHM";

const node: StreamNode = {
  id: "17",
  config_etag: '"original"',
  name: "Synthetic",
  type: "transcode",
  url: "http://node.invalid",
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
  source_access: "local",
};

const reply = (body: unknown, init: { status?: number; etag?: string } = {}) =>
  new Response(JSON.stringify(body), {
    status: init.status ?? 200,
    headers: { "Content-Type": "application/json", ...(init.etag ? { ETag: init.etag } : {}) },
  });

type Call = { url: string; init: RequestInit };

/**
 * A fake server: the node list, effective settings, and recorded writes. A
 * `keyReply` replaces the key endpoint's answer, for failures and for holding
 * an issuance pending.
 */
function serve(
  listed: StreamNode | StreamNode[],
  keyReply: () => Response | Promise<Response> = () =>
    reply({ node_key: NODE_KEY }, { status: 201 }),
) {
  const items = Array.isArray(listed) ? listed : [listed];
  const writes: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: unknown, init: RequestInit = {}) => {
      const url = String(input);
      const method = init.method ?? "GET";
      if (method === "GET" && url.includes("/admin/settings/effective"))
        return reply({ "server.public_url": "https://silo.example/" }, { etag: '"settings"' });
      if (method === "GET") return reply({ items, page: { has_more: false } });
      writes.push({ url, init });
      if (url.endsWith("/key")) return keyReply();
      return reply({ ...items[0], config_etag: '"saved"' }, { etag: '"saved"' });
    }),
  );
  return writes;
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <AdminNodes />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return client;
}

/** Whether any mutation TanStack still holds carries the node key. */
function mutationCacheHoldsKey(client: QueryClient) {
  return client
    .getMutationCache()
    .getAll()
    .some((mutation) => JSON.stringify(mutation.state).includes(NODE_KEY));
}

/** Opens the add dialog for a node type and fills the required fields. */
async function openAddForm(type: "Proxy" | "Transcode") {
  fireEvent.click((await screen.findAllByRole("button", { name: `Add ${type}` }))[0]!);
  const dialog = await screen.findByRole("dialog");
  fireEvent.change(within(dialog).getByPlaceholderText(`${type} Node 1`), {
    target: { value: "New node" },
  });
  fireEvent.change(within(dialog).getByPlaceholderText(/^http/), {
    target: { value: "http://new.invalid" },
  });
  return dialog;
}

function chooseOption(combobox: HTMLElement, option: string) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
  fireEvent.click(within(screen.getByRole("listbox")).getByRole("option", { name: option }));
}

beforeAll(() => {
  // Radix Select reads pointer capture and scrolls its options; jsdom has neither.
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
});
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic");
  setRefreshToken("synthetic-refresh");
  setProfileId("a");
  setProfileToken("pin-a");
  onlineManager.setOnline(true);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

it("saves the source access chosen in the node editor", async () => {
  const writes = serve(node);
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: "Edit Synthetic" }));
  const select = screen.getByRole("combobox", { name: "Source Access" });
  expect(select).toHaveTextContent("Local library mount");
  chooseOption(select, "Read from this server over HTTP");
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]!.init.method).toBe("PUT");
  expect(new Headers(writes[0]!.init.headers).get("If-Match")).toBe('"original"');
  expect(JSON.parse(writes[0]!.init.body as string).source_access).toBe("core");
});

it("sends the source access chosen when creating a transcode node", async () => {
  const writes = serve(node);
  renderPage();
  const dialog = await openAddForm("Transcode");
  chooseOption(
    within(dialog).getByRole("combobox", { name: "Source Access" }),
    "Read from this server over HTTP",
  );
  fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]!.init.method).toBe("POST");
  const body = JSON.parse(writes[0]!.init.body as string);
  expect(body.type).toBe("transcode");
  expect(body.source_access).toBe("core");
});

it("leaves source access out of the proxy form and its request", async () => {
  const writes = serve(node);
  renderPage();
  const dialog = await openAddForm("Proxy");
  expect(within(dialog).queryByRole("combobox", { name: "Source Access" })).toBeNull();
  fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
  await waitFor(() => expect(writes).toHaveLength(1));
  const body = JSON.parse(writes[0]!.init.body as string);
  expect(body.type).toBe("proxy");
  expect(body).not.toHaveProperty("source_access");
});

it("shows an issued key once, with the node environment", async () => {
  const writes = serve(node);
  const client = renderPage();
  expect(await screen.findByText("No key: this node takes no work")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Issue key for Synthetic" }));
  // A keyless node has nothing to break, so issuing asks no confirmation.
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]!.url).toContain("/api/v2/admin/nodes/17/key");
  expect(writes[0]!.init.method).toBe("POST");

  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByText(/not shown again/)).toBeInTheDocument();
  expect(within(dialog).getByText(NODE_KEY)).toBeInTheDocument();
  expect(within(dialog).getByLabelText("Node key")).toHaveTextContent(NODE_KEY);
  await waitFor(() =>
    expect(within(dialog).getByLabelText("Node environment")).toHaveTextContent(
      `SILO_CORE_URL=https://silo.example SILO_NODE_KEY=${NODE_KEY}`,
    ),
  );
  // The reveal has the key; the mutation that fetched it keeps none.
  expect(mutationCacheHoldsKey(client)).toBe(false);

  fireEvent.click(within(dialog).getByRole("button", { name: "I've saved it" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(screen.queryByText(NODE_KEY)).not.toBeInTheDocument();
  expect(document.body.textContent).not.toContain(NODE_KEY);
  expect(mutationCacheHoldsKey(client)).toBe(false);
  expect(writes).toHaveLength(1);
});

it("keeps the key dialog open on an outside click", async () => {
  serve(node);
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: "Issue key for Synthetic" }));
  const dialog = await screen.findByRole("dialog");
  await waitFor(() => expect(within(dialog).getByText(NODE_KEY)).toBeInTheDocument());
  // Radix attaches its outside-pointer listener on the next tick.
  await act(() => new Promise((resolve) => setTimeout(resolve, 0)));
  fireEvent.pointerDown(document.body);
  fireEvent.click(document.body);
  expect(screen.getByRole("dialog")).toBeInTheDocument();
  expect(screen.getByText(NODE_KEY)).toBeInTheDocument();

  fireEvent.keyDown(dialog, { key: "Escape" });
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
});

it("holds every key action while one issuance is in flight", async () => {
  let finish!: (response: Response) => void;
  const writes = serve(
    [node, { ...node, id: "18", name: "Second" }],
    () => new Promise<Response>((resolve) => (finish = resolve)),
  );
  renderPage();
  const first = await screen.findByRole("button", { name: "Issue key for Synthetic" });
  const second = screen.getByRole("button", { name: "Issue key for Second" });
  fireEvent.click(first);
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(first).toBeDisabled();
  expect(second).toBeDisabled();
  fireEvent.click(second);
  expect(writes).toHaveLength(1);

  await act(async () => finish(reply({ node_key: NODE_KEY }, { status: 201 })));
  expect(await screen.findByRole("dialog")).toHaveTextContent("Key for Synthetic");
});

it("tells the admin when issuing a key fails", async () => {
  const writes = serve(node, () =>
    reply({ type: "about:blank", title: "Internal Server Error", status: 500 }, { status: 500 }),
  );
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: "Issue key for Synthetic" }));
  await waitFor(() => expect(writes).toHaveLength(1));
  await waitFor(() =>
    expect(toast.error).toHaveBeenCalledWith(
      expect.stringContaining("Could not confirm a new key for Synthetic"),
    ),
  );
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Issue key for Synthetic" })).toBeEnabled();
});

it("asks before rotating an issued key", async () => {
  const writes = serve({ ...node, key_issued: true });
  renderPage();
  expect(await screen.findByText("Key issued")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Rotate key for Synthetic" }));
  const confirm = await screen.findByRole("alertdialog");
  expect(within(confirm).getByText(/stops working at once/)).toBeInTheDocument();
  expect(within(confirm).getByText(/Drain the node first/)).toBeInTheDocument();
  expect(writes).toHaveLength(0);

  fireEvent.click(within(confirm).getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  expect(writes).toHaveLength(0);

  fireEvent.click(screen.getByRole("button", { name: "Rotate key for Synthetic" }));
  await act(async () => {
    fireEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Rotate key" }),
    );
  });
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]!.url).toContain("/api/v2/admin/nodes/17/key");
  expect(await screen.findByText(NODE_KEY)).toBeInTheDocument();
});
