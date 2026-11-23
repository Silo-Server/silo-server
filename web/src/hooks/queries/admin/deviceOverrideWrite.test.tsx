import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import { useAdminUserSettings, useUpdateAdminUserDeviceSetting } from "./users";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
const row = (fontSize: string, revision: number, updated_at: string) => ({
  key: "playback.subtitle_appearance",
  scope: "profile_device",
  profile_id: "profile-a",
  device_id: "device-a",
  value: { fontSize },
  revision,
  updated_at,
});
const valuesPage = (items: unknown[]) => json({ revision: 1, page: { has_more: false }, items });

beforeEach(() => {
  setAccessToken("test-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// The subtitle appearance editor builds each save on the stored row, and a
// reopened editor starts from it, so the row must show a save as soon as the
// save settles, whether or not the list's refetch has come back.
it.each([
  ["creating the override", []],
  ["changing the override", [row("small", 1, "2026-11-22T00:00:00Z")]],
])(
  "shows a device override save in the user's stored values once it settles (%s)",
  async (_case, stored) => {
    let gets = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        const method = init?.method ?? (input instanceof Request ? input.method : "GET");
        if (method === "PUT") return json(row("medium", 2, "2026-11-23T00:00:00Z"));
        if (url.includes("/settings/values")) {
          gets += 1;
          // The refetch after the save never answers.
          return gets === 1 ? valuesPage(stored) : new Promise<Response>(() => undefined);
        }
        return json({ items: [], page: { has_more: false } });
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children);
    const { result } = renderHook(
      () => ({ stored: useAdminUserSettings(1), save: useUpdateAdminUserDeviceSetting() }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.stored.isLoading).toBe(false));

    await act(() =>
      result.current.save.mutateAsync({
        userId: 1,
        profileId: "profile-a",
        deviceId: "device-a",
        key: "playback.subtitle_appearance",
        value: JSON.stringify({ fontSize: "medium" }),
      }),
    );

    await waitFor(() =>
      expect(result.current.stored.data).toMatchObject([
        {
          key: "playback.subtitle_appearance",
          scope: "profile_device",
          profile_id: "profile-a",
          device_id: "device-a",
          value: JSON.stringify({ fontSize: "medium" }),
          updated_at: "2026-11-23T00:00:00Z",
        },
      ]),
    );
  },
);
