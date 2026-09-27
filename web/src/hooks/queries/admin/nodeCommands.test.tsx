import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import {
  act,
  cleanup,
  renderHook,
  waitFor,
  render,
  screen,
  fireEvent,
} from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import type { StreamNode } from "@/api/types";
import { useAdminNodes, useCheckNodeHealth, useReprobeNode } from "./nodes";
import AdminNodes from "@/pages/AdminNodes";
const node: StreamNode = {
  id: "17",
  name: "Synthetic",
  type: "transcode",
  url: "http://synthetic.invalid",
  enabled: true,
  healthy: true,
  active_jobs: 0,
  group: null,
  max_jobs: null,
  max_bandwidth_kbps: null,
  egress_kbps: 0,
  last_health_check: null,
  created_at: "2026-09-06T00:00:00Z",
};
const reply = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
const observation = (action: string) =>
  action === "check"
    ? { healthy: false, active_jobs: 0, egress_kbps: 0, health_persisted: false }
    : {
        node_id: "17",
        node_name: "Synthetic",
        status: "error",
        error: "Node refused",
        capabilities_refreshed: false,
      };
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
  setAccessToken("synthetic");
  setRefreshToken("synthetic-refresh");
  setProfileId("a");
  setProfileToken("pin-a");
  onlineManager.setOnline(true);
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
});
for (const [action, useHook] of [
  ["check", useCheckNodeHealth],
  ["reprobe", useReprobeNode],
] as const) {
  it(`${action} captures selected node before offline queueing and accepts negative observation`, async () => {
    onlineManager.setOnline(false);
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(reply(observation(action))));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(useHook, fixture());
    const target = { ...node };
    act(() => result.current.mutate(target));
    target.id = "18";
    target.name = "Replacement";
    await waitFor(() => expect(result.current.isPaused).toBe(true));
    act(() => onlineManager.setOnline(true));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain(`/api/v2/admin/nodes/17/${action}`);
    expect(fetchMock.mock.calls[0]?.[1].method).toBe("POST");
    expect(fetchMock.mock.calls[0]?.[1].body).toBeUndefined();
    expect(result.current.variables?.name).toBe("Synthetic");
  });
  it(`${action} refuses a queued command after PIN replacement`, async () => {
    onlineManager.setOnline(false);
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(useHook, fixture());
    act(() => result.current.mutate(node));
    await waitFor(() => expect(result.current.isPaused).toBe(true));
    act(() => {
      setProfileToken("pin-b");
      onlineManager.setOnline(true);
    });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).not.toHaveBeenCalled();
  });
  it.each([401, "network"])(`${action} does not replay %s under global retry3`, async (failure) => {
    const fetchMock =
      failure === "network"
        ? vi.fn().mockRejectedValue(new Error("private"))
        : vi.fn().mockImplementation(() => Promise.resolve(new Response(null, { status: 401 })));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(useHook, fixture());
    act(() => result.current.mutate(node));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
  });
  it(`${action} fences late node observation and invalidation`, async () => {
    let finish!: (r: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            finish = resolve;
          }),
      ),
    );
    const { client, wrapper } = fixture();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const { result } = renderHook(useHook, { wrapper });
    act(() => result.current.mutate(node));
    await waitFor(() => expect(finish).toBeTypeOf("function"));
    act(() => setProfileId("b"));
    await act(async () => finish(reply(observation(action))));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(invalidate).not.toHaveBeenCalled();
    expect(result.current.data).toBeUndefined();
  });
  it(`actual node row ${action} action dispatches once and waits for response`, async () => {
    let finish!: (r: Response) => void;
    const writes: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: unknown, init: RequestInit) => {
        if (init.method === "POST") {
          writes.push(String(url));
          return new Promise<Response>((resolve) => {
            finish = resolve;
          });
        }
        return Promise.resolve(reply({ items: [node], page: { has_more: false } }));
      }),
    );
    const { wrapper: Wrapper } = fixture();
    render(
      <MemoryRouter>
        <Wrapper>
          <AdminNodes />
        </Wrapper>
      </MemoryRouter>,
    );
    const label =
      action === "check" ? "Check health of Synthetic" : "Re-probe hardware on Synthetic";
    const button = await screen.findByRole("button", { name: label });
    fireEvent.click(button);
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(button).toBeDisabled();
    expect(writes[0]).toContain(`/api/v2/admin/nodes/17/${action}`);
    await act(async () => finish(reply(observation(action))));
    await waitFor(() => expect(screen.getByRole("button", { name: label })).toBeEnabled());
  });
}
it.each([null, "pin-b"])(
  "node reader hides cached success after PIN transition %s",
  async (pin) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(reply({ items: [node], page: { has_more: false } }))
      .mockImplementation(() => new Promise(() => {}));
    vi.stubGlobal("fetch", fetchMock);
    const { result, rerender } = renderHook(useAdminNodes, fixture());
    await waitFor(() => expect(result.current.data?.[0]?.name).toBe("Synthetic"));
    act(() => setProfileToken(pin));
    rerender();
    expect(result.current.data).toBeUndefined();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  },
);
