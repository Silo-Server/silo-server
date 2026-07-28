import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";

import { SETTING_KEYS } from "@/lib/settingsContract";
import { storage } from "@/utils/storage";
import {
  effectiveSettingsQueryKey,
  useSettingValue,
  useSettingValuesRealtime,
  type EffectiveSettingsMap,
} from "./settingValues";
import type { EventChannelHandlers } from "@/components/realtimeEventsContext";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return { ...actual, api: apiMock };
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
    apiMock.mockReset();
    subscriptions.length = 0;
    storage.set(storage.KEYS.PROFILE_ID, "profile-1");
  });

  it("subscribes the user_settings channel", () => {
    const { wrapper } = createHarness();
    render(<Subscriber />, { wrapper });
    expect(subscriptions.map((entry) => entry.channel)).toContain("user_settings");
  });

  it("refetches a mounted reader when another device changes this profile", async () => {
    const { wrapper } = createHarness();
    let served = "dark";
    apiMock.mockImplementation(() =>
      Promise.resolve({
        revision: 1,
        settings: [{ key: SETTING_KEYS.UI_THEME, value: served, source: "profile" }],
      }),
    );

    function Reader() {
      useSettingValuesRealtime();
      const { value } = useSettingValue<string>(SETTING_KEYS.UI_THEME);
      return <span data-testid="theme">{value}</span>;
    }

    const view = render(<Reader />, { wrapper });
    await waitFor(() => expect(view.getByTestId("theme").textContent).toBe("dark"));

    // The other device's write lands on the server; this tab only learns of it
    // through the channel.
    served = "light";
    subscriptions
      .find((entry) => entry.channel === "user_settings")
      ?.handlers?.onEvent?.(
        changedFrame({ key: SETTING_KEYS.UI_THEME, scope: "profile", profile_id: "profile-1" }),
      );

    await waitFor(() => expect(view.getByTestId("theme").textContent).toBe("light"));
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

  it("costs one invalidation pass per event without a manual refetch", () => {
    const { queryClient, wrapper } = createHarness();
    seedEffective(queryClient, "dark");
    render(<Subscriber />, { wrapper });
    const handlers = subscriptions.find((entry) => entry.channel === "user_settings")?.handlers;

    // A burst of writes (a settings screen saving several keys at once) must
    // not fan out into a fetch per event: nothing is observing these queries,
    // so invalidation marks them stale and issues no request at all.
    for (const key of [SETTING_KEYS.UI_THEME, SETTING_KEYS.UI_DATE_FORMAT]) {
      handlers?.onEvent?.(changedFrame({ key, scope: "profile", profile_id: "profile-1" }));
    }

    expect(apiMock).not.toHaveBeenCalled();
  });
});
