import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useCreateAutoscanSource, useUpdateAutoscanSource } from "../useAutoscan";
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

for (const create of [true, false]) {
  const useWrite = () => {
    const add = useCreateAutoscanSource();
    const update = useUpdateAutoscanSource();
    const mutation = create ? add : update;
    return {
      ...mutation,
      submit: (input: ReturnType<typeof body>, onSuccess?: () => void) =>
        create
          ? add.mutate(input, { onSuccess })
          : update.mutate({ id: "source-a", body: input }, { onSuccess }),
    };
  };
  it(`${create ? "create" : "update"} copies nested draft before offline queue and succeeds once`, async () => {
    onlineManager.setOnline(false);
    const fetchMock = vi.fn().mockResolvedValue(response(create));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(useWrite, fixture());
    const draft = body();
    act(() => result.current.submit(draft));
    await waitFor(() => expect(result.current.isPaused).toBe(true));
    draft.path_rewrites[0]!.to = "/changed";
    draft.source_config.key = "changed";
    act(() => onlineManager.setOnline(true));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
    const sent = JSON.parse(fetchMock.mock.calls[0]![1].body);
    expect(sent.path_rewrites[0].to).toBe("/new");
    expect(sent.source_config.key).toBe("old");
    expect(fetchMock.mock.calls[0]![1].method).toBe(create ? "POST" : "PUT");
  });
  it(`${create ? "create" : "update"} rejects offline authority replacement`, async () => {
    onlineManager.setOnline(false);
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(useWrite, fixture());
    act(() => result.current.submit(body()));
    await waitFor(() => expect(result.current.isPaused).toBe(true));
    act(() => {
      setProfileToken("pin-b");
      onlineManager.setOnline(true);
    });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).not.toHaveBeenCalled();
  });
  it.each(["401", "network"])(
    `${create ? "create" : "update"} never replays %s under retry3`,
    async (failure) => {
      const fetchMock =
        failure === "network"
          ? vi.fn().mockRejectedValue(new Error("lost"))
          : vi.fn().mockResolvedValue(
              new Response(
                JSON.stringify({
                  type: "https://silo.dev/problems/authentication_required",
                  title: "Unauthorized",
                  status: 401,
                  detail: "Expired",
                  instance: "synthetic",
                }),
                { status: 401, headers: { "Content-Type": "application/problem+json" } },
              ),
            );
      vi.stubGlobal("fetch", fetchMock);
      const { result } = renderHook(useWrite, fixture());
      act(() => result.current.submit(body()));
      await waitFor(() => expect(result.current.isError).toBe(true));
      expect(fetchMock).toHaveBeenCalledOnce();
    },
  );
  it(`${create ? "create" : "update"} fences late receipt and caller callback`, async () => {
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
    const { result } = renderHook(useWrite, { wrapper });
    act(() => result.current.submit(body(), callback));
    await waitFor(() => expect(release).toBeTypeOf("function"));
    act(() => setProfileToken("pin-b"));
    await act(async () => release(response(create)));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(callback).not.toHaveBeenCalled();
    expect(invalidation).not.toHaveBeenCalled();
  });
}
