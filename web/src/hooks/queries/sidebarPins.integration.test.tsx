import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SETTING_KEYS } from "@/lib/settingsContract";
import { useSidebarPins, useToggleSidebarPin } from "./sidebarPins";

const mocks = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  remoteValue: { items: [] } as { items: Array<Record<string, unknown>> },
  remoteLegacyValue: {} as Record<string, Array<Record<string, unknown>>>,
  profileId: "profile-1",
  profileToken: "fake",
  accessToken: "fake",
  authContextVersion: 1,
  serverOrigin: "https://server-1.example",
  capabilities: {
    api_version: 1,
    revision: 5,
    contract_etag: "revision-five",
    supports_batched_effective: true,
    supports_idempotent_writes: true,
    supports_atomic_shortcuts: true,
  } as {
    api_version: number;
    revision: number;
    contract_etag: string;
    supports_batched_effective?: boolean;
    supports_idempotent_writes?: boolean;
    supports_atomic_shortcuts?: boolean;
  },
}));

vi.mock("@/api/client", () => ({
  captureProfileRequestContext: () =>
    mocks.profileId && mocks.accessToken
      ? Object.fromEntries([
          ["profileId", mocks.profileId],
          ["profileToken", mocks.profileToken],
          ["accessToken", mocks.accessToken],
          ["authContextVersion", mocks.authContextVersion],
          ["serverOrigin", mocks.serverOrigin],
        ])
      : null,
  isProfileRequestContextCurrent: (snapshot: {
    accessToken: string;
    authContextVersion: number;
    serverOrigin: string;
  }) =>
    snapshot.accessToken === mocks.accessToken &&
    snapshot.authContextVersion === mocks.authContextVersion &&
    snapshot.serverOrigin === mocks.serverOrigin,
}));

vi.mock("@/utils/storage", () => ({
  storage: {
    KEYS: { PROFILE_ID: "profile_id" },
    get: () => mocks.profileId,
  },
}));

vi.mock("./settingValues", () => ({
  effectiveSettingsQueryKey: ({ keys }: { keys?: readonly string[] }) => [
    "settings",
    "values",
    "effective",
    "profile-1",
    keys?.join(",") ?? "*",
  ],
  useEffectiveSettings: ({ keys }: { keys?: readonly string[] } = {}) => ({
    data: keys?.includes(SETTING_KEYS.UI_SIDEBAR_PINS)
      ? {
          [SETTING_KEYS.UI_SIDEBAR_PINS]: {
            key: SETTING_KEYS.UI_SIDEBAR_PINS,
            value: mocks.remoteLegacyValue,
            source: "profile",
          },
        }
      : {
          [SETTING_KEYS.NAV_SHORTCUTS]: {
            key: SETTING_KEYS.NAV_SHORTCUTS,
            value: mocks.remoteValue,
            source: "profile",
          },
        },
    isLoading: false,
  }),
  useSettingsCapabilities: () => ({ data: mocks.capabilities, isLoading: false }),
  settingsCapabilitiesSupportKey: (
    capabilities:
      | {
          api_version: number;
          revision: number;
          supports_batched_effective?: boolean;
          supports_idempotent_writes?: boolean;
        }
      | undefined,
    key: string,
  ) =>
    capabilities?.api_version === 1 &&
    capabilities.revision >= (key === SETTING_KEYS.NAV_SHORTCUTS ? 5 : 1) &&
    capabilities.supports_batched_effective === true &&
    capabilities.supports_idempotent_writes === true,
  settingsCapabilitiesSupportAtomicShortcuts: (
    capabilities:
      | {
          api_version: number;
          revision: number;
          supports_batched_effective?: boolean;
          supports_idempotent_writes?: boolean;
          supports_atomic_shortcuts?: boolean;
        }
      | undefined,
  ) =>
    capabilities?.api_version === 1 &&
    capabilities.revision >= 5 &&
    capabilities.supports_batched_effective === true &&
    capabilities.supports_idempotent_writes === true &&
    capabilities.supports_atomic_shortcuts === true,
  useSetNavigationShortcutPresence: () => ({ mutateAsync: mocks.mutateAsync }),
}));

function deferred() {
  let resolve!: (value: unknown) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function wrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  };
}

