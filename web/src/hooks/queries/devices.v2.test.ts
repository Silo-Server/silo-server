import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor, act } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useMyDevices, useForgetDevice, useClearDeviceSettings } from "./devices";
import { deviceKeys, settingsKeys } from "./keys";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", () => ({ v2: request }));
function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  return { client, wrapper };
}
const device = {
  device_id: "tv",
  device_name: "TV",
  device_platform: "tv",
  profile_id: "p",
  profile_name: "Profile",
  last_seen_at: "2026-01-02T03:04:05.000Z",
  changed_count: 1,
  is_current_device: false,
};

describe("device settings v2", () => {
  beforeEach(() => vi.resetAllMocks());
  it("follows continuation including an empty page and preserves household scope", async () => {
    request
      .mockResolvedValueOnce({ items: [], page: { has_more: true, next_cursor: "next" } })
      .mockResolvedValueOnce({ items: [device], page: { has_more: false, next_cursor: "" } });
    const { wrapper } = setup();
    const { result } = renderHook(() => useMyDevices({ household: true }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual([device]);
    expect(request).toHaveBeenNthCalledWith(2, "GET /api/v2/devices", {
      query: { scope: "household", limit: 200, cursor: "next" },
    });
  });
  it.each([true, false])(
    "targets the selected profile and invalidates after a failure (forget=%s)",
    async (forget) => {
      const { client, wrapper } = setup();
      const invalidate = vi.spyOn(client, "invalidateQueries");
      const failure = new Error("device disappeared");
      request.mockRejectedValueOnce(failure);
      const { result } = renderHook(() => (forget ? useForgetDevice() : useClearDeviceSettings()), {
        wrapper,
      });
      await act(async () => {
        await expect(
          result.current.mutateAsync({ deviceId: "tv/shared", profileId: "sibling" }),
        ).rejects.toThrow(failure);
      });
      expect(request).toHaveBeenCalledWith(
        forget
          ? "DELETE /api/v2/devices/{device_id}"
          : "DELETE /api/v2/devices/{device_id}/settings",
        { path: { device_id: "tv/shared" }, query: { profile_id: "sibling" } },
      );
      expect(invalidate).toHaveBeenCalledWith({ queryKey: deviceKeys.all });
      expect(invalidate).toHaveBeenCalledWith({ queryKey: [...settingsKeys.all, "values"] });
    },
  );
});
