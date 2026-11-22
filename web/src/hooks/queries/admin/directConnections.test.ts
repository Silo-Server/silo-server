import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";
import type { components } from "@/api/v2/schema";

import { useAdminDirectConnectionsStatus, useRefreshDirectConnections } from "./directConnections";

const status: components["schemas"]["DirectConnectionsStatus"] = { enabled: true, hosts: [] };

function fixture() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
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
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("hides cached diagnostics after a profile authority transition", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(Response.json(status))
    .mockImplementation(() => new Promise(() => {}));
  vi.stubGlobal("fetch", fetchMock);
  const { wrapper } = fixture();
  const { result, rerender } = renderHook(useAdminDirectConnectionsStatus, { wrapper });
  await waitFor(() => expect(result.current.data).toEqual(status));

  act(() => setProfileToken("pin-b"));
  rerender();

  expect(result.current.data).toBeUndefined();
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
});

it("refuses a refresh result after its initiating authority changes", async () => {
  let finish!: (response: Response) => void;
  const fetchMock = vi.fn(() => new Promise<Response>((resolve) => (finish = resolve)));
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const { result } = renderHook(useRefreshDirectConnections, { wrapper });

  act(() => result.current.mutate());
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  act(() => setProfileToken("pin-b"));
  await act(async () => finish(Response.json(status)));

  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(
    client
      .getQueryCache()
      .getAll()
      .some((query) => query.state.data !== undefined),
  ).toBe(false);
});

function statusAt(checkedAt: string): components["schemas"]["DirectConnectionsStatus"] {
  return {
    enabled: true,
    hosts: [
      {
        host: { id: "api", name: "API", role: "api" },
        checked_at: checkedAt,
        features: [],
        knobs: [],
      },
    ],
  };
}

it("drops a poll that started before a refresh published and answers after it", async () => {
  const first = statusAt("2026-11-01T10:00:00.000Z");
  // Later than the refresh result: only request order, not the server time, drops it.
  const late = statusAt("2026-11-01T10:00:20.000Z");
  const refreshed = statusAt("2026-11-01T10:00:10.000Z");
  let finishRefresh!: (response: Response) => void;
  let finishPoll!: (response: Response) => void;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = input instanceof Request ? input.url : String(input);
    if (url.includes("refresh=true"))
      return new Promise<Response>((resolve) => (finishRefresh = resolve));
    if (fetchMock.mock.calls.length === 1) return Promise.resolve(Response.json(first));
    return new Promise<Response>((resolve) => (finishPoll = resolve));
  });
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const poll = renderHook(useAdminDirectConnectionsStatus, { wrapper });
  const refresh = renderHook(useRefreshDirectConnections, { wrapper });
  await waitFor(() => expect(poll.result.current.data).toEqual(first));

  act(() => refresh.result.current.mutate());
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  // A poll that began while the refresh ran answers after the refresh published.
  act(() => void poll.result.current.refetch());
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));

  await act(async () => finishRefresh(Response.json(refreshed)));
  await waitFor(() => expect(poll.result.current.data).toEqual(refreshed));
  await act(async () => finishPoll(Response.json(late)));

  // The observer re-renders only on a change, so wait on the client's fetch count.
  await waitFor(() => expect(client.isFetching()).toBe(0));
  expect(poll.result.current.data).toEqual(refreshed);
});

it("keeps a refresh result that published between a poll's queryFn and its cache write", async () => {
  const first = statusAt("2026-11-01T10:00:00.000Z");
  const late = statusAt("2026-11-01T10:00:20.000Z");
  const refreshed = statusAt("2026-11-01T10:00:10.000Z");
  let finishRefresh!: (response: Response) => void;
  let finishPoll!: (response: Response) => void;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = input instanceof Request ? input.url : String(input);
    if (url.includes("refresh=true"))
      return new Promise<Response>((resolve) => (finishRefresh = resolve));
    if (fetchMock.mock.calls.length === 1) return Promise.resolve(Response.json(first));
    return new Promise<Response>((resolve) => (finishPoll = resolve));
  });
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const poll = renderHook(useAdminDirectConnectionsStatus, { wrapper });
  const refresh = renderHook(useRefreshDirectConnections, { wrapper });
  await waitFor(() => expect(poll.result.current.data).toEqual(first));

  // Hold the poll's cache write: React Query awaits the queryFn result before it calls
  // setData, and the held call stands for that gap. A refresh write has manual set.
  const query = client.getQueryCache().getAll()[0]!;
  const setData = query.setData.bind(query);
  let held: (() => void) | undefined;
  vi.spyOn(query, "setData").mockImplementation((data, options) => {
    if (options?.manual) return setData(data, options);
    held = () => void setData(data, options);
    return data;
  });

  act(() => refresh.result.current.mutate());
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  act(() => void poll.result.current.refetch());
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
  // The poll's queryFn returns; its result waits for the cache write.
  await act(async () => finishPoll(Response.json(late)));
  await waitFor(() => expect(held).toBeDefined());
  await act(async () => finishRefresh(Response.json(refreshed)));
  await waitFor(() => expect(poll.result.current.data).toEqual(refreshed));

  act(() => held!());

  expect(client.getQueryData(query.queryKey)).toEqual(refreshed);
});

