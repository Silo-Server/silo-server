import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  effective: undefined as Record<string, { value: unknown }> | undefined,
  failed: false,
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: () => ({ data: mocks.effective, isError: mocks.failed }),
}));

import { SETTING_KEYS } from "@/lib/settingsContract";

import { useQualityPreference, useQualityPreferenceState } from "./qualityPreference";

function setResolved(value: unknown) {
  mocks.effective =
    value === undefined ? undefined : { [SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY]: { value } };
}

describe("useQualityPreference", () => {
  it("reads the deprecated 'original' and the older source spellings as auto", () => {
    for (const value of ["original", "Original", "source", "max"]) {
      setResolved(value);
      const { result } = renderHook(() => useQualityPreference("1080p"));
      expect(result.current).toBe("auto");
    }
  });

  it("reads an 'original' profile column fallback as auto", () => {
    setResolved(undefined);
    const { result } = renderHook(() => useQualityPreference("original"));
    expect(result.current).toBe("auto");
  });

  it("keeps fallback initialization provisional until a settings read succeeds", () => {
    setResolved(undefined);
    mocks.failed = true;
    const { result, rerender } = renderHook(() => useQualityPreferenceState("720p"));
    expect(result.current).toEqual({ preference: "720p", settled: false });
    mocks.failed = false;
    setResolved("1080p");
    rerender();
    expect(result.current).toEqual({ preference: "1080p", settled: true });
  });
});
