import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  setAccessToken,
  setRefreshToken,
  setProfileId,
  StaleApiRequestContextError,
} from "@/api/client";
import {
  useStartChatGPTLogin,
  useCompleteChatGPTLogin,
  useSelectChatGPTAccount,
  useDisconnectChatGPTAccount,
} from "./chatgpt";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", () => ({ v2: request }));
afterEach(() => vi.unstubAllGlobals());
beforeEach(() => {
  request.mockReset();
  setAccessToken("account-a");
  setProfileId("profile-a");
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
