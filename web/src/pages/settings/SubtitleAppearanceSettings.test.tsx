// @vitest-environment jsdom

import { render, screen, cleanup, fireEvent } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { EffectiveSetting, EffectiveSettingsMap } from "@/hooks/queries/settingValues";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { DEFAULT_SUBTITLE_APPEARANCE } from "@/lib/subtitleAppearance";

const mocks = vi.hoisted(() => ({
  useEffectiveSettings: vi.fn(),
  useSetSettingValue: vi.fn(),
  useClearSettingValue: vi.fn(),
  useSubtitleAppearanceSetting: vi.fn(),
}));

vi.mock("@/hooks/queries/settingValues", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/settingValues")>(
    "@/hooks/queries/settingValues",
  );

  return {
    ...actual,
    useEffectiveSettings: (...args: unknown[]) => mocks.useEffectiveSettings(...args),
    useSetSettingValue: (...args: unknown[]) => mocks.useSetSettingValue(...args),
    useClearSettingValue: (...args: unknown[]) => mocks.useClearSettingValue(...args),
  };
});

vi.mock("@/hooks/queries/subtitleAppearance", () => ({
  useSubtitleAppearanceSetting: (...args: unknown[]) => mocks.useSubtitleAppearanceSetting(...args),
}));

import SubtitleAppearanceSettings from "./SubtitleAppearanceSettings";

// The appearance panel renders a Slider, whose Radix primitive measures itself.
// jsdom has no ResizeObserver, so stub one — the measurement is irrelevant to
// what these tests assert.
class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= NoopResizeObserver as unknown as typeof ResizeObserver;

function resolved(
  key: SettingKey,
  value: unknown,
  source: EffectiveSetting["source"],
): EffectiveSettingsMap {
  return { [key]: { key, value, source } };
}

describe("SubtitleAppearanceSettings", () => {
  let mutate: ReturnType<typeof vi.fn>;
  let mutateAsync: ReturnType<typeof vi.fn>;
  let clearMutateAsync: ReturnType<typeof vi.fn>;
  let save: ReturnType<typeof vi.fn>;
  let reset: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mocks.useEffectiveSettings.mockReset();
    mocks.useSetSettingValue.mockReset();
    mocks.useClearSettingValue.mockReset();
    mocks.useSubtitleAppearanceSetting.mockReset();
    mutate = vi.fn();
    mutateAsync = vi.fn().mockResolvedValue(undefined);
    clearMutateAsync = vi.fn().mockResolvedValue(undefined);
    save = vi.fn().mockResolvedValue(undefined);
    reset = vi.fn().mockResolvedValue(undefined);

    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.useSetSettingValue.mockReturnValue({ isPending: false, mutate, mutateAsync });
    mocks.useClearSettingValue.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: clearMutateAsync,
    });
    mocks.useSubtitleAppearanceSetting.mockReturnValue({
      appearance: DEFAULT_SUBTITLE_APPEARANCE,
      hasDeviceOverride: false,
      save,
      reset,
      isSaving: false,
      isResetting: false,
      isLoading: false,
    });
  });

  afterEach(cleanup);

  it("reads the behavior triple from the contract in one batch", () => {
    render(<SubtitleAppearanceSettings />);

    const [options] = mocks.useEffectiveSettings.mock.calls[0] ?? [];
    expect(options?.keys).toEqual([
      SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE,
      SETTING_KEYS.PLAYBACK_SUBTITLE_MODE,
      SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES,
    ]);
  });

  it("writes forced subtitles at profile scope as a typed boolean", () => {
    render(<SubtitleAppearanceSettings />);

    // Contract default is true, so the first click stores an explicit false —
    // the value the legacy profile column could not distinguish from unset.
    fireEvent.click(screen.getByLabelText("Show forced subtitles"));

    // Awaited rather than fire-and-forget: the profile write is followed by a
    // clear of any device override that would otherwise keep shadowing it.
    expect(mutateAsync).toHaveBeenCalledWith({
      key: SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES,
      value: false,
      identity: { scope: "profile" },
    });
  });

  it("reads a stored forced-subtitle choice rather than the default", () => {
    mocks.useEffectiveSettings.mockReturnValue({
      data: resolved(SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES, false, "profile"),
      isLoading: false,
    });

    render(<SubtitleAppearanceSettings />);

    expect(screen.getByLabelText("Show forced subtitles").getAttribute("aria-checked")).toBe(
      "false",
    );
  });

  it("offers the appearance reset only when this device holds an override", () => {
    render(<SubtitleAppearanceSettings />);
    expect(screen.queryByRole("button", { name: /Reset Appearance/ })).toBeNull();

    cleanup();
    mocks.useSubtitleAppearanceSetting.mockReturnValue({
      appearance: DEFAULT_SUBTITLE_APPEARANCE,
      hasDeviceOverride: true,
      save,
      reset,
      isSaving: false,
      isResetting: false,
      isLoading: false,
    });

    render(<SubtitleAppearanceSettings />);
    fireEvent.click(screen.getByRole("button", { name: /Reset Appearance/ }));
    expect(reset).toHaveBeenCalled();
  });
});
