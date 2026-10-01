import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";

import { v2Problem } from "@/api/v2/problems.test-support";
import { DEFAULT_SUBTITLE_APPEARANCE } from "@/lib/subtitleAppearance";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { useSubtitleAppearanceSetting } from "./subtitleAppearance";

const v2Mock = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: v2Mock };
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
    items: [
      { key: SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE, value, source, definition_revision: 1 },
    ],
  };
}

describe("useSubtitleAppearanceSetting", () => {
  beforeEach(() => {
    v2Mock.mockReset();
  });

  it("saves the device override at profile_device scope", async () => {
    v2Mock.mockImplementation((operation: string) => {
      if (operation === "GET /api/v2/settings/values/effective") {
        return Promise.resolve(effectiveResponse(DEFAULT_SUBTITLE_APPEARANCE, "default"));
      }
      return Promise.resolve(undefined);
    });

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.appearance.fontSize).toBe("large"));

    await result.current.save({ ...DEFAULT_SUBTITLE_APPEARANCE, fontSize: "xlarge" });

    const write = v2Mock.mock.calls.find(
      ([operation]) => operation !== "GET /api/v2/settings/values/effective",
    );
    expect(write?.[0]).toBe("PUT /api/v2/settings/values/{key}");
    expect(write?.[1]).toMatchObject({
      path: { key: SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE },
      query: { scope: "profile_device" },
      // The contract types this value as an object, so the body carries the
      // object itself rather than the JSON-encoded string the legacy route took.
      body: { value: { ...DEFAULT_SUBTITLE_APPEARANCE, fontSize: "xlarge" } },
    });
  });

  it("resets by clearing the device row so the profile value applies again", async () => {
    v2Mock.mockImplementation((operation: string) => {
      if (operation === "GET /api/v2/settings/values/effective") {
        return Promise.resolve(effectiveResponse(DEFAULT_SUBTITLE_APPEARANCE, "profile_device"));
      }
      return Promise.resolve(undefined);
    });

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.hasDeviceOverride).toBe(true));

    await result.current.reset();

    const write = v2Mock.mock.calls.find(
      ([operation]) => operation !== "GET /api/v2/settings/values/effective",
    );
    expect(write?.[0]).toBe("DELETE /api/v2/settings/values/{key}");
    expect(write?.[1]).toMatchObject({
      path: { key: SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE },
      query: { scope: "profile_device" },
    });
  });

  it("reports no device override when the value resolved from a wider scope", async () => {
    v2Mock.mockResolvedValue(effectiveResponse(DEFAULT_SUBTITLE_APPEARANCE, "profile"));

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });

    await waitFor(() => expect(result.current.appearance.fontSize).toBe("large"));
    // A profile-wide appearance is not this device's override, so offering
    // "reset this device" would be a no-op the user cannot see the effect of.
    expect(result.current.hasDeviceOverride).toBe(false);
  });

  it("treats a reset with nothing stored as already done", async () => {
    v2Mock.mockImplementation((operation: string) => {
      if (operation === "GET /api/v2/settings/values/effective") {
        return Promise.resolve(effectiveResponse(DEFAULT_SUBTITLE_APPEARANCE, "profile_device"));
      }
      if (operation.startsWith("DELETE ")) {
        return Promise.reject(v2Problem(404, "not_found", "No value is set at this scope"));
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
