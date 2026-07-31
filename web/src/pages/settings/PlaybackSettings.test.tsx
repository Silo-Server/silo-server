// @vitest-environment jsdom

import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { EffectiveSetting, EffectiveSettingsMap } from "@/hooks/queries/settingValues";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";

const mocks = vi.hoisted(() => ({
  useEffectiveSettings: vi.fn(),
  useSetSettingValue: vi.fn(),
  useClearSettingValue: vi.fn(),
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

import PlaybackSettings from "./PlaybackSettings";

function resolved(
  key: SettingKey,
  value: unknown,
  source: EffectiveSetting["source"],
): EffectiveSettingsMap {
  return { [key]: { key, value, source } };
}

describe("PlaybackSettings", () => {
  let mutate: ReturnType<typeof vi.fn>;
  let mutateAsync: ReturnType<typeof vi.fn>;
  let clearMutateAsync: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mocks.useEffectiveSettings.mockReset();
    mocks.useSetSettingValue.mockReset();
    mocks.useClearSettingValue.mockReset();
    mutate = vi.fn();
    mutateAsync = vi.fn().mockResolvedValue(undefined);
    clearMutateAsync = vi.fn().mockResolvedValue(undefined);

    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.useSetSettingValue.mockReturnValue({ isPending: false, mutate, mutateAsync });
    mocks.useClearSettingValue.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: clearMutateAsync,
    });
  });

  afterEach(cleanup);

  it("renders without a profile record, reading every value from the contract", () => {
    // The screen used to require the cached profile object and read its
    // preference columns; it now resolves them, so it renders from the
    // settings API alone.
    render(<PlaybackSettings />);

    expect(screen.getByText("Spoken language")).toBeTruthy();
    expect(screen.getByText("Auto-play next episode")).toBeTruthy();
    expect(screen.getByText("Next up episodes")).toBeTruthy();
  });

  it("reads its values in one batch rather than one request per control", () => {
    render(<PlaybackSettings />);

    const batched = mocks.useEffectiveSettings.mock.calls.find(
      ([options]) => (options?.keys?.length ?? 0) > 2,
    );
    expect(batched?.[0].keys).toContain(SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO);
    expect(batched?.[0].keys).toContain(SETTING_KEYS.UI_NEXT_UP_MODE);
    expect(batched?.[0].keys).toContain(SETTING_KEYS.CATALOG_METADATA_LANGUAGE);
    expect(batched?.[0].keys).toContain(SETTING_KEYS.CATALOG_METADATA_LANGUAGE_OVERRIDES);
  });

  it("saves a toggle as typed JSON at profile scope", () => {
    render(<PlaybackSettings />);

    fireEvent.click(screen.getByLabelText("Auto-skip intros"));

    // Awaited rather than fire-and-forget: the write is followed by a clear of
    // any device override, which has to see whether the write landed.
    expect(mutateAsync).toHaveBeenCalledWith({
      key: SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO,
      // Typed JSON, not the "true"/"false" strings the legacy endpoint took.
      value: true,
      identity: { scope: "profile" },
    });
  });

  it("reads a stored value in preference to the contract default", () => {
    // playback.auto_play_next defaults to true, so a stored false is the case
    // that proves the screen trusts the resolved answer: the bug this guards is
    // a default-on toggle that a client's own idea of the default flips back.
    render(<PlaybackSettings />);
    expect(screen.getByLabelText("Auto-play next episode").getAttribute("aria-checked")).toBe(
      "true",
    );

    cleanup();
    mocks.useEffectiveSettings.mockReturnValue({
      data: resolved(SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT, false, "profile"),
      isLoading: false,
    });

    render(<PlaybackSettings />);
    expect(screen.getByLabelText("Auto-play next episode").getAttribute("aria-checked")).toBe(
      "false",
    );
  });

  it("turning a default-on toggle off stores an explicit false", async () => {
    render(<PlaybackSettings />);

    fireEvent.click(screen.getByLabelText("Auto-play next episode"));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT,
        value: false,
        identity: { scope: "profile" },
      }),
    );
  });

  it("clears a shadowing device row when auto-play is saved", async () => {
    // The player's post-roll toggle and this switch edit the same setting, and
    // the contract resolves profile_device above profile. A device row left in
    // place would keep shadowing this save and snap the switch back, with no
    // other web affordance able to remove it — so the save clears it.
    mocks.useEffectiveSettings.mockReturnValue({
      data: resolved(SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT, false, "profile_device"),
      isLoading: false,
    });

    render(<PlaybackSettings />);
    expect(screen.getByLabelText("Auto-play next episode").getAttribute("aria-checked")).toBe(
      "false",
    );

    fireEvent.click(screen.getByLabelText("Auto-play next episode"));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT,
        value: true,
        identity: { scope: "profile" },
      }),
    );
    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT,
        identity: { scope: "profile_device" },
      }),
    );
  });

  it("leaves other scopes alone when no device row is shadowing auto-play", async () => {
    mocks.useEffectiveSettings.mockReturnValue({
      data: resolved(SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT, false, "profile"),
      isLoading: false,
    });

    render(<PlaybackSettings />);
    fireEvent.click(screen.getByLabelText("Auto-play next episode"));

    await waitFor(() => expect(mutateAsync).toHaveBeenCalled());
    expect(clearMutateAsync).not.toHaveBeenCalled();
  });

  it("offers a reset only once the profile has stored a next-up choice", () => {
    // The resolved value is the contract default until a row exists, so the
    // affordance keys off the source rather than off the value.
    render(<PlaybackSettings />);
    expect(screen.queryByRole("button", { name: "Reset" })).toBeNull();

    cleanup();
    mocks.useEffectiveSettings.mockReturnValue({
      data: resolved(SETTING_KEYS.UI_NEXT_UP_MODE, "separate", "profile"),
      isLoading: false,
    });

    render(<PlaybackSettings />);
    fireEvent.click(screen.getByRole("button", { name: "Reset" }));

    // Reset is a delete of the profile row, so the setting inherits the
    // contract default again rather than storing the default as a value.
    expect(clearMutateAsync).toHaveBeenCalledWith({
      key: SETTING_KEYS.UI_NEXT_UP_MODE,
      identity: { scope: "profile" },
    });
  });
});
