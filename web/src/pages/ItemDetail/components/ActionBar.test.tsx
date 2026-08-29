import type { ComponentProps } from "react";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { FileVersion } from "@/api/types";
import ActionBar from "./ActionBar";

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));

vi.mock("./SubtitlesPopover", () => ({
  default: () => null,
}));

type ActionBarProps = ComponentProps<typeof ActionBar>;

const selectedVersion: FileVersion = {
  file_id: 1,
  resolution: "1080p",
  codec_video: "h264",
  codec_audio: "aac",
  hdr: false,
  container: "mkv",
  file_size: 0,
  duration: 7_200,
  bitrate: 0,
};

const playBranches: Array<[string, Partial<ActionBarProps>]> = [
  ["standard", {}],
  ["selected version", { selectedVersion }],
  ["resume choice", { playLabel: "Resume", restartHref: "/watch/movie-1?restart=1" }],
];

function renderActionBar(overrides: Partial<ActionBarProps> = {}) {
  return render(
    <MemoryRouter>
      <ActionBar playHref="/watch/movie-1" {...overrides} />
    </MemoryRouter>,
  );
}

describe("ActionBar", () => {
  it.each(playBranches)(
    "keeps the %s Play action on a compositor-only hover path",
    (_, overrides) => {
      renderActionBar(overrides);

      expect(screen.getByRole("button", { name: "Play" })).toHaveClass(
        "cursor-pointer",
        "transform-gpu",
        "transition-transform",
        "duration-150",
        "hover:bg-primary",
        "motion-safe:hover:scale-[1.02]",
        "motion-safe:active:scale-[0.98]",
        "motion-reduce:hover:bg-primary/90",
      );
    },
  );

  it("keeps the watched action on a compositor-only hover path and shows pointers", () => {
    renderActionBar({
      watchedLabel: "Mark Watched",
      onToggleWatched: () => {},
      onToggleFavorite: () => {},
      // The More button only renders when the overflow menu has at least one entry.
      onToggleWatchlist: () => {},
    });

    expect(screen.getByRole("button", { name: "Mark Watched" })).toHaveClass(
      "enabled:cursor-pointer",
      "transform-gpu",
      "transition-transform",
      "duration-150",
      "glass-hover",
      "glass-hover-surface",
      "motion-safe:hover:scale-[1.02]",
      "motion-safe:active:scale-[0.98]",
    );
    expect(screen.getByTitle("Favorite")).toHaveClass(
      "cursor-pointer",
      "glass-hover",
      "glass-hover-surface",
      "transition-none",
    );
    expect(screen.getByTitle("More")).toHaveClass(
      "cursor-pointer",
      "glass-hover",
      "glass-hover-surface",
      "transition-none",
    );
  });

  it("does not expose an enabled pointer affordance while the watched action is pending", () => {
    renderActionBar({
      watchedLabel: "Mark Watched",
      onToggleWatched: () => {},
      isUpdatingWatched: true,
    });

    const watchedAction = screen.getByRole("button", { name: "Mark Watched" });
    expect(watchedAction).toBeDisabled();
    expect(watchedAction).toHaveClass("enabled:cursor-pointer");
    expect(watchedAction).not.toHaveClass("cursor-pointer");
  });
});
