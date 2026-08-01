// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import {
  useAdminDeviceOverrides,
  useAdminUserDeviceSettings,
  useAdminUserSettings,
  useDeleteAdminUserSetting,
  useDeleteAllAdminUserDeviceSettingsForDevice,
  useUpdateAdminUserDeviceSetting,
  useUpdateAdminUserSetting,
} from "./users";

// These hooks moved off the removed string-registry routes
// (/admin/users/{id}/settings, /device-settings, /profiles/{pid}/device-settings/…)
// onto the canonical values API. Every assertion here pins the new URL shape
// and the typed JSON bodies — against the old hooks each of these requests
// would 404 in production, which is exactly the breakage this file guards.

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

const valuesResponse = {
  revision: 1,
  values: [
    {
      key: "playback.subtitle_mode",
      scope: "profile",
      profile_id: "p1",
      value: "always",
      revision: 1,
      updated_at: "2026-07-28T10:00:00Z",
    },
    {
      key: "playback.auto_skip_intro",
      scope: "profile",
      profile_id: "p1",
      value: true,
      revision: 1,
    },
    {
      key: "player.audio_sync_ms",
      scope: "profile_device",
      profile_id: "p1",
      device_id: "tv-1",
      value: 250,
      revision: 1,
      updated_at: "2026-07-28T11:00:00Z",
    },
  ],
};