describe("serialized sidebar pin writes", () => {
  beforeEach(() => {
    mocks.mutateAsync.mockReset();
    mocks.remoteValue = { items: [] };
    mocks.remoteLegacyValue = {};
    mocks.profileId = "profile-1";
    mocks.profileToken = "fake";
    mocks.accessToken = "fake";
    mocks.authContextVersion = 1;
    mocks.serverOrigin = "https://server-1.example";
    mocks.capabilities = {
      api_version: 1,
      revision: 5,
      contract_etag: "revision-five",
      supports_batched_effective: true,
      supports_idempotent_writes: true,
      supports_atomic_shortcuts: true,
    };
  });

  afterEach(cleanup);

  it("preserves the latest optimistic document across an intermediate refetch", async () => {
    const writes = [deferred(), deferred(), deferred()];
    mocks.mutateAsync.mockImplementation(
      () => writes[mocks.mutateAsync.mock.calls.length - 1]!.promise,
    );
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const { result, rerender } = renderHook(
      () => ({ pins: useSidebarPins(), toggle: useToggleSidebarPin() }),
      { wrapper: wrapper(queryClient) },
    );

    act(() => {
      result.current.toggle.togglePin(42, {
        type: "collection",
        id: "a",
        label: "A",
      });
      result.current.toggle.togglePin(42, {
        type: "section",
        id: "b",
        label: "B",
      });
    });
    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));

    // Simulate the first write's server event/refetch returning only A while B
    // remains queued. The local overlay must continue to be the edit base.
    mocks.remoteValue = {
      items: [{ type: "collection", library_id: 42, collection_id: "a", label: "A" }],
    };
    rerender();
    expect(result.current.pins.pins["42"]).toHaveLength(2);

    act(() => {
      result.current.toggle.togglePin(42, {
        type: "collection",
        id: "c",
        label: "C",
      });
    });

    writes[0]!.resolve({});
    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(2));
    expect(mocks.mutateAsync.mock.calls[1]![0]).toMatchObject({
      invalidateOnSettled: false,
      item: { type: "section", library_id: 42, section_id: "b", label: "B" },
      present: true,
      mutationId: expect.any(String),
    });

    writes[1]!.resolve({});
    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(3));
    expect(mocks.mutateAsync.mock.calls[2]![0]).toMatchObject({
      item: { type: "collection", library_id: 42, collection_id: "c", label: "C" },
      present: true,
      mutationId: expect.any(String),
    });
    writes[2]!.resolve({});
    await waitFor(() => expect(result.current.pins.pins["42"]).toHaveLength(1));
  });

  it("serializes rapid toggles as stable desired-state operations", async () => {
    mocks.mutateAsync.mockResolvedValue({});
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const { result } = renderHook(() => useToggleSidebarPin(), {
      wrapper: wrapper(queryClient),
    });
    const pin = { type: "collection" as const, id: "a", label: "A" };

    act(() => {
      result.current.togglePin(42, pin);
      result.current.togglePin(42, pin);
    });

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(2));
    expect(mocks.mutateAsync.mock.calls[0]![0]).toMatchObject({
      item: { type: "collection", library_id: 42, collection_id: "a", label: "A" },
      present: true,
      mutationId: expect.any(String),
    });
    expect(mocks.mutateAsync.mock.calls[1]![0]).toMatchObject({
      item: { type: "collection", library_id: 42, collection_id: "a", label: "A" },
      present: false,
      mutationId: expect.any(String),
    });
    expect(mocks.mutateAsync.mock.calls[1]![0].mutationId).not.toBe(
      mocks.mutateAsync.mock.calls[0]![0].mutationId,
    );
  });

  it("orders the same target across separate hook instances", async () => {
    const writes = [deferred(), deferred()];
    mocks.mutateAsync.mockImplementation(
      () => writes[mocks.mutateAsync.mock.calls.length - 1]!.promise,
    );
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const first = renderHook(() => useToggleSidebarPin(), { wrapper: wrapper(queryClient) });
    const second = renderHook(() => useToggleSidebarPin(), { wrapper: wrapper(queryClient) });
    const pin = { type: "section" as const, id: "recent", label: "Recent" };

    act(() => {
      first.result.current.togglePin(42, pin);
      second.result.current.togglePin(42, pin);
    });
    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    expect(mocks.mutateAsync.mock.calls[0]![0]).toMatchObject({ present: true });

    writes[0]!.resolve({});
    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(2));
    expect(mocks.mutateAsync.mock.calls[1]![0]).toMatchObject({ present: false });
    writes[1]!.resolve({});
  });

  it("keeps queued intent bound to the profile and PIN token captured at click time", async () => {
    const writes = [deferred(), deferred()];
    mocks.mutateAsync.mockImplementation(
      () => writes[mocks.mutateAsync.mock.calls.length - 1]!.promise,
    );
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const { result } = renderHook(() => useToggleSidebarPin(), {
      wrapper: wrapper(queryClient),
    });

    act(() => {
      result.current.togglePin(42, { type: "collection", id: "a", label: "A" });
      result.current.togglePin(42, { type: "collection", id: "b", label: "B" });
    });
    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));
    mocks.profileId = "profile-2";
    mocks.profileToken = "dummy";
    writes[0]!.resolve({});

    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(2));
    expect(mocks.mutateAsync.mock.calls[0]![0]).toMatchObject({
      profileAuth: {
        profileId: "profile-1",
        profileToken: "fake",
        accessToken: "fake",
        authContextVersion: 1,
        serverOrigin: "https://server-1.example",
      },
    });
    expect(mocks.mutateAsync.mock.calls[1]![0]).toMatchObject({
      profileAuth: { profileId: "profile-1", profileToken: "fake" },
    });
    writes[1]!.resolve({});
  });

  it("cancels queued intent after the account and server context switch", async () => {
    const writes = [deferred(), deferred()];
    mocks.mutateAsync.mockImplementation(
      () => writes[mocks.mutateAsync.mock.calls.length - 1]!.promise,
    );
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    const { result } = renderHook(() => useToggleSidebarPin(), {
      wrapper: wrapper(queryClient),
    });

    act(() => {
      result.current.togglePin(42, { type: "collection", id: "a", label: "A" });
      result.current.togglePin(42, { type: "collection", id: "b", label: "B" });
    });
    await waitFor(() => expect(mocks.mutateAsync).toHaveBeenCalledTimes(1));

    mocks.profileId = "profile-for-account-2";
    mocks.profileToken = "dummy";
    mocks.accessToken = "dummy";
    mocks.authContextVersion = 2;
    mocks.serverOrigin = "https://server-2.example";
    await act(async () => {
      writes[0]!.resolve({});
      await writes[0]!.promise;
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(mocks.mutateAsync).toHaveBeenCalledTimes(1);
    expect(invalidate).not.toHaveBeenCalled();
  });

  it("fails closed when the atomic shortcut capability is absent", () => {
    mocks.capabilities = {
      api_version: 1,
      revision: 5,
      contract_etag: "revision-five-without-atomic-shortcuts",
      supports_batched_effective: true,
      supports_idempotent_writes: true,
    };
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const { result } = renderHook(() => useToggleSidebarPin(), {
      wrapper: wrapper(queryClient),
    });

    expect(result.current.canToggle).toBe(false);
    act(() => {
      result.current.togglePin(42, { type: "collection", id: "a", label: "A" });
    });
    expect(mocks.mutateAsync).not.toHaveBeenCalled();
  });

  it("continues reading legacy sidebar pins from revision-four servers", () => {
    mocks.capabilities = {
      api_version: 1,
      revision: 4,
      contract_etag: "revision-four",
      supports_batched_effective: true,
      supports_idempotent_writes: true,
    };
    mocks.remoteLegacyValue = {
      "42": [{ type: "collection", id: "legacy", label: "Legacy pin" }],
    };
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });

    const { result } = renderHook(() => useSidebarPins(), {
      wrapper: wrapper(queryClient),
    });

    expect(result.current.pins["42"]).toEqual([
      { type: "collection", id: "legacy", label: "Legacy pin" },
    ]);
  });

  it("hides cached shortcut data when no profile is active", () => {
    mocks.remoteValue = {
      items: [{ type: "collection", library_id: 42, collection_id: "stale", label: "Stale" }],
    };
    mocks.profileId = "";
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });

    const { result } = renderHook(() => useSidebarPins(), {
      wrapper: wrapper(queryClient),
    });

    expect(result.current.pins).toEqual({});
  });
});
