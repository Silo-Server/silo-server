import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";

import { SETTING_KEYS } from "@/lib/settingsContract";
import { libraryKeys, mediaSurfaceKeys, sectionKeys } from "./keys";
import { storage } from "@/utils/storage";
import {
  effectiveSettingsQueryKey,
  useSettingValuesRealtime,
  type EffectiveSettingsMap,
} from "./settingValues";
import type { EventChannelHandlers } from "@/components/realtimeEventsContext";

const v2Mock = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: v2Mock };
});

// The provider owns the websocket; the hook under test only cares that it
// subscribes the right channel and what it does with a frame. Capturing the
// handlers lets the test push a frame the way the socket would.
const subscriptions = vi.hoisted(
  () => [] as { channel: string; handlers?: EventChannelHandlers }[],
);
vi.mock("@/components/realtimeEventsContext", async () => {
  const { useEffect } = await vi.importActual<typeof import("react")>("react");
  return {
    useEventChannel: (channel: string, handlers?: EventChannelHandlers) => {
      useEffect(() => {
        const entry = { channel, handlers };
        subscriptions.push(entry);
        return () => {
          const index = subscriptions.indexOf(entry);
          if (index >= 0) subscriptions.splice(index, 1);
        };
      }, [channel, handlers]);
    },
  };
});

function Subscriber() {
  useSettingValuesRealtime();
  return null;
}

function createHarness() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { queryClient, wrapper };
}

function changedFrame(data: { key: string; scope: string; profile_id?: string }) {
  return {
    type: "event",
    channel: "user_settings",
    event: "user_settings.changed",
    event_id: "e1",
    timestamp: new Date().toISOString(),
    data,
  };
}

/** Seeds a resolved value the way a mounted screen's read would have. */
function seedEffective(queryClient: QueryClient, value: string) {
  const key = effectiveSettingsQueryKey({ keys: [SETTING_KEYS.UI_THEME] });
  queryClient.setQueryData<EffectiveSettingsMap>(key, {
    [SETTING_KEYS.UI_THEME]: { key: SETTING_KEYS.UI_THEME, value, source: "profile" },
  });
  return key;
}

describe("useSettingValuesRealtime", () => {
  beforeEach(() => {
    v2Mock.mockReset();
    subscriptions.length = 0;
    storage.set(storage.KEYS.PROFILE_ID, "profile-1");
  });

  it.each(["profile-1", "profile-2"])(
    "refreshes Home only for its active profile (%s)",
    async (profileId) => {
      const { queryClient, wrapper } = createHarness();
      const homeKey = sectionKeys.homeItems("recent");
      queryClient.setQueryData(homeKey, { section: { items: [{ content_id: "watched" }] } });
      queryClient.setQueryData(mediaSurfaceKeys.refreshSignal(), 0);
      render(<Subscriber />, { wrapper });
      subscriptions
        .find((entry) => entry.channel === "user_settings")
        ?.handlers?.onEvent?.(
          changedFrame({
            key: SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS,
            scope: "profile",
            profile_id: profileId,
          }),
        );
      await waitFor(() => {
        expect(queryClient.getQueryState(homeKey)?.isInvalidated).toBe(profileId === "profile-1");
        expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBe(
          profileId === "profile-1" ? 1 : 0,
        );
      });
    },
  );

  // Another node can answer the first re-read before it has reloaded the
  // administrator's defaults, so a server-default change is re-read once more
  // after the server's cache TTL; a profile's own change is not.
  it.each([
    ["default", 2],
    ["profile", 1],
  ])("re-reads Home after a %s-scope Next Up change %d time(s)", async (scope, refreshes) => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const { queryClient, wrapper } = createHarness();
      queryClient.setQueryData(mediaSurfaceKeys.refreshSignal(), 0);
      render(<Subscriber />, { wrapper });
      subscriptions
        .find((entry) => entry.channel === "user_settings")
        ?.handlers?.onEvent?.(
          changedFrame({
            key: SETTING_KEYS.UI_NEXT_UP_MODE,
            scope,
            ...(scope === "profile" ? { profile_id: "profile-1" } : {}),
          }),
        );
      await vi.advanceTimersByTimeAsync(0);
      expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBe(1);
      await vi.advanceTimersByTimeAsync(11_000);
      expect(queryClient.getQueryData(mediaSurfaceKeys.refreshSignal())).toBe(refreshes);
    } finally {
      vi.useRealTimers();
    }
  });

  it("refetches the library list when another device hides or shows a library", async () => {
    const { queryClient, wrapper } = createHarness();
    const librariesKey = libraryKeys.user("profile-1");
    queryClient.setQueryData(librariesKey, [{ id: 1 }]);
    render(<Subscriber />, { wrapper });
    subscriptions
      .find((entry) => entry.channel === "user_settings")
      ?.handlers?.onEvent?.(
        changedFrame({
          key: SETTING_KEYS.UI_DISABLED_LIBRARY_IDS,
          scope: "profile",
          profile_id: "profile-1",
        }),
      );
    await waitFor(() => expect(queryClient.getQueryState(librariesKey)?.isInvalidated).toBe(true));
  });

  it("ignores a change addressed to another profile on the same account", () => {
    const { queryClient, wrapper } = createHarness();
    const key = seedEffective(queryClient, "dark");
    render(<Subscriber />, { wrapper });

    subscriptions
      .find((entry) => entry.channel === "user_settings")
      ?.handlers?.onEvent?.(
        changedFrame({ key: SETTING_KEYS.UI_THEME, scope: "profile", profile_id: "profile-2" }),
      );

    expect(queryClient.getQueryState(key)?.isInvalidated ?? false).toBe(false);
  });

  it("invalidates for an account-scoped change, which carries no profile", () => {
    const { queryClient, wrapper } = createHarness();
    const key = seedEffective(queryClient, "dark");
    render(<Subscriber />, { wrapper });

    subscriptions
      .find((entry) => entry.channel === "user_settings")
      ?.handlers?.onEvent?.(changedFrame({ key: SETTING_KEYS.UI_THEME, scope: "account" }));

    expect(queryClient.getQueryState(key)?.isInvalidated ?? false).toBe(true);
  });

  it("ignores frames from other events on the channel", () => {
    const { queryClient, wrapper } = createHarness();
    const key = seedEffective(queryClient, "dark");
    render(<Subscriber />, { wrapper });

    subscriptions
      .find((entry) => entry.channel === "user_settings")
      ?.handlers?.onEvent?.({
        type: "event",
        channel: "user_settings",
        event: "something.else",
        data: null,
      });

    expect(queryClient.getQueryState(key)?.isInvalidated ?? false).toBe(false);
  });
});
