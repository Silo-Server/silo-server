// @vitest-environment jsdom

import type { ComponentProps } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PlayerControls } from "./PlayerControls";

// The appearance panel reads settings through react-query; the controls' own
// rendering does not need a provider.
vi.mock("./SubtitleAppearancePanel", () => ({
  SubtitleAppearancePanel: () => null,
}));

function controlsProps(
  markerEditAvailable: boolean,
  overrides: Partial<ComponentProps<typeof PlayerControls>> = {},
): ComponentProps<typeof PlayerControls> {
  return {
    visible: true,
    playing: false,
    currentTime: 0,
    duration: 120,
    buffered: null,
    markerEditAvailable,
    onToggleMarkerEdit: vi.fn(),
    volume: 1,
    muted: false,
    isFullscreen: false,
    videoFit: "contain",
    onVideoFitToggle: vi.fn(),
    subtitleTracks: [],
    activeSubtitleIndex: null,
    onSubtitleSelect: vi.fn(),
    subtitleDelayMs: 0,
    onSubtitleDelayChange: vi.fn(),
    audioTracks: [],
    activeAudioIndex: -1,
    qualityOptions: [
      {
        id: "original",
        label: "Original",
        sublabel: "",
        resolution: "1080p",
        bitrateKbps: 0,
        isOriginal: true,
      },
    ],
    activeQualityId: "original",
    isTranscoding: false,
    qualityError: null,
    onQualitySelect: vi.fn(),
    showPlaybackInfo: false,
    onTogglePlaybackInfo: vi.fn(),
    onPlayPause: vi.fn(),
    onSeek: vi.fn(),
    onSkip: { back: vi.fn(), forward: vi.fn() },
    skipSeconds: { back: 10, forward: 30 },
    onVolumeChange: vi.fn(),
    onMutedChange: vi.fn(),
    onFullscreenToggle: vi.fn(),
    ...overrides,
  };
}

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

  it.each(["overflow", "Audio", "Chapters"])(
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
        if (menu === "Audio") {
          // The narrow bar's audio control opens the menu in place; it is no
          // longer an entry in the overflow sheet.
          fireEvent.click(screen.getByRole("button", { name: "Audio" }));
        } else {
          fireEvent.click(screen.getByRole("button", { name: "More player options" }));
          if (menu !== "overflow") {
            fireEvent.click(screen.getByRole("menuitem", { name: menu }));
          }
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

  it("disables the track menu triggers while a replace or replan is in flight", () => {
    renderControls(false, {
      audioTracks: [{ title: "English", codec: "eac3", channels: 6 }],
      onAudioSelect: vi.fn(),
      subtitleTracks: [
        {
          index: 0,
          language: "en",
          codec: "srt",
          label: "English",
          source: "embedded",
          url: "/stream/session-1/subtitles/0.vtt",
        },
      ],
      trackMenusLocked: true,
    });

    expect(screen.getByRole("button", { name: "Audio tracks" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Enable captions" })).toBeDisabled();
  });

  // The cold-plan-to-verified-push transition: the menus derive from the
  // session's track lists, so the same mounted controls grow an audio button and
  // a populated subtitle list in place as soon as those lists arrive — no
  // remount, no transport change.
  it("mounts the audio button and subtitle list when the track lists arrive", () => {
    // The narrow layout is the one that used to hide the audio selector inside
    // the overflow sheet.
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1024);
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(callback: ResizeObserverCallback) {
          callback([], {} as ResizeObserver);
        }
        observe() {}
        disconnect() {}
      },
    );
    const view = renderControls(false, {
      onAudioSelect: vi.fn(),
      mediaFileId: 8,
      audioTracks: [],
      subtitleTracks: [],
    });
    expect(screen.queryByRole("button", { name: "Audio" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Enable captions" })).toBeInTheDocument();

    view.rerender(
      <PlayerControls
        {...controlsProps(false, {
          onAudioSelect: vi.fn(),
          mediaFileId: 8,
          audioTracks: [
            { title: "English", codec: "eac3", channels: 6, default: true },
            { title: "Spanish", codec: "ac3", channels: 2 },
          ],
          subtitleTracks: [
            {
              index: 0,
              language: "en",
              codec: "srt",
              label: "English",
              source: "embedded",
              url: "/stream/session-1/subtitles/0.vtt",
            },
          ],
        })}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Audio" }));
    expect(screen.getByRole("menuitem", { name: /English/ })).toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: "Enable captions" }));
    expect(screen.getByRole("menuitem", { name: /English/ })).toBeInTheDocument();
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

describe("narrow player audio control", () => {
  function stubNarrowPlayer() {
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1024);
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(callback: ResizeObserverCallback) {
          callback([], {} as ResizeObserver);
        }
        observe() {}
        disconnect() {}
      },
    );
  }

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  // A laptop-width player showed no audio control at all: the menu existed only
  // inside "More player options", and the validation run reported the player as
  // having no audio selector. Clicking the control must open the plan's audio
  // menu — it must not reach the seek surface underneath and move the playhead.
  it("opens the audio menu in place and never seeks", () => {
    stubNarrowPlayer();
    const onSeek = vi.fn();
    const onAudioSelect = vi.fn();
    renderControls(false, {
      onSeek,
      onAudioSelect,
      onPlayPause: vi.fn(),
      currentTime: 42,
      audioTracks: [
        { title: "Korean TrueHD", language: "ko", codec: "truehd", channels: 8, default: true },
        { title: "Korean AC-3", language: "ko", codec: "ac3", channels: 6 },
      ],
      activeAudioIndex: 0,
    });

    const audio = screen.getByRole("button", { name: "Audio" });
    expect(audio).toHaveAttribute("aria-haspopup", "menu");
    fireEvent.pointerDown(audio);
    fireEvent.click(audio);

    expect(screen.getByRole("menu")).toBeInTheDocument();
    expect(screen.getByText("Korean TrueHD")).toBeInTheDocument();
    expect(onSeek).not.toHaveBeenCalled();
    expect(onAudioSelect).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("menuitem", { name: /Korean AC-3/ }));
    expect(onAudioSelect).toHaveBeenCalledWith(1, 42);
  });

  it("offers the plan's audio inventory for a transcoded source", () => {
    stubNarrowPlayer();
    // A server transcode plan publishes its own audio inventory: both the
    // source tracks are selectable even though the delivered stream carries one.
    renderControls(false, {
      onAudioSelect: vi.fn(),
      audioTracks: [
        { title: "TrueHD Atmos 7.1", language: "ko", codec: "truehd", channels: 8, default: true },
        { title: "AC-3 5.1", language: "ko", codec: "ac3", channels: 6 },
      ],
      activeAudioIndex: 1,
      isTranscoding: true,
    });
    fireEvent.click(screen.getByRole("button", { name: "Audio" }));
    expect(screen.getAllByRole("menuitem")).toHaveLength(2);
  });
});
