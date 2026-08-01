// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SETTING_KEYS } from "@/lib/settingsContract";
import type { EffectiveSetting, EffectiveSettingsMap } from "@/hooks/queries/settingValues";

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

vi.mock("@/hooks/useDateTimeFormat", () => ({ useDateTimeFormat: () => undefined }));
vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({
    emblaRef: () => {},
    canScrollPrev: false,
    canScrollNext: false,
    scrollPrev: () => {},
    scrollNext: () => {},
  }),
}));

import { PlayingNextScreen } from "./PlayingNextScreen";

const KEY = SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT;

function resolved(value: unknown, source: EffectiveSetting["source"]): EffectiveSettingsMap {
  return { [KEY]: { key: KEY, value, source } };
}

function renderScreen() {
  render(
    <PlayingNextScreen
      seriesId="series-1"
      seriesTitle="Test Show"
      nextEpisode={{
        contentId: "ep-2",
        title: "Episode Two",
        seasonNumber: 1,
        episodeNumber: 2,
        runtime: 1800,
      }}
      continueWatchingItems={[]}
      videoEnded={false}
      onPlayItem={() => {}}
      onClose={() => {}}
    />,
  );
}

describe("PlayingNextScreen auto-play toggle", () => {
  let mutateAsync: ReturnType<typeof vi.fn>;
  let clearMutateAsync: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mocks.useEffectiveSettings.mockReset();
    mocks.useSetSettingValue.mockReset();
    mocks.useClearSettingValue.mockReset();
    mutateAsync = vi.fn().mockResolvedValue(undefined);
    clearMutateAsync = vi.fn().mockResolvedValue(undefined);

    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.useSetSettingValue.mockReturnValue({ isPending: false, mutate: vi.fn(), mutateAsync });
    mocks.useClearSettingValue.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: clearMutateAsync,
    });
  });

  afterEach(cleanup);

  it("writes the profile row, the same scope the settings screen edits", async () => {
    // The two surfaces render the resolved value, and the contract resolves
    // profile_device above profile. Writing a device row here would leave the
    // Settings switch inert: it would save a profile value this row shadows.
    renderScreen();

    fireEvent.click(screen.getByRole("button", { name: "Auto-play is on" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        key: KEY,
        value: false,
        identity: { scope: "profile" },
      }),
    );
    expect(
      mutateAsync.mock.calls.some(
        ([args]) => (args as { identity: { scope: string } }).identity.scope === "profile_device",
      ),
    ).toBe(false);
  });

  it("clears a device override rather than writing another one", async () => {
    mocks.useEffectiveSettings.mockReturnValue({
      data: resolved(false, "profile_device"),
      isLoading: false,
    });

    renderScreen();
    expect(screen.getByRole("button", { name: "Auto-play is off" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Auto-play is off" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        key: KEY,
        value: true,
        identity: { scope: "profile" },
      }),
    );
    await waitFor(() =>
      expect(clearMutateAsync).toHaveBeenCalledWith({
        key: KEY,
        identity: { scope: "profile_device" },
      }),
    );
  });
});
