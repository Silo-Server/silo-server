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
const valuesPage = (fontSize: string | null) =>
  json({
    revision: 1,
    page: { has_more: false },
    items:
      fontSize === null
        ? []
        : [
            {
              key: "playback.subtitle_appearance",
              scope: "profile_device",
              profile_id: "profile-a",
              device_id: "device-a",
              value: { fontSize },
            },
          ],
  });

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
// reopened editor starts from it, so the save may only settle once that row
// shows it.
it("settles a device override save once the user's stored values show it", async () => {
  let releaseRefetch!: () => void;
  const refetched = new Promise<void>((resolve) => (releaseRefetch = resolve));
  let gets = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input instanceof Request ? input.url : input);
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      if (method === "PUT") return json({ value: { fontSize: "medium" } });
      if (url.includes("/settings/values")) {
        gets += 1;
        if (gets === 1) return valuesPage(null);
        await refetched;
        return valuesPage("medium");
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

  let settled = false;
  act(() => {
    void result.current.save
      .mutateAsync({
        userId: 1,
        profileId: "profile-a",
        deviceId: "device-a",
        key: "playback.subtitle_appearance",
        value: JSON.stringify({ fontSize: "medium" }),
      })
      .then(() => (settled = true));
  });
  await waitFor(() => expect(gets).toBe(2));
  await act(async () => new Promise((resolve) => setTimeout(resolve, 20)));
  expect(settled).toBe(false);

  await act(async () => releaseRefetch());
  await waitFor(() => expect(settled).toBe(true));
  expect(result.current.stored.data[0]?.value).toContain("medium");
});
