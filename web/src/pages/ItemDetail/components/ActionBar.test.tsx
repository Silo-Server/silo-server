import type { ComponentProps } from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
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
  it.each([true, false])(
    "announces watched state %s independently of label wording",
    (isWatched) => {
      renderActionBar({
        compactMobile: true,
        isWatched,
        watchedLabel: "Change watch status",
        onToggleWatched: vi.fn(),
      });
      expect(screen.getByRole("button", { name: "Change watch status" })).toHaveAttribute(
        "aria-pressed",
        String(isWatched),
      );
    },
  );

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

it("keeps compact secondary actions available in the overflow menu", () => {
  const onToggleFavorite = vi.fn();
  const onRatingChange = vi.fn();
  renderActionBar({ compactMobile: true, onToggleFavorite, onRatingChange });
  fireEvent.click(screen.getByRole("button", { name: "More actions" }));
  const menu = screen.getByRole("menu");
  fireEvent.keyDown(within(menu).getByRole("radio", { name: "1 star" }), { key: "ArrowRight" });
  expect(onRatingChange).toHaveBeenCalledWith(1);
  fireEvent.click(within(menu).getByRole("menuitem", { name: "Add to favorites" }));
  expect(onToggleFavorite).toHaveBeenCalledOnce();
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
});

it("skips desktop-hidden actions when focusing and navigating the menu", () => {
  const rects = vi.spyOn(HTMLElement.prototype, "getClientRects").mockImplementation(function (
    this: HTMLElement,
  ) {
    return (this.closest(".detail-mobile-menu-actions")
      ? []
      : [new DOMRect(0, 0, 100, 30)]) as unknown as DOMRectList;
  });
  try {
    renderActionBar({
      compactMobile: true,
      onToggleFavorite: vi.fn(),
      onToggleWatchlist: vi.fn(),
      canCurateMetadata: true,
      onEditMetadata: vi.fn(),
    });
    fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    const first = screen.getByRole("menuitem", { name: "Add to Watchlist" });
    const last = screen.getByRole("menuitem", { name: "Edit Metadata" });
    expect(first).toHaveFocus();
    fireEvent.keyDown(first, { key: "ArrowUp" });
    expect(last).toHaveFocus();
    fireEvent.keyDown(last, { key: "ArrowDown" });
    expect(first).toHaveFocus();
    fireEvent.keyDown(first, { key: "End" });
    expect(last).toHaveFocus();
    fireEvent.keyDown(last, { key: "Home" });
    expect(first).toHaveFocus();
    fireEvent.keyDown(first, { key: "e" });
    expect(last).toHaveFocus();
  } finally {
    rects.mockRestore();
  }
});

it.each([null, 3])("focuses the active star in a rating-only menu (rating %s)", (rating) => {
  const rects = vi
    .spyOn(HTMLElement.prototype, "getClientRects")
    .mockReturnValue([new DOMRect(0, 0, 100, 30)] as unknown as DOMRectList);
  try {
    const onRatingChange = vi.fn();
    renderActionBar({ compactMobile: true, rating, onRatingChange });
    const trigger = screen.getByRole("button", { name: "More actions" });
    fireEvent.click(trigger);
    const menu = screen.getByRole("menu");
    const star = within(menu).getByRole("radio", { name: rating === 3 ? "3 stars" : "1 star" });
    expect(star).toHaveFocus();
    for (const key of ["ArrowDown", "ArrowUp", "Home", "End"]) {
      trigger.focus();
      fireEvent.keyDown(menu, { key });
      expect(star).toHaveFocus();
    }
    fireEvent.keyDown(star, { key: "ArrowRight" });
    expect(onRatingChange).toHaveBeenCalledWith((rating ?? 0) + 1);
    fireEvent.keyDown(star, { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  } finally {
    rects.mockRestore();
  }
});