it("publishes a poll result that started before a refresh when a reset emptied the cache", async () => {
  const first = statusAt("2026-11-01T10:00:00.000Z");
  const late = statusAt("2026-11-01T10:00:20.000Z");
  const refreshed = statusAt("2026-11-01T10:00:10.000Z");
  let finishPoll!: (response: Response) => void;
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = input instanceof Request ? input.url : String(input);
    if (url.includes("refresh=true")) return Promise.resolve(Response.json(refreshed));
    if (fetchMock.mock.calls.length === 1) return Promise.resolve(Response.json(first));
    return new Promise<Response>((resolve) => (finishPoll = resolve));
  });
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const poll = renderHook(useAdminDirectConnectionsStatus, { wrapper });
  const refresh = renderHook(useRefreshDirectConnections, { wrapper });
  await waitFor(() => expect(poll.result.current.data).toEqual(first));

  // Hold the poll's cache write, as in the test above.
  const query = client.getQueryCache().getAll()[0]!;
  const setData = query.setData.bind(query);
  let held: (() => void) | undefined;
  vi.spyOn(query, "setData").mockImplementation((data, options) => {
    if (options?.manual) return setData(data, options);
    held = () => void setData(data, options);
    return data;
  });
  act(() => void poll.result.current.refetch());
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  await act(async () => finishPoll(Response.json(late)));
  await waitFor(() => expect(held).toBeDefined());
  // A refresh publishes after the poll started, then the cache is emptied.
  act(() => refresh.result.current.mutate());
  await waitFor(() => expect(client.getQueryData(query.queryKey)).toEqual(refreshed));
  act(() => query.reset());
  expect(client.getQueryData(query.queryKey)).toBeUndefined();

  act(() => held!());

  // The poll answers older than the refresh, but there is nothing newer to keep.
  expect(client.getQueryData(query.queryKey)).toEqual(late);
});

it("publishes a poll that started after a refresh even when its checked_at is older", async () => {
  const first = statusAt("2026-11-01T10:00:00.000Z");
  const refreshed = statusAt("2026-11-01T10:00:10.000Z");
  // A replica with a slow clock, or a host clock stepped backward, answers with an older time.
  const skewed = statusAt("2026-11-01T09:59:00.000Z");
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = input instanceof Request ? input.url : String(input);
    if (url.includes("refresh=true")) return Promise.resolve(Response.json(refreshed));
    return Promise.resolve(Response.json(fetchMock.mock.calls.length === 1 ? first : skewed));
  });
  vi.stubGlobal("fetch", fetchMock);
  const { wrapper } = fixture();
  const poll = renderHook(useAdminDirectConnectionsStatus, { wrapper });
  const refresh = renderHook(useRefreshDirectConnections, { wrapper });
  await waitFor(() => expect(poll.result.current.data).toEqual(first));

  act(() => refresh.result.current.mutate());
  await waitFor(() => expect(poll.result.current.data).toEqual(refreshed));
  act(() => void poll.result.current.refetch());

  await waitFor(() => expect(poll.result.current.data).toEqual(skewed));
});

it("publishes a refresh result over a cached poll result even when its checked_at is older", async () => {
  const first = statusAt("2026-11-01T10:00:00.000Z");
  const polled = statusAt("2026-11-01T10:00:10.000Z");
  const refreshed = statusAt("2026-11-01T10:00:05.000Z");
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = input instanceof Request ? input.url : String(input);
    if (url.includes("refresh=true")) return Promise.resolve(Response.json(refreshed));
    return Promise.resolve(Response.json(fetchMock.mock.calls.length === 1 ? first : polled));
  });
  vi.stubGlobal("fetch", fetchMock);
  const { wrapper } = fixture();
  const poll = renderHook(useAdminDirectConnectionsStatus, { wrapper });
  const refresh = renderHook(useRefreshDirectConnections, { wrapper });
  await waitFor(() => expect(poll.result.current.data).toEqual(first));
  act(() => void poll.result.current.refetch());
  await waitFor(() => expect(poll.result.current.data).toEqual(polled));

  act(() => refresh.result.current.mutate());

  await waitFor(() => expect(poll.result.current.data).toEqual(refreshed));
});

it("lets the incoming snapshot win when two checks have the same time", async () => {
  const cached = statusAt("2026-11-01T10:00:00.000Z");
  const incoming = { ...statusAt("2026-11-01T10:00:00.000Z"), enabled: false };
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(Response.json(cached))
    .mockResolvedValueOnce(Response.json(incoming));
  vi.stubGlobal("fetch", fetchMock);
  const { wrapper } = fixture();
  const poll = renderHook(useAdminDirectConnectionsStatus, { wrapper });
  await waitFor(() => expect(poll.result.current.data).toEqual(cached));

  act(() => void poll.result.current.refetch());

  await waitFor(() => expect(poll.result.current.data).toEqual(incoming));
});
