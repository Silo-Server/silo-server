// @vitest-environment jsdom

import type { ComponentProps } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setDateTimeFormatPreferences } from "@/lib/datetime";
import { PlayerControls } from "./PlayerControls";

function renderControls(
  markerEditAvailable: boolean,
  overrides: Partial<ComponentProps<typeof PlayerControls>> = {},
) {
  return render(
    <PlayerControls
      visible
      playing={false}
      currentTime={0}
      duration={120}
      buffered={null}
      markerEditAvailable={markerEditAvailable}
      onToggleMarkerEdit={vi.fn()}
      volume={1}
      muted={false}
      isFullscreen={false}
      videoFit="contain"
      onVideoFitToggle={vi.fn()}
      subtitleTracks={[]}
      activeSubtitleIndex={null}
      onSubtitleSelect={vi.fn()}
      subtitleDelayMs={0}
      onSubtitleDelayChange={vi.fn()}
      audioTracks={[]}
      activeAudioIndex={-1}
      qualityOptions={[]}
      activeQualityId="auto"
      isTranscoding={false}
      qualityError={null}
      onQualitySelect={vi.fn()}
      showPlaybackInfo={false}
      onTogglePlaybackInfo={vi.fn()}
      onPlayPause={vi.fn()}
      onSeek={vi.fn()}
      onSkip={{ back: vi.fn(), forward: vi.fn() }}
      skipSeconds={{ back: 10, forward: 30 }}
      onVolumeChange={vi.fn()}
      onMutedChange={vi.fn()}
      onFullscreenToggle={vi.fn()}
      {...overrides}
    />,
  );
}

describe("PlayerControls", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  describe("end time", () => {
    beforeEach(() => {
      vi.useFakeTimers({
        toFake: ["Date", "setInterval", "clearInterval", "setTimeout", "clearTimeout"],
      });
      vi.setSystemTime(new Date(2026, 10, 9, 18, 21, 0));
      setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "12h" });
    });
    afterEach(() => {
      vi.useRealTimers();
      setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "auto" });
    });

    it("keeps moving forward with the clock while paused", () => {
      renderControls(false, { playing: false, currentTime: 3600, duration: 6000 });
      expect(screen.getByText(/^Ends 7:01\sPM$/)).toBeTruthy();

      act(() => vi.advanceTimersByTime(60_000));

      expect(screen.getByText(/^Ends 7:02\sPM$/)).toBeTruthy();
    });

    it("counts real time at the current playback speed", () => {
      renderControls(false, { currentTime: 3600, duration: 6000, playbackSpeed: 2 });
      expect(screen.getByText(/^Ends 6:41\sPM$/)).toBeTruthy();
    });

    it("is hidden while the length is unknown", () => {
      renderControls(false, { currentTime: 0, duration: 0 });
      expect(screen.queryByText(/^Ends /)).toBeNull();
    });
  });

  it("moves desktop secondary controls into overflow as the player narrows", () => {
    const width = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1024);
    let resize = () => {};
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(callback: ResizeObserverCallback) {
          resize = () => callback([], {} as ResizeObserver);
        }
        observe() {}
        disconnect() {}
      },
    );

    renderControls(true);
    expect(screen.queryByRole("button", { name: "Edit markers" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "More player options" }));
    expect(screen.getByRole("menuitem", { name: "Edit markers" })).toBeInTheDocument();
    expect(screen.getByRole("slider", { name: "Volume" })).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull();

    width.mockReturnValue(1920);
    act(resize);
    expect(screen.queryByRole("button", { name: "More player options" })).toBeNull();
    expect(screen.getByRole("button", { name: "Edit markers" })).toBeInTheDocument();
    expect(screen.getByRole("slider", { name: "Volume" })).toBeInTheDocument();
  });

  it.each(["overflow", "Audio tracks", "Chapters"])(
    "does not reopen %s after widening and narrowing the player",
    (menu) => {
      const width = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1024);
      let resize = () => {};
      vi.stubGlobal(
        "ResizeObserver",
        class {
          constructor(callback: ResizeObserverCallback) {
            resize = () => callback([], {} as ResizeObserver);
          }
          observe() {}
          disconnect() {}
        },
      );
      renderControls(true, {
        audioTracks: [{ language: "en" }, { language: "fr" }],
        activeAudioIndex: 0,
        onAudioSelect: vi.fn(),
        chapters: [
          { index: 0, title: "Opening", start_seconds: 0, end_seconds: 120, source: "embedded" },
        ],
      });
      const openMenu = () => {
        fireEvent.click(screen.getByRole("button", { name: "More player options" }));
        if (menu !== "overflow") {
          fireEvent.click(screen.getByRole("menuitem", { name: menu }));
        }
        expect(screen.getByRole("menu")).toBeInTheDocument();
      };
      openMenu();

      width.mockReturnValue(1920);
      act(resize);
      expect(screen.queryByRole("menu")).toBeNull();
      width.mockReturnValue(1024);
      act(resize);
      expect(screen.queryByRole("menu")).toBeNull();
      expect(screen.getByRole("button", { name: "More player options" })).toHaveAttribute(
        "aria-expanded",
        "false",
      );
      openMenu();
    },
  );

  it("hides marker editing when unavailable", () => {
    renderControls(false);

    expect(screen.queryByRole("button", { name: "Edit markers" })).toBeNull();
  });

  it("toggles between Fit and Fill from the desktop utility rail", () => {
    const onVideoFitToggle = vi.fn();
    const { unmount } = renderControls(false, { onVideoFitToggle });

    const fill = screen.getByRole("button", { name: "Fill screen" });
    expect(fill).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(fill);
    expect(onVideoFitToggle).toHaveBeenCalledOnce();

    unmount();
    renderControls(false, { videoFit: "cover" });
    expect(screen.getByRole("button", { name: "Fill screen" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("uses the mobile transport and hides hardware-volume controls on coarse pointers", () => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        matches: true,
        media: "(pointer: coarse)",
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    );

    renderControls(false);

    expect(screen.getByRole("button", { name: "More player options" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Play" })).toHaveClass("h-16", "w-16");
    expect(screen.queryByRole("button", { name: /mute/i })).toBeNull();
  });

  it("stops hidden compact transport buttons from taking clicks", () => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        matches: true,
        media: "(pointer: coarse)",
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    );

    renderControls(false, { visible: false });
    const cluster = screen.getByRole("button", { name: "Back 10 seconds" }).parentElement;
    expect(cluster).not.toHaveClass("pointer-events-auto");
  });
});
