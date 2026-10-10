// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider, useMutation } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { V2ProblemError, type Problem } from "@/api/v2/request";
import type { WatchProviderConnection } from "@/hooks/queries/watchProviders";

const mocks = vi.hoisted(() => ({
  updateConnection: vi.fn(),
  idleMutation: () => ({ mutate: () => {}, isPending: false, error: null }),
}));

vi.mock("@/hooks/queries/watchProviders", () => ({
  WatchProviderAuthMethod: { DeviceCode: "device_code", APIKey: "api_key" },
  useWatchProviders: () => ({
    data: { providers: [{ key: "trakt", display_name: "Trakt" }] },
    isLoading: false,
  }),
  useWatchProviderConnection: () => ({
    data: makeConnection(),
    isLoading: false,
    isFetching: false,
  }),
  useWatchProviderSyncRuns: () => ({ data: undefined }),
  useUpdateWatchProviderConnection: () =>
    useMutation({ retry: false, mutationFn: mocks.updateConnection }),
  useStartWatchProviderDeviceAuth: mocks.idleMutation,
  usePollWatchProviderDeviceAuth: mocks.idleMutation,
  useConnectWatchProviderAPIKey: mocks.idleMutation,
  useDeleteWatchProviderConnection: mocks.idleMutation,
  useTriggerWatchProviderSync: mocks.idleMutation,
}));

import WatchProvidersSettings from "./WatchProvidersSettings";

function makeConnection(): WatchProviderConnection {
  return {
    etag: '"v1"',
    provider: "trakt",
    display_name: "Trakt",
    capabilities: {
      import_watched: true,
      import_progress: true,
      export_watched: true,
      export_unwatched: false,
      import_favorites: false,
      export_favorites: false,
      remove_favorites: false,
      import_watchlist: false,
      export_watchlist: false,
      remove_watchlist: false,
      provides_watchlist_order: false,
      scrobble_playback: true,
      import_ratings: false,
      export_ratings: false,
      sync_dropped: false,
    },
    auth_method: "device_code",
    connected: true,
    provider_username: "alice",
    import_watched_enabled: false,
    import_progress_enabled: false,
    export_watched_enabled: false,
    export_unwatched_enabled: false,
    import_favorites_enabled: false,
    export_favorites_enabled: false,
    sync_favorite_removals_enabled: false,
    import_watchlist_enabled: false,
    export_watchlist_enabled: false,
    sync_watchlist_removals_enabled: false,
    sync_watchlist_order_enabled: false,
    scrobble_enabled: false,
    import_ratings_enabled: false,
    export_ratings_enabled: false,
    sync_dropped_enabled: false,
    credentials_configured: true,
  };
}

function conflictError() {
  return new V2ProblemError("updateWatchProviderConnection", {
    type: "https://siloserver.org/problems/precondition_failed",
    title: "Precondition failed",
    status: 412,
  } as Problem);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function findButton(container: HTMLElement, label: string) {
  const button = Array.from(container.querySelectorAll("button")).find(
    (candidate) => candidate.textContent?.trim() === label,
  );
  if (!button) throw new Error(`button not found: ${label}`);
  return button;
}

function settingsPanel(container: HTMLElement) {
  return container.querySelector("#watch-provider-trakt-settings");
}

// React Query delivers observer updates on a zero-delay timer.
async function flush() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

describe("WatchProvidersSettings", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    mocks.updateConnection.mockReset();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it("keeps the settings open while retrying a conflicted change", async () => {
    const firstSave = deferred<unknown>();
    const retry = deferred<unknown>();
    mocks.updateConnection
      .mockReturnValueOnce(firstSave.promise)
      .mockReturnValueOnce(retry.promise);

    const queryClient = new QueryClient();
    act(() => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <WatchProvidersSettings />
        </QueryClientProvider>,
      );
    });

    const toggle = findButton(container, "Settings");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(settingsPanel(container)).toBeNull();

    act(() => toggle.click());
    act(() => {
      (container.querySelector("#watch-provider-trakt-import-watched") as HTMLElement).click();
    });
    await flush();
    expect(mocks.updateConnection).toHaveBeenCalledTimes(1);

    // The user folds the section away while the save is still pending.
    act(() => toggle.click());
    expect(settingsPanel(container)).toBeNull();

    await act(async () => {
      firstSave.reject(conflictError());
      await firstSave.promise.catch(() => undefined);
    });
    await flush();

    // The conflict opens the section and the button reflects it.
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      "These settings changed elsewhere",
    );
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    act(() => findButton(container, "Apply my change").click());
    await flush();
    expect(mocks.updateConnection).toHaveBeenCalledTimes(2);
    expect(mocks.updateConnection.mock.calls[1]?.[0]).toEqual({ import_watched_enabled: true });

    // The retry clears the conflict, but the section stays open mid-save.
    expect(container.querySelector('[role="alert"]')).toBeNull();
    expect(settingsPanel(container)).not.toBeNull();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    await act(async () => {
      retry.resolve({});
      await retry.promise;
    });
    await flush();
    expect(settingsPanel(container)).not.toBeNull();

    // The button still closes the section once the conflict is gone.
    act(() => toggle.click());
    expect(settingsPanel(container)).toBeNull();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
  });
});
