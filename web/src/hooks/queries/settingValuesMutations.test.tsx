import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { v2Problem } from "@/api/v2/problems.test-support";
import { SETTING_KEYS } from "@/lib/settingsContract";
import {
  catalogKeys,
  deviceKeys,
  libraryKeys,
  mediaSurfaceKeys,
  sectionKeys,
  settingsKeys,
} from "./keys";
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

  it.each([true, false])(
    "refreshes Home after an unmounted preference save (%s)",
    async (value) => {
      let resolveRequest!: (value: object) => void;
      v2Mock.mockReturnValueOnce(
        new Promise<object>((resolve) => {
          resolveRequest = resolve;
        }),
      );
      const { queryClient, wrapper } = createHarness();
      const homeKey = sectionKeys.homeItems("recent");
      queryClient.setQueryData(homeKey, { section: { items: [{ content_id: "watched" }] } });
      queryClient.setQueryData(mediaSurfaceKeys.refreshSignal(), 0);
      const { result, unmount } = renderHook(() => useSetSettingValue(), { wrapper });

      let save!: Promise<unknown>;
      act(() => {
        save = result.current.mutateAsync({
          key: SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS,
          value,
          identity: { scope: "profile" },
        });
      });
      await waitFor(() => expect(v2Mock).toHaveBeenCalled());
      unmount();
      resolveRequest({});
      await save;

      expect(queryClient.getQueryState(homeKey)?.isInvalidated).toBe(true);
      expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBe(1);
    },
  );

  it("refreshes Home when the preference is cleared", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const homeKey = sectionKeys.homeItems("recent");
    queryClient.setQueryData(homeKey, { section: { items: [] } });
    const { result } = renderHook(() => useClearSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS,
        identity: { scope: "profile" },
      });
    });
    expect(queryClient.getQueryState(homeKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBe(1);
  });

  it("keeps Home cached after an unrelated setting changes", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const homeKey = sectionKeys.homeItems("recent");
    queryClient.setQueryData(homeKey, { section: { items: [] } });
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.UI_THEME,
        value: "dark",
        identity: { scope: "profile" },
      });
    });
    expect(queryClient.getQueryState(homeKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBeUndefined();
  });

  it("refetches the library list when the profile hides or shows a library", async () => {
    v2Mock.mockResolvedValue({});
    const { queryClient, wrapper } = createHarness();
    const librariesKey = libraryKeys.user("profile-1");
    queryClient.setQueryData(librariesKey, [{ id: 1 }]);
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.UI_THEME,
        value: "dark",
        identity: { scope: "profile" },
      });
    });
    expect(queryClient.getQueryState(librariesKey)?.isInvalidated).toBe(false);
    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.UI_DISABLED_LIBRARY_IDS,
        value: [],
        identity: { scope: "profile" },
      });
    });
    expect(queryClient.getQueryState(librariesKey)?.isInvalidated).toBe(true);
  });

  it.each([
    SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
    SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES,
  ])("marks localized media stale when %s changes", async (key) => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const detailKey = catalogKeys.itemDetail("movie-1");
    const homeKey = sectionKeys.homeItems("recent");
    queryClient.setQueryData(detailKey, { content_id: "movie-1", overview: "English" });
    queryClient.setQueryData(homeKey, { section: { items: [] } });
    queryClient.setQueryData(mediaSurfaceKeys.refreshSignal(), 0);
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({ key, value: "de", identity: { scope: "profile" } });
    });
    expect(queryClient.getQueryState(detailKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(homeKey)?.isInvalidated).toBe(true);
    // The home signal is bumped, not reset by its own invalidation.
    expect(queryClient.getQueryState(mediaSurfaceKeys.refreshSignal())?.isInvalidated).toBe(false);
    expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBe(1);
  });

  it("cancels a Home row still loading in the old language", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const homeKey = sectionKeys.homeItems("recent");
    let resolveRow!: (value: object) => void;
    const loading = queryClient
      .fetchQuery({
        queryKey: homeKey,
        queryFn: () => new Promise<object>((resolve) => (resolveRow = resolve)),
      })
      .catch(() => undefined);
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
        value: "fr",
        identity: { scope: "profile" },
      });
    });
    resolveRow({ section: { items: [{ overview: "Deutsch" }] } });
    await loading;
    // The old-language answer did not land as fresh data.
    expect(queryClient.getQueryData(homeKey)).toBeUndefined();
  });

  it("keeps catalog reads cached after an unrelated setting changes", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const detailKey = catalogKeys.itemDetail("movie-1");
    queryClient.setQueryData(detailKey, { content_id: "movie-1" });
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.UI_THEME,
        value: "dark",
        identity: { scope: "profile" },
      });
    });
    expect(queryClient.getQueryState(detailKey)?.isInvalidated).toBe(false);
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
