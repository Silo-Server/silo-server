import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  setAccessToken,
  setRefreshToken,
  setProfileId,
  setProfileToken,
  StaleApiRequestContextError,
} from "@/api/client";
import {
  useStartChatGPTLogin,
  useCompleteChatGPTLogin,
  useSelectChatGPTAccount,
  useDisconnectChatGPTAccount,
  useAdminAICapabilities,
  useChatGPTStatus,
  useChatGPTModels,
} from "./chatgpt";
import { captureProfileRequestContext } from "@/api/client";
import { adminSettingsKey } from "@/api/v2/adminSettingsSnapshot";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", () => ({ v2: request }));
afterEach(() => vi.unstubAllGlobals());
beforeEach(() => {
  request.mockReset();
  setAccessToken("account-a");
  setProfileId("profile-a");
  setProfileToken(null);
});

it("keeps bearer and PIN credentials out of query keys while fencing PIN changes", async () => {
  setAccessToken("synthetic-bearer-secret");
  setProfileToken("synthetic-pin-secret");
  request.mockResolvedValue({});
  const client = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const { rerender } = renderHook(
    () => {
      useAdminAICapabilities("gpt-6-luna");
      useChatGPTStatus(false);
      useChatGPTModels("account-a", true);
    },
    { wrapper },
  );
  await waitFor(() => expect(request).toHaveBeenCalledTimes(3));
  const oldKeys = client
    .getQueryCache()
    .getAll()
    .map((q) => q.queryKey);
  expect(JSON.stringify(oldKeys)).not.toContain("synthetic-bearer-secret");
  expect(JSON.stringify(oldKeys)).not.toContain("synthetic-pin-secret");
  expect(request.mock.calls[0]?.[1]?.profileContext).toMatchObject({
    accessToken: "synthetic-bearer-secret",
    profileToken: "synthetic-pin-secret",
  });
  setProfileToken("next-pin-secret");
  rerender();
  await waitFor(() => expect(request).toHaveBeenCalledTimes(6));
  const nextKeys = client
    .getQueryCache()
    .getAll()
    .map((q) => q.queryKey);
  expect(nextKeys).toHaveLength(6);
  expect(JSON.stringify(nextKeys)).not.toContain("next-pin-secret");
});

it("keeps refetched model catalogs tied to their account during selection", async () => {
  const client = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  let active = "a";
  request.mockImplementation(async (operation, options) => {
    if (operation.includes("/select")) {
      active = options.path.client_id;
      return {};
    }
    if (operation.endsWith("/models")) {
      const account = options.query?.client_id ?? active;
      return { models: [{ id: account + "-model", name: account }] };
    }
    return {};
  });
  const { result, rerender } = renderHook(
    ({ account }) => ({
      models: useChatGPTModels(account, true),
      select: useSelectChatGPTAccount(),
    }),
    { wrapper, initialProps: { account: "a" } },
  );
  await waitFor(() => expect(result.current.models.data?.models[0]?.id).toBe("a-model"));
  const accountAKey = client
    .getQueryCache()
    .getAll()
    .find((query) => query.queryKey.includes("models") && query.queryKey.includes("a"))!.queryKey;
  // Keep the A observer active while selection refetches it after B becomes active.
  await act(async () => {
    await result.current.select.mutateAsync("b");
  });
  expect(active).toBe("b");
  expect(client.getQueryData(accountAKey)).toMatchObject({ models: [{ id: "a-model" }] });
  rerender({ account: "b" });
  await waitFor(() => expect(result.current.models.data?.models[0]?.id).toBe("b-model"));
  await act(async () => {
    await result.current.select.mutateAsync("a");
  });
  rerender({ account: "a" });
  await waitFor(() => expect(result.current.models.data?.models[0]?.id).toBe("a-model"));
});

it.each([
  ["selection", useSelectChatGPTAccount],
  ["completed login", useCompleteChatGPTLogin],
] as const)("refreshes persisted model settings after %s", async (_name, hook) => {
  const client = new QueryClient();
  const invalidate = vi.spyOn(client, "invalidateQueries");
  request.mockResolvedValue({});
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const { result } = renderHook(
    () => {
      const mutation = hook();
      return { mutateAsync: (input: string): Promise<unknown> => mutation.mutateAsync(input) };
    },
    { wrapper },
  );
  await act(async () => {
    await result.current.mutateAsync("fixture");
  });
  expect(invalidate).toHaveBeenCalledWith({
    queryKey: adminSettingsKey(captureProfileRequestContext()!),
    exact: true,
  });
});

