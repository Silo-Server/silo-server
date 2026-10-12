import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import listEffectiveSettingsUnknownKey from "../../../../contracts/api/v2/fixtures/list_effective_settings_unknown_key.json";
import { v2Problem } from "@/api/v2/problems.test-support";
import { V2ProblemError } from "@/api/v2/request";
import { SETTING_KEYS } from "@/lib/settingsContract";
import {
  settingsCapabilitiesSupportAtomicShortcuts,
  settingsCapabilitiesSupportKey,
  type SettingIdentity,
  useClearSettingValue,
  useSetNavigationShortcutPresence,
  useSetSettingValue,
  useSettingsCapabilities,
  useSettingValue,
  useStoredSettingValues,
} from "./settingValues";

const v2Mock = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: v2Mock };
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
    v2Mock.mockClear();
  });

  it("reads the values stored at one scope, leaving out keys with nothing stored", async () => {
    v2Mock.mockResolvedValueOnce({
      revision: 8,
      items: [
        { key: "ui.title_art", scope: "profile_device", is_set: true, value: false },
        { key: "player.hdr_enabled", scope: "profile_device", is_set: false },
      ],
    });

    const { result } = renderHook(
      () =>
        useStoredSettingValues({
          keys: [SETTING_KEYS.UI_TITLE_ART, SETTING_KEYS.PLAYER_HDR_ENABLED],
          identity: { scope: "profile_device", deviceId: "apple-tv" },
        }),
      { wrapper },
    );

    await waitFor(() => expect(result.current.data).toEqual({ "ui.title_art": false }));
    expect(v2Mock).toHaveBeenCalledWith("GET /api/v2/settings/values", {
      query: expect.objectContaining({
        scope: "profile_device",
        device_id: "apple-tv",
        keys: [SETTING_KEYS.UI_TITLE_ART, SETTING_KEYS.PLAYER_HDR_ENABLED],
      }),
    });
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

    expect(v2Mock.mock.calls.map(([key]) => key)).toEqual([
      "PUT /api/v2/settings/values/{key}",
      "DELETE /api/v2/settings/values/{key}",
    ]);
    for (const [, options] of v2Mock.mock.calls) {
      expect(options.path).toEqual({ key: SETTING_KEYS.UI_CARD_PRESENTATION });
      // The identity carries only what the caller may address; the client
      // family comes from the request header, never from the query.
      expect(options.query).toEqual({
        scope: "profile_client",
        library_id: undefined,
        series_id: undefined,
        device_id: undefined,
        profile_id: undefined,
      });
      expect(options.query).not.toHaveProperty("client_family");
    }
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
        profileAuth,
        invalidateOnSettled: false,
      });
    });

    expect(v2Mock).toHaveBeenCalledWith("PUT /api/v2/settings/values/nav.shortcuts/item", {
      body: {
        item: { type: "library", library_id: 42, label: "Movies" },
        present: true,
      },
      profileContext: profileAuth,
    });
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
    ["an already-cleared 404 reset", v2Problem(404, "not_found", "Already cleared")],
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
    if (apiError) v2Mock.mockRejectedValueOnce(apiError);
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
    manifest_revision: 5,
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
        { ...revisionFive, manifest_revision: 4 },
        SETTING_KEYS.NAV_PRIMARY_MENU,
      ),
    ).toBe(false);
    expect(settingsCapabilitiesSupportKey(revisionFive, SETTING_KEYS.NAV_PRIMARY_MENU)).toBe(true);
  });

  it("requires batched reads but not idempotent replay", () => {
    expect(
      settingsCapabilitiesSupportKey(
        { ...revisionFive, supports_batched_effective: false },
        SETTING_KEYS.NAV_PRIMARY_MENU,
      ),
    ).toBe(false);
    const withoutBatch = {
      api_version: 1,
      manifest_revision: 5,
      contract_etag: "without-batch",
      supports_idempotent_writes: true,
    };
    const withoutIdempotency = {
      api_version: 1,
      manifest_revision: 5,
      contract_etag: "without-idempotency",
      supports_batched_effective: true,
    };
    expect(settingsCapabilitiesSupportKey(withoutBatch, SETTING_KEYS.NAV_PRIMARY_MENU)).toBe(false);
    // The v2 writes send no mutation id, so a server flag for it is not a
    // precondition of using the definition.
    expect(settingsCapabilitiesSupportKey(withoutIdempotency, SETTING_KEYS.NAV_PRIMARY_MENU)).toBe(
      true,
    );
  });

  it("does not report idempotent writes the v2 operations cannot carry", async () => {
    v2Mock.mockResolvedValueOnce({
      api_version: 1,
      manifest_revision: 5,
      contract_etag: "revision-five",
      definition_count: 1,
      scopes: [],
      client_families: [],
      supports_batched_effective: true,
      supports_idempotent_writes: true,
      supports_atomic_shortcuts: true,
    });
    const { result } = renderHook(() => useSettingsCapabilities(), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data).toMatchObject({
      supports_batched_effective: true,
      supports_idempotent_writes: false,
      supports_atomic_shortcuts: true,
    });
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

describe("keyed effective reads", () => {
  const effectiveCalls = () =>
    v2Mock.mock.calls.filter(
      ([operation]) => operation === "GET /api/v2/settings/values/effective",
    );

  afterEach(() => {
    cleanup();
    v2Mock.mockReset();
    v2Mock.mockResolvedValue(undefined);
  });

  it("shares one request between single-key reads started together", async () => {
    v2Mock.mockImplementation((operation: string) =>
      Promise.resolve(
        operation === "GET /api/v2/settings/values/effective" ? effectiveBody : undefined,
      ),
    );
    const effectiveBody = {
      items: [
        { key: SETTING_KEYS.UI_TITLE_ART, source: "profile", scope: "profile", value: false },
        { key: SETTING_KEYS.PLAYER_HDR_ENABLED, source: "default", value: true },
      ],
    };

    const { result } = renderHook(
      () => ({
        titleArt: useSettingValue<boolean>(SETTING_KEYS.UI_TITLE_ART),
        hdr: useSettingValue<boolean>(SETTING_KEYS.PLAYER_HDR_ENABLED),
      }),
      { wrapper },
    );

    await waitFor(() =>
      expect(result.current.titleArt.isSuccess && result.current.hdr.isSuccess).toBe(true),
    );
    expect(result.current.titleArt.value).toBe(false);
    expect(result.current.titleArt.source).toBe("profile");
    expect(result.current.hdr.source).toBe("default");
    expect(effectiveCalls()).toHaveLength(1);
    expect(v2Mock).toHaveBeenCalledWith("GET /api/v2/settings/values/effective", {
      query: expect.objectContaining({
        keys: expect.arrayContaining([SETTING_KEYS.UI_TITLE_ART, SETTING_KEYS.PLAYER_HDR_ENABLED]),
      }),
    });
  });

  it("fails every read that shared a failed request", async () => {
    v2Mock.mockImplementation((operation: string) =>
      operation === "GET /api/v2/settings/values/effective"
        ? Promise.reject(new Error("offline"))
        : Promise.resolve(undefined),
    );

    const { result } = renderHook(
      () => ({
        titleArt: useSettingValue(SETTING_KEYS.UI_TITLE_ART),
        hdr: useSettingValue(SETTING_KEYS.PLAYER_HDR_ENABLED),
      }),
      { wrapper },
    );

    await waitFor(() =>
      expect(result.current.titleArt.isError && result.current.hdr.isError).toBe(true),
    );
    expect(v2Mock).toHaveBeenCalledTimes(1);
  });

  it("keeps an unknown key's 422 with the read that asked for it", async () => {
    v2Mock.mockImplementation((operation: string, options?: { query?: { keys?: string[] } }) => {
      if (operation !== "GET /api/v2/settings/values/effective") return Promise.resolve(undefined);
      if (options?.query?.keys?.includes(SETTING_KEYS.PLAYER_HDR_ENABLED)) {
        return Promise.reject(
          new V2ProblemError(
            "GET /api/v2/settings/values/effective",
            listEffectiveSettingsUnknownKey,
            null,
          ),
        );
      }
      return Promise.resolve({
        items: [
          { key: SETTING_KEYS.UI_TITLE_ART, source: "profile", scope: "profile", value: false },
        ],
      });
    });

    const { result } = renderHook(
      () => ({
        titleArt: useSettingValue<boolean>(SETTING_KEYS.UI_TITLE_ART),
        hdr: useSettingValue<boolean>(SETTING_KEYS.PLAYER_HDR_ENABLED),
      }),
      { wrapper },
    );

    await waitFor(() =>
      expect(result.current.titleArt.isSuccess && result.current.hdr.isError).toBe(true),
    );
    expect(result.current.titleArt.value).toBe(false);
    expect(effectiveCalls()).toHaveLength(3);
  });
});
