import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";

import { ApiClientError } from "@/api/client";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { resolveSettingValues, type StoredSettingRow } from "@/lib/settingsResolve";
import { useAutoPlayNextSetting } from "./autoPlayNext";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return { ...actual, api: apiMock };
});

const KEY = SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT;

function createHarness() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { queryClient, wrapper };
}

/**
 * A server stand-in that resolves through the shared client resolver, which
 * mirrors internal/settingsresolve. Using it rather than a canned answer is
 * what makes the scope-precedence assertions below mean something: a
 * profile_device row really does outrank a profile row here.
 */
function fakeSettingsServer(initial: StoredSettingRow[] = []) {
  const rows = [...initial];
  apiMock.mockImplementation((path: string, options?: RequestInit) => {
    if (path.startsWith("/settings/values/effective")) {
      const resolved = resolveSettingValues([KEY], rows, {
        profileId: "profile-1",
        deviceId: "device-1",
      })[0]!;
      return Promise.resolve({
        revision: 1,
        settings: [{ key: KEY, value: resolved.value, source: resolved.source }],
      });
    }
    const match = /^\/settings\/values\/([^?]+)\?scope=([a-z_]+)/.exec(path);
    if (!match) return Promise.resolve(undefined);
    const key = match[1]!;
    const scope = match[2]!;
    const index = rows.findIndex((row) => row.key === key && row.scope === scope);
    if (options?.method === "DELETE") {
      if (index < 0) {
        return Promise.reject(
          new ApiClientError(404, "not_found", "No value is set at this scope"),
        );
      }
      rows.splice(index, 1);
      return Promise.resolve(undefined);
    }
    const value = (JSON.parse(options?.body as string) as { value: unknown }).value;
    const row: StoredSettingRow = {
      key,
      scope: scope as StoredSettingRow["scope"],
      profileId: "profile-1",
      deviceId: scope === "profile_device" ? "device-1" : undefined,
      value,
    };
    if (index < 0) rows.push(row);
    else rows[index] = row;
    return Promise.resolve(undefined);
  });
  return rows;
}

describe("useAutoPlayNextSetting", () => {
  beforeEach(() => {
    apiMock.mockReset();
  });

  it("resolves an unset key to the contract default", async () => {
    fakeSettingsServer();
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useAutoPlayNextSetting(), { wrapper });

    await waitFor(() => expect(result.current.enabled).toBe(true));
    expect(result.current.hasDeviceOverride).toBe(false);
  });

  it("saves at profile scope so both surfaces read the same row", async () => {
    const rows = fakeSettingsServer();
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useAutoPlayNextSetting(), { wrapper });
    await waitFor(() => expect(result.current.enabled).toBe(true));

    await result.current.setEnabled(false);

    expect(rows).toEqual([expect.objectContaining({ key: KEY, scope: "profile", value: false })]);
  });

  it("clears a shadowing device row so the saved value actually takes effect", async () => {
    // The regression: the post-roll toggle used to write profile_device while
    // the settings screen wrote profile. profile_device wins resolution, so
    // the settings switch saved a value that never applied and snapped back.
    const rows = fakeSettingsServer([
      {
        key: KEY,
        scope: "profile_device",
        profileId: "profile-1",
        deviceId: "device-1",
        value: false,
      },
    ]);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useAutoPlayNextSetting(), { wrapper });

    await waitFor(() => expect(result.current.enabled).toBe(false));
    expect(result.current.hasDeviceOverride).toBe(true);

    await result.current.setEnabled(true);

    expect(rows.some((row) => row.scope === "profile_device")).toBe(false);
    expect(rows).toEqual([expect.objectContaining({ scope: "profile", value: true })]);
    // The resolved answer now matches what was asked for, rather than the
    // device row's stale false.
    await waitFor(() => expect(result.current.enabled).toBe(true));
  });

  it("does not attempt a delete when nothing is shadowing the profile row", async () => {
    fakeSettingsServer([{ key: KEY, scope: "profile", profileId: "profile-1", value: false }]);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useAutoPlayNextSetting(), { wrapper });
    await waitFor(() => expect(result.current.enabled).toBe(false));

    await result.current.setEnabled(true);

    const deletes = apiMock.mock.calls.filter(
      ([, options]) => (options as RequestInit | undefined)?.method === "DELETE",
    );
    expect(deletes).toHaveLength(0);
  });
});
