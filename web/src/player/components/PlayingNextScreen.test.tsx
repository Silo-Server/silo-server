// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ComponentProps } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { EffectiveSetting, EffectiveSettingsMap } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";

const mocks = vi.hoisted(() => ({
  useEffectiveSettings: vi.fn(),
  useSetSettingValue: vi.fn(),
  useClearSettingValue: vi.fn(),
  spoilerPrefs: { hideImages: false, hideOverviews: false },
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

vi.mock("@/hooks/useEpisodeSpoilerPrefs", () => ({
  useEpisodeSpoilerPrefs: () => mocks.spoilerPrefs,
}));

vi.mock("@/lib/thumbhash", () => ({
  decodeThumbhash: (hash: string) => (hash ? `data:image/png;base64,${hash}` : ""),
}));

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

function renderScreen(props: Partial<ComponentProps<typeof PlayingNextScreen>> = {}) {
  render(
    <PlayingNextScreen
      seriesId="series-1"
      seriesTitle="Test Show"
      nextEpisode={{
        contentId: "ep-2",
        title: "Episode Two",
        seasonNumber: 1,
        episodeNumber: 2,
        runtime: 48,
      }}
      continueWatchingItems={[]}
      videoEnded={false}
      onPlayItem={() => {}}
      onClose={() => {}}
      {...props}
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

describe("PlayingNextScreen next-episode start", () => {
  beforeEach(() => {
    mocks.useEffectiveSettings.mockReset().mockReturnValue({ data: {}, isLoading: false });
    mocks.useSetSettingValue
      .mockReset()
      .mockReturnValue({ isPending: false, mutateAsync: vi.fn() });
    mocks.useClearSettingValue
      .mockReset()
      .mockReturnValue({ isPending: false, mutateAsync: vi.fn() });
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("starts the next episode as an automatic start when the countdown runs out", () => {
    vi.useFakeTimers();
    const onPlayNow = vi.fn();
    renderScreen({ videoEnded: true, onPlayNow });

    act(() => vi.advanceTimersByTime(10_000));

    expect(onPlayNow).toHaveBeenCalledOnce();
    expect(onPlayNow).toHaveBeenCalledWith("automatic");
  });

  it("keeps counting down while the parent re-renders the same next episode", () => {
    vi.useFakeTimers();
    const onPlayNow = vi.fn();
    const screenFor = () => (
      <PlayingNextScreen
        seriesId="series-1"
        seriesTitle="Test Show"
        // A fresh object every render, as a parent rebuilding its list makes.
        nextEpisode={{
          contentId: "ep-2",
          title: "Two",
          seasonNumber: 1,
          episodeNumber: 2,
          runtime: 48,
        }}
        continueWatchingItems={[]}
        videoEnded
        onPlayNow={onPlayNow}
        onPlayItem={() => {}}
        onClose={() => {}}
      />
    );
    const { rerender } = render(screenFor());

    for (let elapsed = 0; elapsed < 12_000; elapsed += 900) {
      act(() => vi.advanceTimersByTime(900));
      rerender(screenFor());
    }

    expect(onPlayNow).toHaveBeenCalledWith("automatic");
  });

  it("starts the next episode as the viewer's start from Play Now or Enter", () => {
    const onPlayNow = vi.fn();
    renderScreen({ onPlayNow });
    expect(screen.getByText("48m")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Play Now" }));
    fireEvent.keyDown(document, { key: "Enter" });

    expect(onPlayNow.mock.calls).toEqual([["viewer"], ["viewer"]]);
  });
});

describe("PlayingNextScreen shuffle", () => {
  beforeEach(() => {
    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.useSetSettingValue.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: vi.fn(),
    });
    mocks.useClearSettingValue.mockReturnValue({ isPending: false, mutateAsync: vi.fn() });
  });

  afterEach(cleanup);

  it("names the shuffle and offers another pick or stopping", () => {
    const onReshuffle = vi.fn();
    const onStop = vi.fn();
    renderScreen({ shuffle: { scopeLabel: "Test Show · Season 1", onReshuffle, onStop } });

    expect(screen.getByText("Shuffling Test Show · Season 1")).toBeTruthy();
    expect(screen.getByText("Up Next at Random")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Pick Another" }));
    fireEvent.click(screen.getByRole("button", { name: "Stop shuffling" }));

    expect(onReshuffle).toHaveBeenCalledOnce();
    expect(onStop).toHaveBeenCalledOnce();
  });

  it("shows a shuffled movie without a season and episode line", () => {
    renderScreen({
      seriesTitle: "Heat",
      nextEpisode: {
        contentId: "movie-1",
        title: "Heat",
        seasonNumber: 0,
        episodeNumber: 0,
        runtime: 170,
      },
      shuffle: { scopeLabel: "Movies", onReshuffle: () => {}, onStop: () => {} },
    });

    expect(screen.getByText("Heat")).toBeTruthy();
    expect(screen.queryByText(/S0:E0/)).toBeNull();
  });

  it("gives a newly picked item the full countdown", () => {
    vi.useFakeTimers();
    const onPlayNow = vi.fn();
    const screenFor = (contentId: string) => (
      <PlayingNextScreen
        seriesTitle="Movies"
        nextEpisode={{
          contentId,
          title: contentId,
          seasonNumber: 0,
          episodeNumber: 0,
          runtime: 90,
        }}
        continueWatchingItems={[]}
        videoEnded
        onPlayNow={onPlayNow}
        onPlayItem={() => {}}
        onClose={() => {}}
        shuffle={{ scopeLabel: "Movies", onReshuffle: () => {}, onStop: () => {} }}
      />
    );
    const { rerender } = render(screenFor("movie-1"));

    act(() => vi.advanceTimersByTime(8_000));
    // Pick Another replaced the announced movie two seconds before it played.
    rerender(screenFor("movie-2"));
    act(() => vi.advanceTimersByTime(8_000));
    expect(onPlayNow).not.toHaveBeenCalled();

    act(() => vi.advanceTimersByTime(2_000));
    expect(onPlayNow).toHaveBeenCalledWith("automatic");
    vi.useRealTimers();
  });

  it("leaves Enter on a shuffle control to that control", () => {
    const onPlayNow = vi.fn();
    renderScreen({
      onPlayNow,
      shuffle: { scopeLabel: "Movies", onReshuffle: () => {}, onStop: () => {} },
    });

    fireEvent.keyDown(screen.getByRole("button", { name: "Pick Another" }), { key: "Enter" });
    fireEvent.keyDown(screen.getByRole("button", { name: "Stop shuffling" }), { key: "Enter" });
    expect(onPlayNow).not.toHaveBeenCalled();

    fireEvent.keyDown(document, { key: "Enter" });
    expect(onPlayNow).toHaveBeenCalledWith("viewer");
  });

  it("titles a movie by its own name when there is no series line", () => {
    renderScreen({
      seriesTitle: undefined,
      nextEpisode: {
        contentId: "movie-1",
        title: "Heat",
        seasonNumber: 0,
        episodeNumber: 0,
        runtime: 170,
      },
      shuffle: { scopeLabel: "Movies", onReshuffle: () => {}, onStop: () => {} },
    });

    expect(screen.getByText("Heat")).toBeTruthy();
  });

  it("offers no shuffle controls outside a shuffle", () => {
    renderScreen();

    expect(screen.getByText("Playing Next")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Pick Another" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Stop shuffling" })).toBeNull();
  });
});

describe("PlayingNextScreen spoiler protection", () => {
  beforeEach(() => {
    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.useSetSettingValue.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: vi.fn(),
    });
    mocks.useClearSettingValue.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: vi.fn(),
    });
    mocks.spoilerPrefs = { hideImages: true, hideOverviews: false };
  });

  afterEach(() => {
    mocks.spoilerPrefs = { hideImages: false, hideOverviews: false };
    cleanup();
  });

  const nextWithoutUrl = {
    contentId: "ep-2",
    title: "Episode Two",
    seasonNumber: 1,
    episodeNumber: 2,
    runtime: 48,
    stillThumbhash: "still-hash",
    stillIsEpisodeStill: true,
    watchState: { played: false },
  };

  function previewPlaceholder(): HTMLImageElement {
    const images = Array.from(document.querySelectorAll<HTMLImageElement>("img")).filter(
      (img) =>
        img.getAttribute("src") === "data:image/png;base64,still-hash" &&
        !img.className.includes("blur-3xl"),
    );
    const [image] = images;
    if (images.length !== 1 || !image)
      throw new Error(`expected one preview placeholder, found ${images.length}`);
    return image;
  }

  it("blurs the still's placeholder when the still URL is missing", () => {
    renderScreen({ nextEpisode: nextWithoutUrl });
    expect(previewPlaceholder().className).toContain("blur-xl");
  });

  it("leaves a shuffled movie's backdrop and overview visible", () => {
    mocks.spoilerPrefs = { hideImages: true, hideOverviews: true };
    renderScreen({
      nextEpisode: {
        contentId: "movie-1",
        title: "Heat",
        seasonNumber: 0,
        episodeNumber: 0,
        runtime: 170,
        overview: "A heist crew plans one last job.",
        stillThumbhash: "still-hash",
        isMovie: true,
      },
      shuffle: { scopeLabel: "Movies", onReshuffle: () => {}, onStop: () => {} },
    });
    expect(previewPlaceholder().className).not.toContain("blur-xl");
    expect(screen.getByText("A heist crew plans one last job.")).toBeTruthy();
  });

  it("leaves the placeholder clear for a started episode", () => {
    renderScreen({
      nextEpisode: { ...nextWithoutUrl, watchState: { played: false, position_seconds: 120 } },
    });
    expect(previewPlaceholder().className).not.toContain("blur-xl");
  });
});
