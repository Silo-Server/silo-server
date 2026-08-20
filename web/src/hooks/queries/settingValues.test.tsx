import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiClientError } from "@/api/client";
import { SETTING_KEYS } from "@/lib/settingsContract";
import {
  settingsCapabilitiesSupportAtomicShortcuts,
  settingsCapabilitiesSupportKey,
  type SettingIdentity,
  useClearSettingValue,
  useSetNavigationShortcutPresence,
  useSetSettingValue,
} from "./settingValues";

const apiMock = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
const apiWithProfileRequestContextMock = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return {
    ...actual,
    api: apiMock,
    apiWithProfileRequestContext: apiWithProfileRequestContextMock,
  };
});

function wrapper({ children }: { children: ReactNode }) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

describe("self-service setting identities", () => {
  afterEach(() => {
    cleanup();
    apiMock.mockClear();
    apiWithProfileRequestContextMock.mockClear();
  });

  it("cannot address another client family through a query parameter", async () => {
    const staleIdentity = {
      scope: "profile_client",
      // @ts-expect-error Client family comes only from the canonical API header.
      clientFamily: "tv",
    } satisfies SettingIdentity;
    const setHook = renderHook(() => useSetSettingValue(), { wrapper });
    const clearHook = renderHook(() => useClearSettingValue(), { wrapper });

    await act(async () => {
      await setHook.result.current.mutateAsync({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        value: { poster_size: "large", caption: "artwork" },
        identity: staleIdentity,
      });
      await clearHook.result.current.mutateAsync({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        identity: staleIdentity,
      });
    });

    expect(apiMock.mock.calls.map(([path]) => path)).toEqual([
      `/settings/values/${SETTING_KEYS.UI_CARD_PRESENTATION}?scope=profile_client`,
      `/settings/values/${SETTING_KEYS.UI_CARD_PRESENTATION}?scope=profile_client`,
    ]);
    expect(apiMock.mock.calls.map(([, options]) => options?.method)).toEqual(["PUT", "DELETE"]);
  });

  it("sends an atomic shortcut with its captured profile and PIN token", async () => {
    const shortcutHook = renderHook(() => useSetNavigationShortcutPresence(), { wrapper });

    const profileAuth = {
      profileId: "profile-old",
      profileToken: "fake",
      accessToken: "fake",
      authContextVersion: 3,
      serverOrigin: "https://old.example",
    };
    await act(async () => {
      await shortcutHook.result.current.mutateAsync({
        item: { type: "library", library_id: 42, label: "Movies" },
        present: true,
        mutationId: "mutation-1",
        profileAuth,
        invalidateOnSettled: false,
      });
    });

    expect(apiWithProfileRequestContextMock).toHaveBeenCalledWith(
      "/settings/values/nav.shortcuts/item",
      profileAuth,
      {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          "X-Silo-Mutation-Id": "mutation-1",
        },
        body: JSON.stringify({
          item: { type: "library", library_id: 42, label: "Movies" },
          present: true,
        }),
      },
    );
  });

  it("keeps an ordinary write pending until effective values reconcile", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    let releaseInvalidation!: () => void;
    const invalidation = new Promise<void>((resolve) => {
      releaseInvalidation = resolve;
    });
    const invalidateQueries = vi
      .spyOn(queryClient, "invalidateQueries")
      .mockReturnValue(invalidation);
    const localWrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const setHook = renderHook(() => useSetSettingValue(), { wrapper: localWrapper });

    let settled = false;
    const mutation = setHook.result.current
      .mutateAsync({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        value: { poster_size: "compact", caption: "title" },
        identity: { scope: "profile_client" },
      })
      .then(() => {
        settled = true;
      });

    await waitFor(() => expect(invalidateQueries).toHaveBeenCalled());
    expect(setHook.result.current.isPending).toBe(true);
    expect(settled).toBe(false);

    await act(async () => {
      releaseInvalidation();
      await mutation;
    });
    await waitFor(() => expect(setHook.result.current.isPending).toBe(false));
    expect(settled).toBe(true);
  });

  it.each([
    ["a successful reset", undefined],
    ["an already-cleared 404 reset", new ApiClientError(404, "not_found", "Already cleared")],
  ])("keeps %s pending until effective values reconcile", async (_label, apiError) => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    let releaseInvalidation!: () => void;
    const invalidation = new Promise<void>((resolve) => {
      releaseInvalidation = resolve;
    });
    const invalidateQueries = vi
      .spyOn(queryClient, "invalidateQueries")
      .mockReturnValue(invalidation);
    const localWrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    if (apiError) apiMock.mockRejectedValueOnce(apiError);
    const clearHook = renderHook(() => useClearSettingValue(), { wrapper: localWrapper });

    let settled = false;
    const mutation = clearHook.result.current
      .mutateAsync({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        identity: { scope: "profile_client" },
      })
      .catch((error: unknown) => {
        if (!apiError) throw error;
        expect(error).toBe(apiError);
      })
      .then(() => {
        settled = true;
      });

    await waitFor(() => expect(invalidateQueries).toHaveBeenCalled());
    expect(clearHook.result.current.isPending).toBe(true);
    expect(settled).toBe(false);

    await act(async () => {
      releaseInvalidation();
      await mutation;
    });
    await waitFor(() => expect(clearHook.result.current.isPending).toBe(false));
    expect(settled).toBe(true);
  });
});

