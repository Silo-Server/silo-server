import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";

import { ApiClientError } from "@/api/client";
import { DEFAULT_SUBTITLE_APPEARANCE } from "@/lib/subtitleAppearance";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { useSubtitleAppearanceSetting } from "./subtitleAppearance";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return { ...actual, api: apiMock };
});

function createHarness() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { queryClient, wrapper };
}

/** The effective response the server sends for one resolved key. */
function effectiveResponse(value: unknown, source: string) {
  return {
    revision: 1,
    settings: [{ key: SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE, value, source }],
  };
}

describe("useSubtitleAppearanceSetting", () => {
  beforeEach(() => {
    apiMock.mockReset();
  });

  it("saves the device override at profile_device scope", async () => {
    apiMock.mockImplementation((path: string) => {
      if (path.startsWith("/settings/values/effective")) {
        return Promise.resolve(effectiveResponse(DEFAULT_SUBTITLE_APPEARANCE, "default"));
      }
      return Promise.resolve(undefined);
    });

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.appearance.fontSize).toBe("large"));

    await result.current.save({ ...DEFAULT_SUBTITLE_APPEARANCE, fontSize: "xlarge" });

    const write = apiMock.mock.calls.find(([path]) => (path as string).includes("?scope="));
    expect(write?.[0]).toBe(
      `/settings/values/${SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE}?scope=profile_device`,
    );
    expect(write?.[1]).toMatchObject({ method: "PUT" });
    // The contract types this value as an object, so the body carries the
    // object itself rather than the JSON-encoded string the legacy route took.
    expect(JSON.parse((write?.[1] as RequestInit).body as string)).toEqual({
      value: { ...DEFAULT_SUBTITLE_APPEARANCE, fontSize: "xlarge" },
    });
  });

  it("resets by clearing the device row so the profile value applies again", async () => {
    apiMock.mockImplementation((path: string) => {
      if (path.startsWith("/settings/values/effective")) {
        return Promise.resolve(effectiveResponse(DEFAULT_SUBTITLE_APPEARANCE, "profile_device"));
      }
      return Promise.resolve(undefined);
    });

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.hasDeviceOverride).toBe(true));

    await result.current.reset();

    const write = apiMock.mock.calls.find(([path]) => (path as string).includes("?scope="));
    expect(write?.[0]).toBe(
      `/settings/values/${SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE}?scope=profile_device`,
    );
    expect(write?.[1]).toMatchObject({ method: "DELETE" });
  });

  it("reports no device override when the value resolved from a wider scope", async () => {
    apiMock.mockResolvedValue(effectiveResponse(DEFAULT_SUBTITLE_APPEARANCE, "profile"));

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });

    await waitFor(() => expect(result.current.appearance.fontSize).toBe("large"));
    // A profile-wide appearance is not this device's override, so offering
    // "reset this device" would be a no-op the user cannot see the effect of.
    expect(result.current.hasDeviceOverride).toBe(false);
  });

  it("treats a reset with nothing stored as already done", async () => {
    apiMock.mockImplementation((path: string, options?: RequestInit) => {
      if (path.startsWith("/settings/values/effective")) {
        return Promise.resolve(effectiveResponse(DEFAULT_SUBTITLE_APPEARANCE, "profile_device"));
      }
      if (options?.method === "DELETE") {
        return Promise.reject(
          new ApiClientError(404, "not_found", "No value is set at this scope"),
        );
      }
      return Promise.resolve(undefined);
    });

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.hasDeviceOverride).toBe(true));

    // The canonical DELETE answers 404 for "nothing stored here", which for a
    // reset is the requested state rather than a failure.
    await expect(result.current.reset()).resolves.toBeUndefined();
  });
});
