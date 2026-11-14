import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";

const mocks = vi.hoisted(() => ({
  controller: null as null | {
    active: null | {
      contentId: string;
      playing: boolean;
      currentTime: number;
      duration: number;
      hasFile: boolean;
    };
    activeRequest: null | { contentId: string };
    isBackgroundBarVisible: boolean;
    startPlayback: ReturnType<typeof vi.fn>;
    stopPlayback: ReturnType<typeof vi.fn>;
    toggleActivePlayback: ReturnType<typeof vi.fn>;
  },
  startPlayback: vi.fn(),
  toggleActivePlayback: vi.fn(),
  user: null as null | { role: "admin" | "user"; permissions?: string[] },
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ user: mocks.user }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { is_primary: true } }),
}));
vi.mock("@/components/EditMetadataDialog", () => ({
  default: ({ open, item }: { open: boolean; item: { title: string } }) =>
    open ? <div>Editing {item.title}</div> : null,
}));
vi.mock("@/components/MatchItemDialog", () => ({
  default: ({ open, item }: { open: boolean; item: { title: string; library_id?: number } }) =>
    open ? (
      <div>
        Matching {item.title} in {item.library_id}
      </div>
    ) : null,
}));

vi.mock("@/pages/audiobooks/player/audiobookPlaybackContext", () => ({
  useAudiobookPlaybackController: () => mocks.controller,
}));

vi.mock("@/components/AddToCollectionDialog", () => ({
  default: () => null,
}));
vi.mock("@/pages/audiobooks/components/ChaptersSection", () => ({ ChaptersSection: () => null }));
vi.mock("@/pages/audiobooks/components/NarratorCard", () => ({ NarratorCard: () => null }));
vi.mock("@/pages/audiobooks/components/NarratorPicker", () => ({ NarratorPicker: () => null }));
vi.mock("@/pages/audiobooks/components/RelatedRail", () => ({ RelatedRail: () => null }));
vi.mock("@/pages/ItemDetail/DetailHero", () => ({
  default: ({ actions }: { actions?: ReactNode }) => <div>{actions}</div>,
}));
vi.mock("@/pages/ItemDetail/components/MetadataBadges", () => ({
  default: () => null,
}));

import { makeItemDetail } from "./itemDetail.test-support";
import AudiobookContent from "./AudiobookContent";

function bookWithProgress(seconds: number) {
  return makeItemDetail("audiobook", {
    content_id: "book-1",
    title: "Test Book",
    versions: [
      {
        file_id: 1,
        file_path: "p",
        resolution: "",
        codec_video: "",
        codec_audio: "aac",
        hdr: false,
        container: "m4b",
        file_size: 1,
        duration: 36_000,
        bitrate: 128_000,
        chapters: [],
      },
    ],
    user_data: {
      played: false,
      is_in_progress: true,
      position_seconds: seconds,
      duration_seconds: 36_000,
    },
    audiobook: {
      authors: [{ name: "Author" }],
      narrators: [{ name: "Narrator" }],
      total_duration_seconds: 36_000,
      other_narrations: [],
      related: { also_by_author: [], similar: [] },
    },
  });
}

describe("AudiobookContent playback actions", () => {
  beforeEach(() => {
    mocks.startPlayback.mockClear();
    mocks.toggleActivePlayback.mockClear();
    mocks.controller = {
      active: null,
      activeRequest: null,
      isBackgroundBarVisible: false,
      startPlayback: mocks.startPlayback,
      stopPlayback: vi.fn(),
      toggleActivePlayback: mocks.toggleActivePlayback,
    };
  });

  it("sends resume and Listen-from-Start with the correct start positions", async () => {
    render(
      <MemoryRouter>
        <AudiobookContent item={bookWithProgress(5000)} />
      </MemoryRouter>,
    );

    expect(mocks.startPlayback).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: /^resume/i }));
    expect(mocks.startPlayback).toHaveBeenCalledTimes(1);
    // Smart rewind backs a cold resume up 10s so the listener regains context.
    expect(mocks.startPlayback.mock.calls[0]?.[0]).toMatchObject({
      initialPositionSeconds: 4990,
    });

    await userEvent.click(screen.getByRole("button", { name: /listen from start/i }));
    expect(mocks.startPlayback).toHaveBeenCalledTimes(2);
    expect(mocks.startPlayback.mock.calls[1]?.[0]).toMatchObject({
      contentId: "book-1",
      title: "Test Book",
      initialPositionSeconds: 0,
    });

    await userEvent.click(screen.getByRole("button", { name: /listen from start/i }));
    expect(mocks.startPlayback).toHaveBeenCalledTimes(3);
    expect(mocks.startPlayback.mock.calls[2]?.[0]).toMatchObject({
      contentId: "book-1",
      initialPositionSeconds: 0,
    });
  });

  it("toggles the active shared player instead of starting a duplicate", async () => {
    mocks.controller = {
      active: {
        contentId: "book-1",
        playing: true,
        currentTime: 5000,
        duration: 36_000,
        hasFile: true,
      },
      activeRequest: { contentId: "book-1" },
      isBackgroundBarVisible: true,
      startPlayback: mocks.startPlayback,
      stopPlayback: vi.fn(),
      toggleActivePlayback: mocks.toggleActivePlayback,
    };

    render(
      <MemoryRouter>
        <AudiobookContent item={bookWithProgress(5000)} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: /^pause/i }));

    expect(mocks.toggleActivePlayback).toHaveBeenCalledTimes(1);
    expect(mocks.startPlayback).not.toHaveBeenCalled();
  });
});

describe("AudiobookContent metadata actions", () => {
  beforeEach(() => {
    mocks.controller = null;
  });

  it("lets a metadata curator edit and match the book", async () => {
    mocks.user = { role: "user", permissions: ["metadata_curation"] };
    render(
      <MemoryRouter>
        <AudiobookContent item={bookWithProgress(0)} libraryId={7} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Edit Metadata" }));
    expect(screen.getByText("Editing Test Book")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Match Item" }));
    expect(screen.getByText("Matching Test Book in 7")).toBeInTheDocument();
  });

  it("offers no metadata actions to a listener", () => {
    mocks.user = { role: "user", permissions: [] };
    render(
      <MemoryRouter>
        <AudiobookContent item={bookWithProgress(0)} />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("button", { name: "More actions" })).not.toBeInTheDocument();
  });
});
