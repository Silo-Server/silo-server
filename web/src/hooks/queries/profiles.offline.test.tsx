// A profile edit held until reconnect replaces whatever another browser
// changed the same field to in the meantime, so these writes go out at once
// and fail while offline, with the save-failed message rather than the
// browser's own network error text.
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import type { Profile } from "@/api/types";
import { useDeleteProfileAvatar, useUpdateProfile, useUploadProfileAvatar } from "./profiles";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

type Mutation = { isError: boolean };
type Row = {
  name: string;
  useHook: () => Mutation;
  send: (hook: Mutation) => void;
  failed: string;
};
function row<T extends Mutation>(useHook: () => T, send: (hook: T) => void, failed: string): Row {
  return { name: useHook.name, useHook, send: send as (hook: Mutation) => void, failed };
}

const profile = { id: "profile-a", avatar_source: "upload" } as Profile;

beforeEach(() => {
  setAccessToken("synthetic-access");
  setRefreshToken("synthetic-refresh");
  setProfileId("profile-a");
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
  vi.mocked(toast.error).mockClear();
});

it.each([
  row(
    useUpdateProfile,
    (h) => h.mutate({ id: "profile-a", body: { subtitle_mode: "always" } }),
    "Failed to save profile",
  ),
  row(
    useUploadProfileAvatar,
    (h) => h.mutate({ id: "profile-a", file: new File(["x"], "a.png", { type: "image/png" }) }),
    "Failed to upload avatar",
  ),
  row(useDeleteProfileAvatar, (h) => h.mutate(profile), "Failed to remove avatar"),
])(
  "$name sends an offline edit at once instead of holding it",
  async ({ useHook, send, failed }) => {
    onlineManager.setOnline(false);
    const fetchMock = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    vi.stubGlobal("fetch", fetchMock);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children);
    const { result } = renderHook(useHook, { wrapper });
    act(() => send(result.current));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalledWith(failed);
  },
);