describe("admin canonical settings hooks", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("lists non-device values from /settings/values with stringified values", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) => {
        expect(String(input)).toBe("/api/v1/admin/users/7/settings/values");
        return jsonResponse(valuesResponse);
      }),
    );

    const { result } = renderHook(() => useAdminUserSettings(7), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    expect(result.current.data).toEqual([
      {
        key: "playback.subtitle_mode",
        scope: "profile",
        profile_id: "p1",
        library_id: undefined,
        series_id: undefined,
        value: "always",
        updated_at: "2026-07-28T10:00:00Z",
      },
      {
        key: "playback.auto_skip_intro",
        scope: "profile",
        profile_id: "p1",
        library_id: undefined,
        series_id: undefined,
        value: "true",
        updated_at: undefined,
      },
    ]);
  });

  it("derives device overrides from the same list, enriched with names", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) => {
        const url = String(input);
        if (url === "/api/v1/admin/users/7/settings/values") {
          return jsonResponse(valuesResponse);
        }
        if (url === "/api/v1/admin/devices") {
          return jsonResponse({
            devices: [
              {
                user_id: 7,
                device_id: "tv-1",
                device_name: "Living Room TV",
                device_platform: "tvos",
              },
            ],
          });
        }
        if (url === "/api/v1/admin/users/7/profiles") {
          return jsonResponse([{ id: "p1", name: "Laura" }]);
        }
        throw new Error(`unexpected request: ${url}`);
      }),
    );

    const { result } = renderHook(() => useAdminUserDeviceSettings(7), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.data.length).toBe(1));

    expect(result.current.data).toEqual([
      {
        user_id: 7,
        profile_id: "p1",
        profile_name: "Laura",
        device_id: "tv-1",
        device_name: "Living Room TV",
        device_platform: "tvos",
        key: "player.audio_sync_ms",
        value: "250",
        updated_at: "2026-07-28T11:00:00Z",
      },
    ]);
  });

  it("scopes the device panel's overrides to one device, from the canonical list", async () => {
    // The device detail endpoint reports the legacy user_device_settings
    // table, which nothing canonical writes to; the panel has to read the
    // values API or an override written since the cutover never appears.
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) => {
        const url = String(input);
        if (url === "/api/v1/admin/users/7/settings/values") {
          return jsonResponse({
            revision: 1,
            values: [
              ...valuesResponse.values,
              {
                key: "player.hdr_enabled",
                scope: "profile_device",
                profile_id: "p1",
                device_id: "phone-9",
                value: false,
                revision: 1,
              },
            ],
          });
        }
        if (url === "/api/v1/admin/devices") {
          return jsonResponse({ devices: [] });
        }
        if (url === "/api/v1/admin/users/7/profiles") {
          return jsonResponse([{ id: "p1", name: "Laura" }]);
        }
        throw new Error(`unexpected request: ${url}`);
      }),
    );

    const { result } = renderHook(() => useAdminDeviceOverrides(7, "tv-1"), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.data.length).toBe(1));

    // Only tv-1's row, and never a non-device-scoped row from the same list.
    expect(result.current.data.map((setting) => setting.key)).toEqual(["player.audio_sync_ms"]);
    expect(result.current.data[0]?.device_id).toBe("tv-1");
  });

  it("writes a user setting to the canonical route with a typed value", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      if (init?.method === "PUT") {
        expect(url).toBe(
          "/api/v1/admin/users/7/settings/values/playback.auto_skip_intro?scope=profile&profile_id=p1",
        );
        // Booleans travel typed, not as the "true" string the old registry stored.
        expect(JSON.parse(String(init.body))).toEqual({ value: true });
        return jsonResponse({ key: "playback.auto_skip_intro", scope: "profile", value: true });
      }
      return jsonResponse(valuesResponse);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useUpdateAdminUserSetting(), { wrapper: createWrapper() });
    result.current.mutate({
      userId: 7,
      key: "playback.auto_skip_intro",
      identity: { scope: "profile", profileId: "p1" },
      value: "true",
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
  });

  it("deletes a user setting at its exact scope", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      expect(init?.method).toBe("DELETE");
      expect(String(input)).toBe(
        "/api/v1/admin/users/7/settings/values/playback.subtitle_mode?scope=profile&profile_id=p1",
      );
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useDeleteAdminUserSetting(), { wrapper: createWrapper() });
    result.current.mutate({
      userId: 7,
      key: "playback.subtitle_mode",
      identity: { scope: "profile", profileId: "p1" },
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
  });

  it("writes a device override at profile_device scope with a typed number", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      if (init?.method === "PUT") {
        expect(String(input)).toBe(
          "/api/v1/admin/users/7/settings/values/player.audio_sync_ms?scope=profile_device&profile_id=p1&device_id=tv-1",
        );
        expect(JSON.parse(String(init.body))).toEqual({ value: 250 });
        return jsonResponse({ key: "player.audio_sync_ms", scope: "profile_device", value: 250 });
      }
      return jsonResponse(valuesResponse);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useUpdateAdminUserDeviceSetting(), {
      wrapper: createWrapper(),
    });
    result.current.mutate({
      userId: 7,
      profileId: "p1",
      deviceId: "tv-1",
      key: "player.audio_sync_ms",
      value: "250",
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
  });

  it("resets a whole device profile via per-key deletes, tolerating 404s", async () => {
    const deleted: string[] = [];
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      if (init?.method === "DELETE") {
        const url = String(input);
        deleted.push(url);
        // The first key's row is already gone: that is the goal state, not a
        // failure, so the loop must carry on to the second key.
        if (url.includes("player.hdr_enabled")) {
          return jsonResponse(
            { error: "not_found", message: "No value is set at this scope" },
            404,
          );
        }
        return new Response(null, { status: 204 });
      }
      return jsonResponse(valuesResponse);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useDeleteAllAdminUserDeviceSettingsForDevice(), {
      wrapper: createWrapper(),
    });
    result.current.mutate({
      userId: 7,
      profileId: "p1",
      deviceId: "tv-1",
      keys: ["player.hdr_enabled", "player.audio_sync_ms"],
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(deleted).toEqual([
      "/api/v1/admin/users/7/settings/values/player.hdr_enabled?scope=profile_device&profile_id=p1&device_id=tv-1",
      "/api/v1/admin/users/7/settings/values/player.audio_sync_ms?scope=profile_device&profile_id=p1&device_id=tv-1",
    ]);
  });
});
