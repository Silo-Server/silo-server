import { describe, expect, it, vi, beforeEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { SETTING_KEYS, SETTINGS_REVISION } from "@/lib/settingsContract";
import { type StoredSettingRow } from "@/lib/settingsResolve";
import { useSubtitleSyncSetting } from "./subtitleSync";
import { createHarness, fakeSettingsServer as fakeServer } from "./settingsServer.test-support";

const v2Mock = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: v2Mock };
});

const KEY = SETTING_KEYS.PLAYER_SUBTITLE_SYNC_MS;

const fakeSettingsServer = (initial?: StoredSettingRow[], manifestRevision = SETTINGS_REVISION) =>
  fakeServer(v2Mock, KEY, initial, manifestRevision);

describe("useSubtitleSyncSetting", () => {
  beforeEach(() => {
    v2Mock.mockReset();
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