it.each([
  ["selection", useSelectChatGPTAccount],
  ["completed login", useCompleteChatGPTLogin],
] as const)("uses transaction-owned welcome metadata for %s", async (_name, hook) => {
  const client = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const { result } = renderHook(hook, { wrapper });
  for (const firstConnection of [true, false]) {
    request.mockImplementation(async (_operation, options) => {
      options.onResponse(
        new Response(null, {
          status: 204,
          headers: {
            "X-Silo-Settings-Before-ETag": "before",
            "X-Silo-Settings-After-ETag": "after",
            "X-Silo-ChatGPT-First-Connection": String(firstConnection),
          },
        }),
      );
    });
    await act(async () => {
      await expect(result.current.mutateAsync("fixture")).resolves.toMatchObject({
        beforeETag: "before",
        afterETag: "after",
        firstConnection,
      });
    });
  }
});

it.each([
  ["start", useStartChatGPTLogin],
  ["complete", useCompleteChatGPTLogin],
  ["select", useSelectChatGPTAccount],
  ["disconnect", useDisconnectChatGPTAccount],
] as const)(
  "rejects a stale %s result without invalidating the next authority",
  async (_name, hook) => {
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    let resolve!: (value: object) => void;
    request.mockImplementation(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    const { result, rerender } = renderHook(
      () => {
        const mutation = hook();
        return { mutateAsync: (input: string): Promise<unknown> => mutation.mutateAsync(input) };
      },
      { wrapper },
    );
    let pending!: Promise<unknown>;
    act(() => {
      pending = result.current.mutateAsync("fixture");
    });
    const rejected = expect(pending).rejects.toBeInstanceOf(StaleApiRequestContextError);
    await vi.waitFor(() => expect(request).toHaveBeenCalledOnce());
    expect(request.mock.calls[0]?.[1]).toMatchObject({
      profileContext: { profileId: "profile-a", accessToken: "account-a" },
    });
    setProfileId("profile-b");
    rerender();
    await act(async () => {
      resolve({});
      await rejected;
    });
    expect(invalidate).not.toHaveBeenCalled();
  },
);

it("refuses completion after an account switch before sending a request", async () => {
  const client = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const { result } = renderHook(useCompleteChatGPTLogin, { wrapper });
  setAccessToken("account-b");
  await act(async () => {
    await expect(result.current.mutateAsync("old-callback")).rejects.toBeInstanceOf(
      StaleApiRequestContextError,
    );
  });
  expect(request).not.toHaveBeenCalled();
});

it("rejects a result when authority changes while invalidation is pending", async () => {
  const client = new QueryClient();
  let finishInvalidation!: () => void;
  const invalidate = vi.spyOn(client, "invalidateQueries").mockImplementation(
    () =>
      new Promise<void>((resolve) => {
        finishInvalidation = resolve;
      }),
  );
  request.mockResolvedValue({ attempt_id: "old-attempt" });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const { result, rerender } = renderHook(useStartChatGPTLogin, { wrapper });
  let pending!: Promise<unknown>;
  act(() => {
    pending = result.current.mutateAsync(undefined);
  });
  const rejected = expect(pending).rejects.toBeInstanceOf(StaleApiRequestContextError);
  await vi.waitFor(() => expect(invalidate).toHaveBeenCalledOnce());
  setAccessToken("account-b");
  rerender();
  await act(async () => {
    finishInvalidation();
    await rejected;
  });
});

it("refreshes an expired Silo token while preserving the captured write authority", async () => {
  setRefreshToken("refresh-a");
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  request.mockImplementation(actual.v2);
  let writes = 0;
  const fetchMock = vi.fn<typeof fetch>(async (input) => {
    const url = String(input);
    if (url === "/api/v2/auth/refresh") {
      return new Response(
        JSON.stringify({ access_token: "fresh-a", refresh_token: "refresh-b", expires_in: 3600 }),
        { headers: { "Content-Type": "application/json" } },
      );
    }
    writes++;
    return writes === 1
      ? new Response(
          JSON.stringify({
            type: "about:blank",
            title: "Authentication required",
            status: 401,
            code: "token_refresh_required",
          }),
          { status: 401, headers: { "Content-Type": "application/problem+json" } },
        )
      : new Response(JSON.stringify({ revocation_confirmed: true }), {
          headers: { "Content-Type": "application/json" },
        });
  });
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const { result } = renderHook(useDisconnectChatGPTAccount, { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync("fixture")).resolves.toMatchObject({
      revocation_confirmed: true,
    });
  });
  expect(writes).toBe(2);
  expect(fetchMock.mock.calls.at(-1)?.[1]?.headers).toMatchObject({
    Authorization: "Bearer fresh-a",
    "X-Profile-Id": "profile-a",
  });
});
