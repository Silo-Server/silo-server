import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { DEFAULT_SUBTITLE_APPEARANCE } from "@/lib/subtitleAppearance";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { type StoredSettingRow } from "@/lib/settingsResolve";
import { useSubtitleAppearanceSetting } from "./subtitleAppearance";
import { createHarness, fakeSettingsServer as fakeServer } from "./settingsServer.test-support";

const v2Mock = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: v2Mock };
});

const KEY = SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE;

const fakeSettingsServer = (initial?: StoredSettingRow[]) => fakeServer(v2Mock, KEY, initial);

const XLARGE = { ...DEFAULT_SUBTITLE_APPEARANCE, fontSize: "xlarge" as const };

describe("useSubtitleAppearanceSetting", () => {
  beforeEach(() => {
    v2Mock.mockReset();
  });

  it("clears this browser's row so a save from an older per-device build stops winning", async () => {
    const rows = fakeSettingsServer([
      {
        key: KEY,
        scope: "profile_device",
        profileId: "profile-1",
        deviceId: "device-1",
        value: { ...DEFAULT_SUBTITLE_APPEARANCE, fontSize: "small" },
      },
    ]);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.appearance.fontSize).toBe("small"));

    await result.current.save(XLARGE);

    expect(rows).toEqual([expect.objectContaining({ scope: "profile", value: XLARGE })]);
    await waitFor(() => expect(result.current.appearance.fontSize).toBe("xlarge"));
  });

  it("does not attempt a delete when nothing is shadowing the profile row", async () => {
    fakeSettingsServer([{ key: KEY, scope: "profile", profileId: "profile-1", value: XLARGE }]);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.appearance.fontSize).toBe("xlarge"));

    await result.current.save(DEFAULT_SUBTITLE_APPEARANCE);

    const deletes = v2Mock.mock.calls.filter(([operation]) =>
      String(operation).startsWith("DELETE "),
    );
    expect(deletes).toHaveLength(0);
  });

  it("resets to the default by clearing both the profile and this browser's rows", async () => {
    const rows = fakeSettingsServer([
      { key: KEY, scope: "profile", profileId: "profile-1", value: XLARGE },
      {
        key: KEY,
        scope: "profile_device",
        profileId: "profile-1",
        deviceId: "device-1",
        value: XLARGE,
      },
    ]);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.isCustomized).toBe(true));

    await result.current.reset();

    expect(rows).toEqual([]);
    await waitFor(() => expect(result.current.isCustomized).toBe(false));
    expect(result.current.appearance).toEqual(DEFAULT_SUBTITLE_APPEARANCE);
  });

  it("treats a reset with nothing stored as already done", async () => {
    fakeSettingsServer();
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    // The canonical DELETE answers 404 for "nothing stored here", which for a
    // reset is the requested state rather than a failure.
    await expect(result.current.reset()).resolves.toBeUndefined();
  });
});
