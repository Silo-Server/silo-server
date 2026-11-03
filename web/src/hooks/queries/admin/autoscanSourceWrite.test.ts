import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useCreateAutoscanSource } from "../useAutoscan";
const source = {
  id: "source-a",
  plugin_id: "plugin",
  capability_id: "cap",
  connection_id: null,
  enabled: true,
  delivery_mode: "poll",
  path_rewrites: [],
  source_config: {},
  label: "label",
  webhook_configured: false,
};
const response = (create: boolean) =>
  new Response(JSON.stringify(source), {
    status: create ? 201 : 200,
    headers: { "Content-Type": "application/json" },
  });
const body = () => ({
  plugin_id: "plugin",
  capability_id: "cap",
  enabled: true,
  connection_id: null,
  poll_interval_seconds: null,
  path_rewrites: [{ from: "/old", to: "/new" }],
  source_config: { key: "old" },
});
function fixture() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: 3, retryDelay: 0 }, queries: { retry: false } },
  });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
  setProfileId("profile-a");
  setProfileToken("pin-a");
  onlineManager.setOnline(true);
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
});

it("create fences late receipt and caller callback", async () => {
  let release!: (r: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      () =>
        new Promise<Response>((r) => {
          release = r;
        }),
    ),
  );
  const { client, wrapper } = fixture();
  const invalidation = vi.spyOn(client, "invalidateQueries");
  const callback = vi.fn();
  const { result } = renderHook(useCreateAutoscanSource, { wrapper });
  act(() => result.current.mutate(body(), { onSuccess: callback }));
  await waitFor(() => expect(release).toBeTypeOf("function"));
  act(() => setProfileToken("pin-b"));
  await act(async () => release(response(true)));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(callback).not.toHaveBeenCalled();
  expect(invalidation).not.toHaveBeenCalled();
});
