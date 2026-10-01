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
  it("passes 'original' through unchanged — it is a distinct wire value from 'auto' (#849)", () => {
    setResolved("original");
    const { result } = renderHook(() => useQualityPreference("1080p"));
    expect(result.current).toBe("original");
  });

  it("passes resolution rungs and auto through unchanged", () => {
    for (const value of ["auto", "480p", "720p", "1080p", "2160p"]) {
      setResolved(value);
      const { result } = renderHook(() => useQualityPreference(null));
      expect(result.current).toBe(value);
    }
  });

  it("falls back to the profile column until the settings read resolves", () => {
    setResolved(undefined);
    const { result } = renderHook(() => useQualityPreference("720p"));
    expect(result.current).toBe("720p");
  });

  it("returns null with no resolved value and no fallback", () => {
    setResolved(undefined);
    const { result } = renderHook(() => useQualityPreference());
    expect(result.current).toBeNull();
  });
});
