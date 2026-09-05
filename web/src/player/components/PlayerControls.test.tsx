// @vitest-environment jsdom

import type { ComponentProps } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
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
      subtitleTracks={[]}
      activeSubtitleIndex={null}
      onSubtitleSelect={vi.fn()}
      subtitleDelayMs={0}
      onSubtitleDelayChange={vi.fn()}
      audioTracks={[]}
      activeAudioIndex={-1}
      qualityOptions={[
        {
          id: "original",
          label: "Original",
          sublabel: "",
          resolution: "1080p",
          bitrateKbps: 0,
          isOriginal: true,
        },
      ]}
      activeQualityId="original"
      isTranscoding={false}
      qualityError={null}
      onQualitySelect={vi.fn()}
      showPlaybackInfo={false}
      onTogglePlaybackInfo={vi.fn()}
      onPlayPause={vi.fn()}
      onSeek={vi.fn()}
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

  it("shows marker editing when available", () => {
    renderControls(true);

    expect(screen.getByRole("button", { name: "Edit markers" })).toBeInTheDocument();
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
});
