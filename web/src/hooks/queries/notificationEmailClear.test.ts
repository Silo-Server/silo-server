import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority, notificationScope } from "@/api/v2/notifications";
import { notificationKeys } from "./keys";
import { useClearEmailNotificationAddress } from "./notifications";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => {
  onlineManager.setOnline(true);
  cleanup();
  vi.unstubAllGlobals();
});

it("updates only the captured email preferences cache and rejects a stale receipt", async () => {
  const client = new QueryClient();
  const base = notificationKeys.emailPreferences();
  const own = [...base, notificationScope(captureNotificationAuthority())];
  const other = [...base, "other"];
  for (const key of [base, own, other]) client.setQueryData(key, [{ id: "one" }]);
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
    new Response(
      JSON.stringify({
        mode: "off",
        custom_email: "",
        pending_email: "",
        can_edit_address: true,
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useClearEmailNotificationAddress(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  await act(async () => {
    await result.current.mutateAsync();
  });
  expect(client.getQueryData(own)).toEqual({
    mode: "off",
    custom_email: "",
    pending_email: "",
    can_edit_address: true,
  });
  for (const key of [base, other]) expect(client.getQueryData(key)).toEqual([{ id: "one" }]);
  client.setQueryData(own, [{ id: "two" }]);
  fetch.mockImplementation(async () => {
    setProfileToken("new-pin");
    return new Response(
      JSON.stringify({ mode: "off", custom_email: "", pending_email: "", can_edit_address: true }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  });
  await act(async () => {
    await expect(result.current.mutateAsync()).rejects.toThrow();
  });
  expect(client.getQueryData(own)).toEqual([{ id: "two" }]);
  expect(fetch).toHaveBeenCalledTimes(2);
  client.clear();
});

it("does not replay address clear on authentication or service failure", async () => {
  const client = new QueryClient();
  const { result } = renderHook(() => useClearEmailNotificationAddress(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  for (const status of [401, 403, 500]) {
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
    vi.stubGlobal("fetch", fetch);
    await act(async () => {
      await expect(result.current.mutateAsync()).rejects.toThrow();
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(String(fetch.mock.calls[0]![0])).toContain(
      "/api/v2/notifications/email-preferences/address",
    );
    expect(fetch.mock.calls[0]![1].method).toBe("DELETE");
  }
  client.clear();
});

it.each(["mutate", "mutateAsync"] as const)(
  "keeps %s authority immutable across offline pause and replacement",
  async (method) => {
    const client = new QueryClient();
    const original = [
      ...notificationKeys.emailPreferences(),
      notificationScope(captureNotificationAuthority()),
    ];
    client.setQueryData(original, { custom_email: "original" });
    const fetch = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          mode: "off",
          custom_email: "",
          pending_email: "",
          can_edit_address: true,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetch);
    const { result, rerender } = renderHook(() => useClearEmailNotificationAddress(), {
      wrapper: ({ children }: { children: ReactNode }) =>
        createElement(QueryClientProvider, { client }, children),
    });
    onlineManager.setOnline(false);
    let completion: Promise<unknown> | undefined;
    act(() => {
      if (method === "mutateAsync")
        completion = result.current.mutateAsync().catch((error) => error);
      else result.current.mutate();
    });
    await waitFor(() => expect(result.current.isPaused).toBe(true));
    setProfileId("replacement");
    setProfileToken("replacement-pin");
    const replacement = [
      ...notificationKeys.emailPreferences(),
      notificationScope(captureNotificationAuthority()),
    ];
    client.setQueryData(replacement, { custom_email: "replacement" });
    rerender();
    await act(async () => {
      onlineManager.setOnline(true);
      await client.resumePausedMutations();
      await completion;
    });
    await waitFor(() => expect(result.current.isPending).toBe(false));
    expect(fetch).not.toHaveBeenCalled();
    expect(client.getQueryData(original)).toEqual({ custom_email: "original" });
    expect(client.getQueryData(replacement)).toEqual({ custom_email: "replacement" });
    client.clear();
  },
);
