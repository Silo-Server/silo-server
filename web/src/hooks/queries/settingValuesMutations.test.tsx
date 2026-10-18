import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { v2Problem } from "@/api/v2/problems.test-support";
import { SETTING_KEYS } from "@/lib/settingsContract";
import {
  adminKeys,
  catalogKeys,
  deviceKeys,
  libraryKeys,
  mediaSurfaceKeys,
  sectionKeys,
  settingsKeys,
  shuffleKeys,
} from "./keys";
import type { LibraryCollection } from "@/api/types";
import { serverCollectionPeek } from "@/lib/collections/peek";
import { adminPeekKey, profilePeekKey } from "@/lib/homeRows/peek";
import type { HomeRow } from "@/lib/homeRows/types";
import { pickerKeys } from "@/pages/watchtogether/room/picker/usePickerData";
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

  it("refreshes Home when Show new episodes first changes", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const homeKey = sectionKeys.homeItems("continue-watching");
    queryClient.setQueryData(homeKey, { section: { items: [] } });
    queryClient.setQueryData(mediaSurfaceKeys.refreshSignal(), 0);
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.UI_NEXT_UP_NEW_RELEASES_FIRST,
        value: true,
        identity: { scope: "profile" },
      });
    });
    expect(queryClient.getQueryState(homeKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBe(1);
  });

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

  it.each([
    SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
    SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES,
  ])("refreshes Home and library rows when %s changes", async (key) => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const homeKey = sectionKeys.homeItems("featured");
    const libraryKey = sectionKeys.libraryItems(7, "featured");
    queryClient.setQueryData(homeKey, { section: { items: [] } });
    queryClient.setQueryData(libraryKey, { section: { items: [] } });
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        key,
        value: key === SETTING_KEYS.CATALOG_METADATA_LANGUAGE ? "fr" : { ja: "original" },
        identity: { scope: "profile" },
      });
    });
    // Section cards carry localized text, so a cached German Home would
    // otherwise stay German after switching to French.
    expect(queryClient.getQueryState(homeKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(libraryKey)?.isInvalidated).toBe(true);
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

  it("marks shuffle, Home-row editor and Watch Together picker cards stale, not room presence", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const page = { kind: "home" } as const;
    const row = {
      id: "row-1",
      sectionType: "recently_added",
      config: {},
      itemLimit: 20,
    } as HomeRow;
    const localized = [
      shuffleKeys.detail("shuffle-1"),
      adminPeekKey(page, row),
      profilePeekKey(page, row),
      ["home-row-preview", "home", "{}", 12],
      ["curatedListPicker", "alien"],
      serverCollectionPeek(
        { id: "collection-1", updated_at: "2026-10-14" } as LibraryCollection,
        3,
        false,
      ).queryKey,
      pickerKeys.rows("room-1", []),
      pickerKeys.search("alien"),
      pickerKeys.recent("movie"),
    ];
    const presenceKey = ["watch-party", "presence", "room-1", null];
    for (const key of [...localized, presenceKey]) queryClient.setQueryData(key, {});
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
        value: "fr",
        identity: { scope: "profile" },
      });
    });
    for (const key of localized) {
      expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
    }
    expect(queryClient.getQueryState(presenceKey)?.isInvalidated).toBe(false);
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

  it("cancels a detail read still loading in the old language", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const detailKey = catalogKeys.itemDetail("movie-1");
    let resolveDetail!: (value: object) => void;
    const loading = queryClient
      .prefetchQuery({
        queryKey: detailKey,
        queryFn: () => new Promise<object>((resolve) => (resolveDetail = resolve)),
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
    resolveDetail({ content_id: "movie-1", overview: "Deutsch" });
    await loading;
    expect(queryClient.getQueryData(detailKey)).toBeUndefined();
  });

  it("leaves client-only admin state alone", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const scan = { id: "scan-1", status: "running" };
    // Fed only by the events socket; a refetch would replace it with [].
    const queryFn = vi.fn(() => Promise.resolve([] as object[]));
    renderHook(
      () =>
        useQuery({
          queryKey: adminKeys.activeScans(),
          queryFn,
          initialData: [scan],
          staleTime: Infinity,
        }),
      { wrapper },
    );
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
        value: "fr",
        identity: { scope: "profile" },
      });
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(queryFn).not.toHaveBeenCalled();
    expect(queryClient.getQueryData(adminKeys.activeScans())).toEqual([scan]);
  });

  it("settles the write without waiting for mounted screens to refetch", async () => {
    v2Mock.mockResolvedValueOnce({});
    const { queryClient, wrapper } = createHarness();
    const detailKey = catalogKeys.itemDetail("movie-1");
    let calls = 0;
    const { result: detail } = renderHook(
      () =>
        useQuery({
          queryKey: detailKey,
          // The first read answers; the refetch never does.
          queryFn: () =>
            calls++ === 0
              ? Promise.resolve({ content_id: "movie-1" })
              : new Promise<object>(() => undefined),
        }),
      { wrapper },
    );
    await waitFor(() => expect(detail.current.data).toBeDefined());
    const { result } = renderHook(() => useSetSettingValue(), { wrapper });
    await act(async () => {
      await result.current.mutateAsync({
        key: SETTING_KEYS.CATALOG_METADATA_LANGUAGE,
        value: "fr",
        identity: { scope: "profile" },
      });
    });
    expect(result.current.isPending).toBe(false);
    expect(queryClient.getQueryState(detailKey)?.fetchStatus).toBe("fetching");
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
