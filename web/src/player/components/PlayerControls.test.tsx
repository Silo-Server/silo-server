// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PlayerControls } from "./PlayerControls";

function renderControls(markerEditAvailable: boolean) {
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
    />,
  );
}

describe("PlayerControls", () => {
  it("hides marker editing when unavailable", () => {
    renderControls(false);

    expect(screen.queryByRole("button", { name: "Edit markers" })).toBeNull();
  });

  it("shows marker editing when available", () => {
    renderControls(true);

    expect(screen.getByRole("button", { name: "Edit markers" })).toBeInTheDocument();
  });
});
