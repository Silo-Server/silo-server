import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  effective: undefined as Record<string, { value: unknown }> | undefined,
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: () => ({ data: mocks.effective, isLoading: false }),
}));

import { SETTING_KEYS } from "@/lib/settingsContract";

import { useQualityPreference } from "./qualityPreference";

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

  it("falls back to the profile column until the settings read resolves", () => {
    setResolved(undefined);
    const { result } = renderHook(() => useQualityPreference("720p"));
    expect(result.current).toBe("720p");
  });
});
