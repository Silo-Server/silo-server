import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import {
  useAdminUserSettings,
  useDeleteAdminUserDeviceSetting,
  useDeleteAllAdminUserDeviceSettingsForDevice,
  useUpdateAdminUserDeviceSetting,
} from "./users";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
const row = (fontSize: string, revision: number, updated_at: string) => ({
  key: "playback.subtitle_appearance",
  scope: "profile_device",
  profile_id: "profile-a",
  device_id: "device-a",
  value: { fontSize },
  revision,
  updated_at,
});
const valuesPage = (items: unknown[]) => json({ revision: 1, page: { has_more: false }, items });

beforeEach(() => {
  setAccessToken("test-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// The subtitle appearance editor builds each save on the stored row, and a
// reopened editor starts from it, so the row must show a save as soon as the
// save settles, whether or not the list's refetch has come back.
it.each([
  ["creating the override", []],
  ["changing the override", [row("small", 1, "2026-11-22T00:00:00Z")]],
])(
  "shows a device override save in the user's stored values once it settles (%s)",
  async (_case, stored) => {
    let gets = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        const method = init?.method ?? (input instanceof Request ? input.method : "GET");
        if (method === "PUT") return json(row("medium", 2, "2026-11-23T00:00:00Z"));
        if (url.includes("/settings/values")) {
          gets += 1;
          // The refetch after the save never answers.
          return gets === 1 ? valuesPage(stored) : new Promise<Response>(() => undefined);
        }
        return json({ items: [], page: { has_more: false } });
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children);
    const { result } = renderHook(
      () => ({ stored: useAdminUserSettings(1), save: useUpdateAdminUserDeviceSetting() }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.stored.isLoading).toBe(false));

    await act(() =>
      result.current.save.mutateAsync({
        userId: 1,
        profileId: "profile-a",
        deviceId: "device-a",
        key: "playback.subtitle_appearance",
        value: JSON.stringify({ fontSize: "medium" }),
      }),
    );

    await waitFor(() =>
      expect(result.current.stored.data).toMatchObject([
        {
          key: "playback.subtitle_appearance",
          scope: "profile_device",
          profile_id: "profile-a",
          device_id: "device-a",
          value: JSON.stringify({ fontSize: "medium" }),
          updated_at: "2026-11-23T00:00:00Z",
        },
      ]),
    );
  },
);

// A reopened editor reads its row and override flag from the same list, so a
// reset must take the row out as soon as the delete settles; otherwise the
// editor offers the deleted values again until a refetch answers.
it.each([
  [
    "one override",
    () => {
      const reset = useDeleteAdminUserDeviceSetting();
      return () =>
        reset.mutateAsync({
          userId: 1,
          profileId: "profile-a",
          deviceId: "device-a",
          key: "playback.subtitle_appearance",
        });
    },
  ],
  [
    "every override for the profile",
    () => {
      const reset = useDeleteAllAdminUserDeviceSettingsForDevice();
      return () =>
        reset.mutateAsync({
          userId: 1,
          profileId: "profile-a",
          deviceId: "device-a",
          keys: ["playback.subtitle_appearance"],
        });
    },
  ],
])("drops a reset device override from the user's stored values (%s)", async (_case, useReset) => {
  const otherDevice = { ...row("large", 1, "2026-11-21T00:00:00Z"), device_id: "device-b" };
  let gets = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input instanceof Request ? input.url : input);
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      if (method === "DELETE") return new Response(null, { status: 204 });
      if (url.includes("/settings/values")) {
        gets += 1;
        // The refetch after the reset never answers.
        return gets === 1
          ? valuesPage([row("medium", 2, "2026-11-23T00:00:00Z"), otherDevice])
          : new Promise<Response>(() => undefined);
      }
      return json({ items: [], page: { has_more: false } });
    }),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const { result } = renderHook(() => ({ stored: useAdminUserSettings(1), reset: useReset() }), {
    wrapper,
  });
  await waitFor(() => expect(result.current.stored.data).toHaveLength(2));

  await act(async () => {
    await result.current.reset();
  });

  await waitFor(() =>
    expect(result.current.stored.data).toMatchObject([
      { device_id: "device-b", value: JSON.stringify({ fontSize: "large" }) },
    ]),
  );
});

it("drops the overrides a reset-all removed before a later delete failed", async () => {
  const subtitles = row("medium", 2, "2026-11-23T00:00:00Z");
  const speed = { ...row("medium", 1, "2026-11-23T00:00:00Z"), key: "player.playback_speed" };
  let gets = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input instanceof Request ? input.url : input);
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      if (method === "DELETE") {
        return url.includes("player.playback_speed")
          ? new Response(JSON.stringify({ status: 500, title: "Internal" }), {
              status: 500,
              headers: { "Content-Type": "application/problem+json" },
            })
          : new Response(null, { status: 204 });
      }
      if (url.includes("/settings/values")) {
        gets += 1;
        return gets === 1 ? valuesPage([subtitles, speed]) : new Promise<Response>(() => undefined);
      }
      return json({ items: [], page: { has_more: false } });
    }),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const { result } = renderHook(
    () => ({
      stored: useAdminUserSettings(1),
      reset: useDeleteAllAdminUserDeviceSettingsForDevice(),
    }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.stored.data).toHaveLength(2));

  await act(async () => {
    await result.current.reset
      .mutateAsync({
        userId: 1,
        profileId: "profile-a",
        deviceId: "device-a",
        keys: ["playback.subtitle_appearance", "player.playback_speed"],
      })
      .catch(() => undefined);
  });

  await waitFor(() =>
    expect(result.current.stored.data).toMatchObject([{ key: "player.playback_speed" }]),
  );
  expect(result.current.stored.data).toHaveLength(1);
});

// The server clears both halves of a mirrored pair in one delete, so the
// cached list must lose both, or the device still counts and offers a reset
// for an override that is gone.
it("drops the mirrored intro skip row a reset clears with it", async () => {
  const introRow = (key: string, value: unknown, device_id = "device-a") => ({
    ...row("medium", 1, "2026-11-23T00:00:00Z"),
    key,
    value,
    device_id,
  });
  let gets = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input instanceof Request ? input.url : input);
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      if (method === "DELETE") return new Response(null, { status: 204 });
      if (url.includes("/settings/values")) {
        gets += 1;
        return gets === 1
          ? valuesPage([
              introRow("playback.intro_skip_mode", "always"),
              introRow("playback.auto_skip_intro", true),
              introRow("playback.auto_skip_intro", true, "device-b"),
            ])
          : new Promise<Response>(() => undefined);
      }
      return json({ items: [], page: { has_more: false } });
    }),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const { result } = renderHook(
    () => ({ stored: useAdminUserSettings(1), reset: useDeleteAdminUserDeviceSetting() }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.stored.data).toHaveLength(3));

  await act(async () => {
    await result.current.reset.mutateAsync({
      userId: 1,
      profileId: "profile-a",
      deviceId: "device-a",
      key: "playback.intro_skip_mode",
    });
  });

  await waitFor(() =>
    expect(result.current.stored.data).toMatchObject([
      { key: "playback.auto_skip_intro", device_id: "device-b" },
    ]),
  );
});
