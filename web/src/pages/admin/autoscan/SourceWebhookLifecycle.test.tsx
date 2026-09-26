import { useAutoscanSources } from "@/hooks/queries/useAutoscan";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import type { AutoscanSource } from "@/api/types";
import { WebhookEndpointSection } from "./SourcesPanel";
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
const source: AutoscanSource = {
  id: "source-a",
  plugin_id: "plugin",
  capability_id: "cap",
  connection_id: null,
  enabled: true,
  delivery_mode: "webhook",
  poll_interval_seconds: null,
  path_rewrites: [],
  source_config: {},
  label: "Source",
  last_run_at: null,
  last_error: null,
  webhook_configured: true,
  webhook_url: "/api/v2/autoscan/webhooks/old-synthetic",
};
function mount(initial = source) {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: 3 }, queries: { retry: false } },
  });
  const view = (row: AutoscanSource) => (
    <QueryClientProvider client={client}>
      <WebhookEndpointSection
        source={row}
        provider="auto"
        onProviderChange={() => {}}
        isSaving={false}
      />
    </QueryClientProvider>
  );
  const mounted = render(view(initial));
  return { rerender: (row: AutoscanSource) => mounted.rerender(view(row)) };
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken(null);
  setProfileId("a");
  setProfileToken("pin-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("rotation confirmation keeps its original source and uses returned current URL instead of cached old URL", async () => {
  let release!: (r: Response) => void;
  const fetchMock = vi.fn(
    (_url: unknown) =>
      new Promise<Response>((resolve) => {
        release = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const view = mount();
  fireEvent.click(screen.getByRole("button", { name: "Rotate webhook URL" }));
  view.rerender({ ...source, id: "source-b" });
  fireEvent.click(screen.getByRole("button", { name: "Rotate" }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/sources/source-a/webhook/rotate");
  expect(screen.getByLabelText("Webhook delivery URL")).not.toHaveValue(
    `${window.location.origin}/api/v2/autoscan/webhooks/old-synthetic`,
  );
  await act(async () =>
    release(
      new Response(
        JSON.stringify({ ...source, webhook_url: "/api/v2/autoscan/webhooks/new-synthetic" }),
        { headers: { "Content-Type": "application/json" } },
      ),
    ),
  );
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Rotate webhook URL" })).toBeEnabled(),
  );
  // The receipt belongs to source-a, never to a replacement row.
  await waitFor(() =>
    expect(screen.getByLabelText("Webhook delivery URL")).not.toHaveValue(
      `${window.location.origin}/api/v2/autoscan/webhooks/new-synthetic`,
    ),
  );
});
it("successful rotation shows current readback when parent cache remains old", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue(
        new Response(
          JSON.stringify({ ...source, webhook_url: "/api/v2/autoscan/webhooks/new-synthetic" }),
          { headers: { "Content-Type": "application/json" } },
        ),
      ),
  );
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Rotate webhook URL" }));
  fireEvent.click(screen.getByRole("button", { name: "Rotate" }));
  await waitFor(() =>
    expect(screen.getByLabelText("Webhook delivery URL")).toHaveValue(
      `${window.location.origin}/api/v2/autoscan/webhooks/new-synthetic`,
    ),
  );
});
it("uncertain rotation hides old secret and disables copying and replacement", async () => {
  vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("private-secret")));
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Rotate webhook URL" }));
  fireEvent.click(screen.getByRole("button", { name: "Rotate" }));
  await waitFor(() =>
    expect(screen.getByLabelText("Webhook delivery URL")).toHaveValue(
      "Reload this page before using or replacing this URL",
    ),
  );
  expect(screen.getByRole("button", { name: "Copy webhook URL" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Rotate webhook URL" })).toBeDisabled();
});
it("confirmation opened under an old PIN cannot send or reveal the old secret", async () => {
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const view = mount();
  fireEvent.click(screen.getByRole("button", { name: "Rotate webhook URL" }));
  act(() => setProfileToken("pin-b"));
  view.rerender(source);
  fireEvent.click(screen.getByRole("button", { name: "Rotate" }));
  await waitFor(() =>
    expect(screen.getByLabelText("Webhook delivery URL")).toHaveValue(
      "Select the original administrator profile to view this URL",
    ),
  );
  expect(fetchMock).not.toHaveBeenCalled();
});
it("actual generate action displays returned URL without waiting for parent refresh", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(
      new Response(JSON.stringify(source), { headers: { "Content-Type": "application/json" } }),
    );
  vi.stubGlobal("fetch", fetchMock);
  mount({ ...source, webhook_configured: false, webhook_url: undefined });
  fireEvent.click(screen.getByRole("button", { name: "Generate webhook URL" }));
  await waitFor(() =>
    expect(screen.getByLabelText("Webhook delivery URL")).toHaveValue(
      `${window.location.origin}/api/v2/autoscan/webhooks/old-synthetic`,
    ),
  );
  expect(fetchMock).toHaveBeenCalledOnce();
});

// Read observations come from the actual source query hook; separate clients let
// an old read finish after a newer read without TanStack canceling it for us.
it.each(["rotation", "deletion", "reveal recovery"])(
  "retires same-ID mutation readback for later %s without rollback from an older in-flight read",
  async (change) => {
    const reply = (body: unknown) =>
      new Response(JSON.stringify(body), {
        headers: { "Content-Type": "application/json" },
      });
    let finishOld!: (response: Response) => void;
    let nextRead: AutoscanSource = source;
    let reads = 0;
    const fetchMock = vi.fn((_url: unknown, init: RequestInit) => {
      if (init.method === "POST")
        return Promise.resolve(
          reply({
            ...source,
            webhook_url:
              change === "reveal recovery"
                ? undefined
                : "/api/v2/autoscan/webhooks/local-synthetic",
          }),
        );
      reads++;
      if (reads === 1)
        return new Promise<Response>((resolve) => {
          finishOld = resolve;
        });
      return Promise.resolve(reply({ items: [nextRead], page: { has_more: false } }));
    });
    vi.stubGlobal("fetch", fetchMock);
    function reader() {
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      return renderHook(useAutoscanSources, {
        wrapper: ({ children }) => (
          <QueryClientProvider client={client}>{children}</QueryClientProvider>
        ),
      });
    }
    const oldRead = reader();
    await waitFor(() => expect(finishOld).toBeTypeOf("function"));
    const view = mount();
    fireEvent.click(screen.getByRole("button", { name: "Rotate webhook URL" }));
    fireEvent.click(screen.getByRole("button", { name: "Rotate" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Rotate webhook URL" })).toBeEnabled(),
    );
    await waitFor(() =>
      expect(screen.getByLabelText("Webhook delivery URL")).toHaveValue(
        change === "reveal recovery"
          ? "… (URL unavailable)"
          : `${window.location.origin}/api/v2/autoscan/webhooks/local-synthetic`,
      ),
    );
    // This read started before the mutation, even though it finishes afterward.
    await act(async () => finishOld(reply({ items: [source], page: { has_more: false } })));
    await waitFor(() => expect(oldRead.result.current.isSuccess).toBe(true));
    view.rerender(oldRead.result.current.data![0]!);
    expect(screen.getByLabelText("Webhook delivery URL")).toHaveValue(
      change === "reveal recovery"
        ? "… (URL unavailable)"
        : `${window.location.origin}/api/v2/autoscan/webhooks/local-synthetic`,
    );
    nextRead =
      change === "deletion"
        ? { ...source, webhook_configured: false, webhook_url: undefined }
        : { ...source, webhook_url: "/api/v2/autoscan/webhooks/fresh-synthetic" };
    const freshRead = reader();
    await waitFor(() => expect(freshRead.result.current.isSuccess).toBe(true));
    view.rerender(freshRead.result.current.data![0]!);
    if (change === "deletion") {
      expect(screen.queryByLabelText("Webhook delivery URL")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Generate webhook URL" })).toBeEnabled();
    } else {
      expect(screen.getByLabelText("Webhook delivery URL")).toHaveValue(
        `${window.location.origin}/api/v2/autoscan/webhooks/fresh-synthetic`,
      );
    }
    // A later publication of that older observation cannot resurrect a secret.
    view.rerender(oldRead.result.current.data![0]!);
    if (change === "deletion")
      expect(screen.queryByLabelText("Webhook delivery URL")).not.toBeInTheDocument();
    else
      expect(screen.getByLabelText("Webhook delivery URL")).toHaveValue(
        `${window.location.origin}/api/v2/autoscan/webhooks/fresh-synthetic`,
      );
    if (change === "deletion") {
      fireEvent.click(screen.getByRole("button", { name: "Generate webhook URL" }));
      await waitFor(() =>
        expect(screen.getByLabelText("Webhook delivery URL")).toHaveValue(
          `${window.location.origin}/api/v2/autoscan/webhooks/local-synthetic`,
        ),
      );
    }
  },
);
