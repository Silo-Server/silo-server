import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";

import { v2Problem } from "@/api/v2/problems.test-support";
import { SETTING_KEYS, SETTINGS_API_VERSION, SETTINGS_REVISION } from "@/lib/settingsContract";
import { resolveSettingValues, type StoredSettingRow } from "@/lib/settingsResolve";
import { useSubtitleSyncSetting } from "./subtitleSync";

const v2Mock = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: v2Mock };
});

const KEY = SETTING_KEYS.PLAYER_SUBTITLE_SYNC_MS;

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
 * mirrors internal/settingsresolve, so a profile_device row really does
 * outrank a profile row here.
 */
function fakeSettingsServer(
  initial: StoredSettingRow[] = [],
  manifestRevision = SETTINGS_REVISION,
) {
  const rows = [...initial];
  v2Mock.mockImplementation(
    (
      operation: string,
      options?: { path?: { key: string }; query?: { scope: string }; body?: { value: unknown } },
    ) => {
      if (operation === "GET /api/v2/settings/contract/capabilities") {
        return Promise.resolve({
          api_version: SETTINGS_API_VERSION,
          manifest_revision: manifestRevision,
          contract_etag: "etag",
        });
      }
      if (operation === "GET /api/v2/settings/values/effective") {
        const resolved = resolveSettingValues([KEY], rows, {
          profileId: "profile-1",
          deviceId: "device-1",
        })[0]!;
        return Promise.resolve({
          revision: 1,
          items: [
            { key: KEY, value: resolved.value, source: resolved.source, definition_revision: 1 },
          ],
        });
      }
      const key = options?.path?.key;
      const scope = options?.query?.scope;
      if (!key || !scope) return Promise.resolve(undefined);
      const index = rows.findIndex((row) => row.key === key && row.scope === scope);
      if (operation.startsWith("DELETE ")) {
        if (index < 0) {
          return Promise.reject(v2Problem(404, "not_found", "No value is set at this scope"));
        }
        rows.splice(index, 1);
        return Promise.resolve(undefined);
      }
      const row: StoredSettingRow = {
        key,
        scope: scope as StoredSettingRow["scope"],
        profileId: "profile-1",
        deviceId: scope === "profile_device" ? "device-1" : undefined,
        value: options?.body?.value,
      };
      if (index < 0) rows.push(row);
      else rows[index] = row;
      return Promise.resolve(undefined);
    },
  );
  return rows;
}

describe("useSubtitleSyncSetting", () => {
  beforeEach(() => {
    v2Mock.mockReset();
  });

  it("reads the profile's offset when this browser has none of its own", async () => {
    fakeSettingsServer([{ key: KEY, scope: "profile", profileId: "profile-1", value: -750 }]);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleSyncSetting(), { wrapper });

    expect(result.current.isLoaded).toBe(false);
    await waitFor(() => expect(result.current.offsetMs).toBe(-750));
    expect(result.current.isLoaded).toBe(true);
  });

  it("saves at profile scope and clears a shadowing browser row", async () => {
    const rows = fakeSettingsServer([
      {
        key: KEY,
        scope: "profile_device",
        profileId: "profile-1",
        deviceId: "device-1",
        value: 300,
      },
    ]);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleSyncSetting(), { wrapper });
    await waitFor(() => expect(result.current.offsetMs).toBe(300));

    await result.current.save(-200);

    expect(rows).toEqual([expect.objectContaining({ key: KEY, scope: "profile", value: -200 })]);
    await waitFor(() => expect(result.current.offsetMs).toBe(-200));
  });

  it("clamps an offset past the contract's bounds instead of sending one the server refuses", async () => {
    const rows = fakeSettingsServer();
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleSyncSetting(), { wrapper });
    await waitFor(() => expect(result.current.isLoaded).toBe(true));

    await result.current.save(12_345);

    expect(rows).toEqual([expect.objectContaining({ value: 10_000 })]);
  });

  it("saves and resets for this browser only on a server older than the profile scope", async () => {
    const rows = fakeSettingsServer(
      [
        {
          key: KEY,
          scope: "profile_device",
          profileId: "profile-1",
          deviceId: "device-1",
          value: 300,
        },
      ],
      17,
    );
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleSyncSetting(), { wrapper });
    await waitFor(() => expect(result.current.isLoaded).toBe(true));

    await result.current.save(-200);
    expect(rows).toEqual([
      expect.objectContaining({ key: KEY, scope: "profile_device", value: -200 }),
    ]);

    await result.current.reset();
    expect(rows).toEqual([]);
    await result.current.reset();
  });

  it("stays loaded when a background capabilities refetch fails", async () => {
    fakeSettingsServer([{ key: KEY, scope: "profile", profileId: "profile-1", value: -750 }]);
    const { queryClient, wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleSyncSetting(), { wrapper });
    await waitFor(() => expect(result.current.isLoaded).toBe(true));

    const answer = v2Mock.getMockImplementation()!;
    v2Mock.mockImplementation((operation: string, ...rest: unknown[]) =>
      operation === "GET /api/v2/settings/contract/capabilities"
        ? Promise.reject(new Error("offline"))
        : answer(operation, ...rest),
    );
    await act(() => queryClient.refetchQueries({ type: "active" }));
    expect(
      queryClient.getQueryCache().findAll({
        predicate: (query) => query.queryKey.includes("capabilities"),
      })[0]?.state.status,
    ).toBe("error");

    expect(result.current.isLoaded).toBe(true);
  });
});
