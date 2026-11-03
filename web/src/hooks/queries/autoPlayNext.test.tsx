import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { type StoredSettingRow } from "@/lib/settingsResolve";
import { useAutoPlayNextSetting } from "./autoPlayNext";
import { createHarness, fakeSettingsServer as fakeServer } from "./settingsServer.test-support";

const v2Mock = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: v2Mock };
});

const KEY = SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT;

const fakeSettingsServer = (initial?: StoredSettingRow[]) => fakeServer(v2Mock, KEY, initial);

describe("useAutoPlayNextSetting", () => {
  beforeEach(() => {
    v2Mock.mockReset();
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

    expect(result.current.enabled).toBe(true);
    expect(result.current.hasDeviceOverride).toBe(false);
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

    const deletes = v2Mock.mock.calls.filter(([operation]) =>
      String(operation).startsWith("DELETE "),
    );
    expect(deletes).toHaveLength(0);
  });
});