describe("settings capability gates", () => {
  const revisionFive = {
    api_version: 1,
    revision: 5,
    contract_etag: "revision-five",
    supports_batched_effective: true,
    supports_idempotent_writes: true,
  };

  it("requires a compatible API and the definition's introduced revision", () => {
    expect(settingsCapabilitiesSupportKey(undefined, SETTING_KEYS.NAV_PRIMARY_MENU)).toBe(false);
    expect(
      settingsCapabilitiesSupportKey(
        { ...revisionFive, api_version: 2 },
        SETTING_KEYS.NAV_PRIMARY_MENU,
      ),
    ).toBe(false);
    expect(
      settingsCapabilitiesSupportKey(
        { ...revisionFive, revision: 4 },
        SETTING_KEYS.NAV_PRIMARY_MENU,
      ),
    ).toBe(false);
    expect(settingsCapabilitiesSupportKey(revisionFive, SETTING_KEYS.NAV_PRIMARY_MENU)).toBe(true);
  });

  it("requires batched reads and idempotent replay semantics", () => {
    expect(
      settingsCapabilitiesSupportKey(
        { ...revisionFive, supports_batched_effective: false },
        SETTING_KEYS.NAV_PRIMARY_MENU,
      ),
    ).toBe(false);
    expect(
      settingsCapabilitiesSupportKey(
        { ...revisionFive, supports_idempotent_writes: false },
        SETTING_KEYS.NAV_PRIMARY_MENU,
      ),
    ).toBe(false);
    const withoutBatch = {
      api_version: 1,
      revision: 5,
      contract_etag: "without-batch",
      supports_idempotent_writes: true,
    };
    const withoutIdempotency = {
      api_version: 1,
      revision: 5,
      contract_etag: "without-idempotency",
      supports_batched_effective: true,
    };
    expect(settingsCapabilitiesSupportKey(withoutBatch, SETTING_KEYS.NAV_PRIMARY_MENU)).toBe(false);
    expect(settingsCapabilitiesSupportKey(withoutIdempotency, SETTING_KEYS.NAV_PRIMARY_MENU)).toBe(
      false,
    );
  });

  it("requires an explicit atomic-shortcut capability", () => {
    expect(settingsCapabilitiesSupportAtomicShortcuts(revisionFive)).toBe(false);
    expect(
      settingsCapabilitiesSupportAtomicShortcuts({
        ...revisionFive,
        supports_atomic_shortcuts: false,
      }),
    ).toBe(false);
    expect(
      settingsCapabilitiesSupportAtomicShortcuts({
        ...revisionFive,
        supports_atomic_shortcuts: true,
      }),
    ).toBe(true);
  });
});
