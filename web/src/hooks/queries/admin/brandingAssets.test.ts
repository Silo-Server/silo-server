import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useUploadBrandingAsset } from "./branding";
import { adminKeys, themeKeys } from "../keys";

const uploaded = () =>
  new Response(
    JSON.stringify({
      kind: "wordmark",
      ref: "abc.webp",
      url: "/api/v1/branding/assets/wordmark?v=abc.webp",
    }),
    { headers: { "Content-Type": "application/json" } },
  );
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
const png = () => new File(["\x89PNG"], "logo.png", { type: "image/png" });

it("uploads the captured file once as multipart to the v2 slot and invalidates branding", async () => {
  const fetchMock = vi.fn().mockResolvedValue(uploaded());
  vi.stubGlobal("fetch", fetchMock);
  const fx = fixture();
  const invalidate = vi.spyOn(fx.client, "invalidateQueries");
  const { result } = renderHook(useUploadBrandingAsset, fx);
  act(() => result.current.mutate({ kind: "wordmark", file: png() }));
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(String(url)).toBe("/api/v2/admin/branding/assets/wordmark");
  expect(init.method).toBe("POST");
  expect(init.body).toBeInstanceOf(FormData);
  expect((init.body as FormData).get("file")).toBeInstanceOf(Blob);
  expect((init.headers as Record<string, string>)["Content-Type"]).toBeUndefined();
  expect((init.headers as Record<string, string>)["X-Profile-Id"]).toBe("profile-a");
  expect(invalidate).toHaveBeenCalledWith({ queryKey: themeKeys.branding() });
  expect(invalidate).toHaveBeenCalledWith({ queryKey: adminKeys.serverSettings() });
});
