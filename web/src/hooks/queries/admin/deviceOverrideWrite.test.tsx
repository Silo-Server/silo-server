import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import {
  useAdminUserSettings,
  useDeleteAdminUserDeviceSetting,
  useDeleteAdminUserSetting,
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
const deleted = () => new Response(null, { status: 204 });
// The server's answer to a delete with nothing stored at that scope.
const alreadyGone = () =>
  new Response(
    JSON.stringify({
      type: "https://siloserver.org/docs/api/v2/problems/not_found",
      title: "Not found",
      status: 404,
      detail: "No value is set at this scope",
    }),
    { status: 404, headers: { "Content-Type": "application/problem+json" } },
  );

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
// editor offers the deleted values again until a refetch answers. A reset the
// server answers with 404 (another admin or tab reset it first) ends the same
// way instead of failing on every retry.
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
    deleted,
  ],
  [
    "one override another tab already reset",
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
    alreadyGone,
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
    deleted,
  ],
  [
    "a removal on the user's Preferences tab",
    () => {
      const remove = useDeleteAdminUserSetting();
      return () =>
        remove.mutateAsync({
          userId: 1,
          key: "playback.subtitle_appearance",
          identity: { scope: "profile_device", profileId: "profile-a", deviceId: "device-a" },
        });
    },
    deleted,
  ],
])("drops a reset device override from the stored values (%s)", async (_case, useReset, reply) => {
  const otherDevice = { ...row("large", 1, "2026-11-21T00:00:00Z"), device_id: "device-b" };
  let gets = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input instanceof Request ? input.url : input);
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      if (method === "DELETE") return reply();
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

// Each delete of a mirrored key clears both halves, so a second delete for the
// pair would erase an intro skip save that landed after the first one.
it("sends one delete for the mirrored intro skip pair in a reset-all", async () => {
  const introRow = (key: string, value: unknown) => ({
    ...row("medium", 1, "2026-11-23T00:00:00Z"),
    key,
    value,
  });
  const deleted: string[] = [];
  let gets = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input instanceof Request ? input.url : input);
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      if (method === "DELETE") {
        deleted.push(
          decodeURIComponent(new URL(url, "http://silo.test").pathname.split("/").pop()!),
        );
        return new Response(null, { status: 204 });
      }
      if (url.includes("/settings/values")) {
        gets += 1;
        return gets === 1
          ? valuesPage([
              introRow("playback.intro_skip_mode", "always"),
              introRow("playback.auto_skip_intro", true),
              row("medium", 2, "2026-11-23T00:00:00Z"),
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
    () => ({
      stored: useAdminUserSettings(1),
      reset: useDeleteAllAdminUserDeviceSettingsForDevice(),
    }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.stored.data).toHaveLength(3));

  await act(async () => {
    await result.current.reset.mutateAsync({
      userId: 1,
      profileId: "profile-a",
      deviceId: "device-a",
      keys: [
        "playback.intro_skip_mode",
        "playback.auto_skip_intro",
        "playback.subtitle_appearance",
      ],
    });
  });

  expect(deleted).toEqual(["playback.intro_skip_mode", "playback.subtitle_appearance"]);
  await waitFor(() => expect(result.current.stored.data).toEqual([]));
});

// The server writes both halves of a mirrored pair in one save, but answers
// with the addressed row only, so the cached list must add the other half too.
it.each([
  ["Skip intros, from no override", "playback.intro_skip_mode", "always", [], "true"],
  ["Skip intros set to never", "playback.intro_skip_mode", "never", [], "false"],
  [
    "the old switch, over an existing pair",
    "playback.auto_skip_intro",
    "false",
    [
      ["playback.intro_skip_mode", "always"],
      ["playback.auto_skip_intro", true],
    ],
    "ask",
  ],
] as const)(
  "shows the mirrored intro skip row a save writes with it (%s)",
  async (_case, key, value, stored, mirroredValue) => {
    const introRow = (rowKey: string, rowValue: unknown, updated_at = "2026-11-22T00:00:00Z") => ({
      ...row("medium", 1, updated_at),
      key: rowKey,
      value: rowValue,
    });
    let gets = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        const method = init?.method ?? (input instanceof Request ? input.method : "GET");
        if (method === "PUT") {
          return json(
            introRow(
              key,
              // The boolean key stores a boolean, not the string the form sends.
              value === "false" ? false : value,
              "2026-11-23T00:00:00Z",
            ),
          );
        }
        if (url.includes("/settings/values")) {
          gets += 1;
          // The refetch after the save never answers.
          return gets === 1
            ? valuesPage(stored.map(([k, v]) => introRow(k, v)))
            : new Promise<Response>(() => undefined);
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
    await waitFor(() => expect(result.current.stored.data).toHaveLength(stored.length));

    await act(() =>
      result.current.save.mutateAsync({
        userId: 1,
        profileId: "profile-a",
        deviceId: "device-a",
        key,
        value,
      }),
    );

    const mirror =
      key === "playback.intro_skip_mode" ? "playback.auto_skip_intro" : "playback.intro_skip_mode";
    await waitFor(() => expect(result.current.stored.data).toHaveLength(2));
    expect(result.current.stored.data).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ key, value, updated_at: "2026-11-23T00:00:00Z" }),
        expect.objectContaining({
          key: mirror,
          scope: "profile_device",
          profile_id: "profile-a",
          device_id: "device-a",
          value: mirroredValue,
          updated_at: "2026-11-23T00:00:00Z",
        }),
      ]),
    );
  },
);
