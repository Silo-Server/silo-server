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
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
  setProfileToken,
} from "@/api/client";
import type { StreamNode } from "@/api/types";
import { useCreateNode, useUpdateNode, useDeleteNode } from "./nodes";
import { adminKeys } from "../keys";
import AdminNodes from "@/pages/AdminNodes";
const node: StreamNode = {
  id: "17",
  config_etag: '"original"',
  name: "Synthetic",
  type: "proxy",
  url: "http://node.invalid",
  enabled: true,
  healthy: false,
  active_jobs: 0,
  group: null,
  max_jobs: null,
  max_bandwidth_kbps: null,
  egress_kbps: 0,
  last_health_check: null,
  created_at: "2026-09-06T00:00:00Z",
};
const reply = (body: unknown, etag?: string) =>
  new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json", ...(etag ? { ETag: etag } : {}) },
  });
const saved = () => reply({ ...node, config_etag: '"saved"' }, '"saved"');
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
it("update queues the original validator, node and draft once", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn().mockImplementation(async () => saved());
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useUpdateNode, fixture());
  const selected = structuredClone(node);
  const body = { name: "Submitted", public_url: null };
  act(() => result.current.mutate({ node: selected, body }));
  selected.id = "99";
  selected.config_etag = '"later"';
  body.name = "Later draft";
  act(() => onlineManager.setOnline(true));
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0]!;
  expect(String(url)).toContain("/api/v2/admin/nodes/17");
  expect(new Headers(init.headers).get("If-Match")).toBe('"original"');
  expect(JSON.parse(init.body)).toEqual({ name: "Submitted", public_url: null });
});
it("queued deletion refuses same-profile PIN replacement without dispatch", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useDeleteNode, fixture());
  const authority = captureProfileRequestContext();
  act(() => result.current.mutate({ node, authority }));
  act(() => {
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
});
it.each(["network", "401", "412"])("update never automatically replays %s", async (failure) => {
  const fetchMock = vi.fn().mockImplementation(async () => {
    if (failure === "network") throw new Error("uncertain");
    return new Response(
      JSON.stringify({
        type:
          "https://siloserver.org/docs/api/v2/problems/" +
          (failure === "412" ? "precondition_failed" : "authentication_required"),
        title: "Refused",
        status: Number(failure),
      }),
      { status: Number(failure), headers: { "Content-Type": "application/problem+json" } },
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useUpdateNode, fixture());
  act(() => result.current.mutate({ node, body: { name: "Edit" } }));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
it("late create acknowledgement cannot close UI or invalidate another authority", async () => {
  let finish!: (r: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      () =>
        new Promise<Response>((r) => {
          finish = r;
        }),
    ),
  );
  const { client, wrapper } = fixture();
  const invalidate = vi.spyOn(client, "invalidateQueries");
  const close = vi.fn();
  const { result } = renderHook(useCreateNode, { wrapper });
  act(() =>
    result.current.mutate(
      { name: "Synthetic", type: "proxy", url: node.url },
      { onSuccess: close },
    ),
  );
  await waitFor(() => expect(finish).toBeTypeOf("function"));
  act(() => setProfileId("b"));
  await act(async () => finish(saved()));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(close).not.toHaveBeenCalled();
  expect(invalidate).not.toHaveBeenCalled();
});
it("mounted edit retains original validator across a later list and locks submitted draft", async () => {
  let listed = node;
  let finish!: (r: Response) => void;
  const writes: RequestInit[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((_url: unknown, init: RequestInit) => {
      if (init.method === "PUT") {
        writes.push(init);
        return new Promise<Response>((r) => {
          finish = r;
        });
      }
      return Promise.resolve(reply({ items: [listed], page: { has_more: false } }));
    }),
  );
  const { client, wrapper: Wrapper } = fixture();
  render(
    <MemoryRouter>
      <Wrapper>
        <AdminNodes />
      </Wrapper>
    </MemoryRouter>,
  );
  fireEvent.click(await screen.findByRole("button", { name: "Edit Synthetic" }));
  const name = screen.getByDisplayValue("Synthetic");
  fireEvent.change(name, { target: { value: "Draft" } });
  listed = { ...node, config_etag: '"competitor"', name: "Competitor" };
  await act(async () => {
    await client.invalidateQueries({ queryKey: adminKeys.nodes() });
  });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(new Headers(writes[0]!.headers).get("If-Match")).toBe('"original"');
  expect(JSON.parse(writes[0]!.body as string).name).toBe("Draft");
  expect(name).toBeDisabled();
  await act(async () => finish(saved()));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
});
it("mounted delete confirmation retains original target and validator", async () => {
  let listed = node;
  const writes: RequestInit[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((_url: unknown, init: RequestInit) => {
      if (init.method === "DELETE") {
        writes.push(init);
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      return Promise.resolve(reply({ items: [listed], page: { has_more: false } }));
    }),
  );
  const { client, wrapper: Wrapper } = fixture();
  render(
    <MemoryRouter>
      <Wrapper>
        <AdminNodes />
      </Wrapper>
    </MemoryRouter>,
  );
  fireEvent.click(await screen.findByRole("button", { name: "Delete Synthetic" }));
  listed = { ...node, config_etag: '"competitor"' };
  await act(async () => {
    await client.invalidateQueries({ queryKey: adminKeys.nodes() });
  });
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(new Headers(writes[0]!.headers).get("If-Match")).toBe('"original"');
});
it("setup create preserves authenticated profile absence", async () => {
  setProfileId(null);
  setProfileToken(null);
  const fetchMock = vi.fn().mockImplementation(async () => saved());
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useCreateNode, fixture());
  act(() => result.current.mutate({ name: "Setup", type: "proxy", url: node.url }));
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  const headers = new Headers(fetchMock.mock.calls[0]![1].headers);
  expect(headers.get("Authorization")).toBe("Bearer synthetic");
  expect(headers.get("X-Profile-Id")).toBe("");
  expect(result.current.isAuthorityActive()).toBe(true);
});
it("profile selection after queued account-only create refuses dispatch", async () => {
  setProfileId(null);
  setProfileToken(null);
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useCreateNode, fixture());
  act(() => result.current.mutate({ name: "Setup", type: "proxy", url: node.url }));
  act(() => {
    setProfileId("new-profile");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
});

it("unmounted queued creation never dispatches", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result, unmount } = renderHook(useCreateNode, fixture());
  let pending!: Promise<unknown>;
  act(() => {
    pending = result.current.mutateAsync({ name: "Setup", type: "proxy", url: node.url });
  });
  const rejected = expect(pending).rejects.toThrow();
  unmount();
  act(() => onlineManager.setOnline(true));
  await rejected;
  expect(fetchMock).not.toHaveBeenCalled();
});
it("old mounted form acknowledgement cannot close a newly opened draft", async () => {
  let finish!: (r: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn((_url: unknown, init: RequestInit) => {
      if (init.method === "PUT")
        return new Promise<Response>((resolve) => {
          finish = resolve;
        });
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
  fireEvent.click(await screen.findByRole("button", { name: "Edit Synthetic" }));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(finish).toBeTypeOf("function"));
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  fireEvent.click(screen.getByRole("button", { name: "Edit Synthetic" }));
  fireEvent.change(screen.getByDisplayValue("Synthetic"), {
    target: { value: "New retained draft" },
  });
  await act(async () => finish(saved()));
  expect(screen.getByRole("dialog")).toBeInTheDocument();
  expect(screen.getByDisplayValue("New retained draft")).toBeInTheDocument();
});
