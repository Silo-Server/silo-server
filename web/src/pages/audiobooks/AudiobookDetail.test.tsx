import { useEffect } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";

// vi.hoisted() ensures the mock state exists before vi.mock factories run.
const mocks = vi.hoisted(() => ({
  useAudiobook: vi.fn(),
  playerMounts: [] as Array<{ initialPositionSeconds: number; mountId: number }>,
}));

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return {
    ...actual,
    useParams: () => ({ contentId: "book-1" }),
  };
});

vi.mock("@/hooks/audiobooks/useAudiobook", () => ({
  useAudiobook: (...args: unknown[]) => mocks.useAudiobook(...args),
}));

// Stub the player. Records a mount via useEffect(_, []) so the entry
// only fires on actual React mounts (not re-renders of the same instance).
// Two consecutive clicks of "Play from Start" should produce TWO entries
// when the playToken remount mechanism works; only ONE if React reuses
// the same instance (the bug we're catching).
let playerMountCounter = 0;
vi.mock("./player/AudiobookPlayer", () => ({
  default: ({ initialPositionSeconds }: { initialPositionSeconds: number }) => {
    useEffect(() => {
      playerMountCounter += 1;
      mocks.playerMounts.push({ initialPositionSeconds, mountId: playerMountCounter });
    }, []);
    return <div data-testid="player-stub">player @ {initialPositionSeconds}</div>;
  },
}));

vi.mock("@/components/AddToCollectionDialog", () => ({
  default: () => null,
}));
vi.mock("./components/ChaptersSection", () => ({ ChaptersSection: () => null }));
vi.mock("./components/NarratorCard", () => ({ NarratorCard: () => null }));
vi.mock("./components/NarratorPicker", () => ({ NarratorPicker: () => null }));
vi.mock("./components/RelatedRail", () => ({ RelatedRail: () => null }));
vi.mock("@/pages/ItemDetail/DetailHero", () => ({
  default: ({ actions }: { actions?: React.ReactNode }) => <div>{actions}</div>,
}));
vi.mock("@/pages/ItemDetail/components/MetadataBadges", () => ({
  default: () => null,
}));

import AudiobookDetail from "./AudiobookDetail";

function bookWithProgress(seconds: number) {
  return {
    audiobook: {
      content_id: "book-1",
      title: "Test Book",
      year: 2024,
      genres: [],
    },
    author: "Author",
    narrator: "Narrator",
    files: [
      {
        id: 1,
        path: "p",
        duration_seconds: 36000,
        chapters: [],
      },
    ],
    progress: { position_seconds: seconds, updated_at: "2026-01-01" },
  };
}

describe("AudiobookDetail Play-from-Start remount", () => {
  beforeEach(() => {
    mocks.playerMounts.length = 0;
    playerMountCounter = 0;
    mocks.useAudiobook.mockReset();
  });

  it("forces a player remount on every openPlayer call, even when startSeconds is unchanged", async () => {
    mocks.useAudiobook.mockReturnValue({
      data: bookWithProgress(5000),
      isLoading: false,
      error: null,
    });

    render(
      <MemoryRouter>
        <AudiobookDetail />
      </MemoryRouter>,
    );

    expect(mocks.playerMounts).toHaveLength(0);

    // First click: Play from Start — opens at 0.
    await userEvent.click(screen.getByRole("button", { name: /play from start/i }));
    expect(mocks.playerMounts).toHaveLength(1);
    expect(mocks.playerMounts[0]?.initialPositionSeconds).toBe(0);

    // Second click: Play from Start again — startSeconds is already 0,
    // but the player MUST remount (a new mount entry, not just a re-render)
    // so the underlying audio element resets to position 0.
    await userEvent.click(screen.getByRole("button", { name: /play from start/i }));
    expect(mocks.playerMounts).toHaveLength(2);
    expect(mocks.playerMounts[1]?.initialPositionSeconds).toBe(0);
    expect(mocks.playerMounts[1]?.mountId).not.toBe(mocks.playerMounts[0]?.mountId);
  });

  it("remounts when switching from Resume to Play-from-Start", async () => {
    mocks.useAudiobook.mockReturnValue({
      data: bookWithProgress(5000),
      isLoading: false,
      error: null,
    });

    render(
      <MemoryRouter>
        <AudiobookDetail />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: /^resume/i }));
    expect(mocks.playerMounts).toHaveLength(1);
    expect(mocks.playerMounts[0]?.initialPositionSeconds).toBe(5000);

    await userEvent.click(screen.getByRole("button", { name: /play from start/i }));
    expect(mocks.playerMounts).toHaveLength(2);
    expect(mocks.playerMounts[1]?.initialPositionSeconds).toBe(0);
  });
});
