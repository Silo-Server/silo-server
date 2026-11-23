// An admin's profile edit held until reconnect replaces whatever the household
// changed on that profile in the meantime, parental limits included, so the
// edit goes out at once and fails while offline, with the save-failed message
// rather than the browser's own network error text.
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import { useUpdateAdminUserProfile } from "./userProfiles";

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

it("sends an offline admin profile edit at once instead of holding it", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const { result } = renderHook(() => useUpdateAdminUserProfile(7), { wrapper });
  act(() => result.current.mutate({ id: "profile-kid", body: { max_content_rating: "PG" } }));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalled();
  expect(toast.error).toHaveBeenCalledWith("Couldn't save the profile");
});
