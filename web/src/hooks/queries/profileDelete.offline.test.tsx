// A profile delete confirmed offline must fail at once. Held until reconnect,
// it would remove the profile and its watch history later, after the confirm
// dialog had already closed with nothing on screen saying a delete was pending.
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import { useDeleteAdminUserProfile } from "./admin/userProfiles";
import { useDeleteProfile } from "./profiles";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

beforeEach(() => {
  setAccessToken("synthetic-access");
  setRefreshToken("synthetic-refresh");
  setProfileId("profile-admin");
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
  vi.mocked(toast.error).mockClear();
});

it.each([
  { name: "household delete", useHook: () => useDeleteProfile() },
  { name: "admin delete", useHook: () => useDeleteAdminUserProfile(7) },
])("$name sends an offline delete at once instead of holding it", async ({ useHook }) => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const { result } = renderHook(useHook, { wrapper });
  act(() => result.current.mutate("profile-guest"));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalled();
  expect(toast.error).toHaveBeenCalled();
});
