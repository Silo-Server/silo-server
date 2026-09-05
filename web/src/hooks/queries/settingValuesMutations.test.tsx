import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { v2Problem } from "@/api/v2/problems.test-support";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { deviceKeys, settingsKeys } from "./keys";
import { useClearSettingValue, useSetSettingValue } from "./settingValues";

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

describe("typed setting mutations", () => {
  beforeEach(() => {
    v2Mock.mockReset();
  });

  it("does not invalidate effective settings after a definitive rejected write", async () => {
    v2Mock.mockRejectedValueOnce(v2Problem(429, "rate_limited", "rate limited"));
    const { queryClient, wrapper } = createHarness();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });

    await act(async () => {
      await expect(
        result.current.mutateAsync({
          key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
          value: { version: 1, libraries: {} },
          identity: { scope: "profile_device" },
        }),
      ).rejects.toThrow("rate limited");
    });

    expect(invalidateQueries).not.toHaveBeenCalled();
  });

  it("invalidates effective settings and device summaries after a successful device write", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });

    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
        value: { version: 1, libraries: {} },
        identity: { scope: "profile_device" },
      });
    });

    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: [...settingsKeys.all, "values"],
    });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: deviceKeys.all });
  });

  it("reconciles effective settings and device summaries after an ambiguous write failure", async () => {
    v2Mock.mockRejectedValueOnce(new TypeError("network connection lost"));
    const { queryClient, wrapper } = createHarness();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });

    await act(async () => {
      await expect(
        result.current.mutateAsync({
          key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
          value: { version: 1, libraries: {} },
          identity: { scope: "profile_device" },
        }),
      ).rejects.toThrow("network connection lost");
    });

    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: [...settingsKeys.all, "values"],
    });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: deviceKeys.all });
  });

  it("reconciles effective settings and device summaries after a server write failure", async () => {
    v2Mock.mockRejectedValueOnce(v2Problem(503, "unavailable", "service unavailable"));
    const { queryClient, wrapper } = createHarness();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });

    await act(async () => {
      await expect(
        result.current.mutateAsync({
          key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
          value: { version: 1, libraries: {} },
          identity: { scope: "profile_device" },
        }),
      ).rejects.toThrow("service unavailable");
    });

    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: [...settingsKeys.all, "values"],
    });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: deviceKeys.all });
  });

  it("does not invalidate effective settings after a definitive rejected clear", async () => {
    v2Mock.mockRejectedValueOnce(v2Problem(429, "rate_limited", "rate limited"));
    const { queryClient, wrapper } = createHarness();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useClearSettingValue(), { wrapper });

    await act(async () => {
      await expect(
        result.current.mutateAsync({
          key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
          identity: { scope: "profile_device" },
        }),
      ).rejects.toThrow("rate limited");
    });

    expect(invalidateQueries).not.toHaveBeenCalled();
  });

  it("reconciles effective settings and device summaries after an already-cleared response", async () => {
    v2Mock.mockRejectedValueOnce(v2Problem(404, "not_found", "setting not found"));
    const { queryClient, wrapper } = createHarness();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useClearSettingValue(), { wrapper });

    await act(async () => {
      await expect(
        result.current.mutateAsync({
          key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
          identity: { scope: "profile_device" },
        }),
      ).rejects.toThrow("setting not found");
    });

    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: [...settingsKeys.all, "values"],
    });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: deviceKeys.all });
  });

  it("invalidates effective settings and device summaries after a successful device clear", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useClearSettingValue(), { wrapper });

    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
        identity: { scope: "profile_device" },
      });
    });

    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: [...settingsKeys.all, "values"],
    });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: deviceKeys.all });
  });

  it("reconciles effective settings and device summaries after an ambiguous clear failure", async () => {
    v2Mock.mockRejectedValueOnce(new TypeError("response connection lost"));
    const { queryClient, wrapper } = createHarness();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useClearSettingValue(), { wrapper });

    await act(async () => {
      await expect(
        result.current.mutateAsync({
          key: SETTING_KEYS.UI_LIBRARY_PAGE_STATE,
          identity: { scope: "profile_device" },
        }),
      ).rejects.toThrow("response connection lost");
    });

    expect(invalidateQueries).toHaveBeenCalledWith({
      queryKey: [...settingsKeys.all, "values"],
    });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: deviceKeys.all });
  });
});
