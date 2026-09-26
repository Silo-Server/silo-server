import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import type { AutoscanSource, AutoscanScanSourceDescriptor } from "@/api/types";
import { SourceRow } from "./SourcesPanel";
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
const source: AutoscanSource = {
  id: "source-a",
  plugin_id: "plugin",
  capability_id: "cap",
  connection_id: null,
  enabled: false,
  delivery_mode: "poll",
  poll_interval_seconds: null,
  path_rewrites: [],
  source_config: {},
  label: "Old",
  last_run_at: null,
  last_error: null,
  webhook_configured: false,
};
const descriptor: AutoscanScanSourceDescriptor = { delivery_modes: ["poll"], connection: "none" };
function mount() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: 3 }, queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <SourceRow
        source={source}
        descriptor={descriptor}
        connectionOptions={[]}
        pluginDisplayNames={new Map()}
        globalPollInterval={60}
        onDelete={() => {}}
        layout="card"
      />
    </QueryClientProvider>,
  );
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
it("actual row label edit saves full state through PUT", async () => {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ ...source, label: "New" }), {
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  mount();
  const input = screen.getByPlaceholderText("Custom label (optional)");
  fireEvent.change(input, { target: { value: "New" } });
  fireEvent.blur(input);
  await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
  const request = fetchMock.mock.calls[0]!;
  expect(String(request[0])).toContain("/api/v2/admin/autoscan/sources/source-a");
  expect(request[1].method).toBe("PUT");
  expect(JSON.parse(request[1].body)).toMatchObject({
    label: "New",
    enabled: false,
    path_rewrites: [],
  });
});
it("actual row retained draft refuses PIN replacement", async () => {
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  mount();
  const input = screen.getByPlaceholderText("Custom label (optional)");
  fireEvent.change(input, { target: { value: "Retained" } });
  act(() => setProfileToken("pin-b"));
  await act(async () => fireEvent.blur(input));
  expect(fetchMock).not.toHaveBeenCalled();
  expect(input).toHaveValue("Retained");
});
