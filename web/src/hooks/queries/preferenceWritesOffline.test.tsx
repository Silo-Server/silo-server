// A preference write held until reconnect replaces whatever another browser
// saved for the same setting or title in the meantime, so these writes go out
// at once and fail while offline.
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import {
  useDeleteAdminUserDeviceSetting,
  useDeleteAdminUserSetting,
  useDeleteAllAdminUserDeviceSettingsForDevice,
  useUpdateAdminUserDeviceSetting,
  useUpdateAdminUserSetting,
} from "./admin/users";
import { useDeleteSubtitlePreference, useSetSubtitlePreference } from "./subtitles";
import { useDeleteAudioPreference, useSetAudioPreference } from "./trackPreferences";
import type { VersionAudioTrack } from "@/api/types";

type Mutation = { isError: boolean };
type Row = { name: string; useHook: () => Mutation; send: (hook: Mutation) => void };
function row<T extends Mutation>(useHook: () => T, send: (hook: T) => void): Row {
  return { name: useHook.name, useHook, send: send as (hook: Mutation) => void };
}

const web = { scope: "profile_client", profileId: "profile-a", clientFamily: "web" } as const;
const device = { userId: 1, profileId: "profile-a", deviceId: "device-a" };
const track: VersionAudioTrack = { language: "en", codec: "aac", channels: 2 };

beforeEach(() => {
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
  setProfileId("profile-a");
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
});

it.each([
  row(useUpdateAdminUserSetting, (h) =>
    h.mutate({ userId: 1, key: "nav.primary_menu", identity: web, value: '{"items":[]}' }),
  ),
  row(useDeleteAdminUserSetting, (h) =>
    h.mutate({ userId: 1, key: "nav.primary_menu", identity: web }),
  ),
  row(useUpdateAdminUserDeviceSetting, (h) =>
    h.mutate({ ...device, key: "playback.auto_play_next", value: "true" }),
  ),
  row(useDeleteAdminUserDeviceSetting, (h) =>
    h.mutate({ ...device, key: "playback.auto_play_next" }),
  ),
  row(useDeleteAllAdminUserDeviceSettingsForDevice, (h) =>
    h.mutate({ ...device, keys: ["playback.auto_play_next"] }),
  ),
  row(useSetSubtitlePreference, (h) => h.mutate({ prefId: "series-a", selection: null })),
  row(useDeleteSubtitlePreference, (h) => h.mutate("series-a")),
  row(useSetAudioPreference, (h) => h.mutate({ prefId: "series-a", trackIndex: 1, track })),
  row(useDeleteAudioPreference, (h) => h.mutate("series-a")),
])("$name sends an offline write at once instead of holding it", async ({ useHook, send }) => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const { result } = renderHook(useHook, { wrapper });
  act(() => send(result.current));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalled();
});
