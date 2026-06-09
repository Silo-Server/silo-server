import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import type { ItemDetail } from "@/api/types";

const mocks = vi.hoisted(() => ({
  controller: null as null | {
    active: null | { contentId: string; playing: boolean; currentTime: number; duration: number; hasFile: boolean };
    activeRequest: null | { contentId: string };
    isBackgroundBarVisible: boolean;
    startPlayback: ReturnType<typeof vi.fn>;
    stopPlayback: ReturnType<typeof vi.fn>;
    toggleActivePlayback: ReturnType<typeof vi.fn>;
  },
  startPlayback: vi.fn(),
  toggleActivePlayback: vi.fn(),
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

import AudiobookContent from "./AudiobookContent";

function bookWithProgress(seconds: number): ItemDetail & { type: "audiobook" } {
  return {
    content_id: "book-1",
    type: "audiobook",
    title: "Test Book",
    year: 2024,
    overview: "",
    runtime: 0,
    content_rating: "",
    genres: [],
    rating_imdb: null,
    rating_tmdb: null,
    rating_rt_critic: null,
    rating_rt_audience: null,
    imdb_id: "",
    tmdb_id: "",
    tvdb_id: "",
    cast: [],
    crew: [],
    studios: [],
    networks: [],
    countries: [],
    release_date: null,
    first_air_date: null,
    last_air_date: null,
    season_count: null,
    poster_url: "",
    poster_thumbhash: "",
    backdrop_url: "",
    backdrop_thumbhash: "",
    logo_url: "",
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
    subtitles: [],
    intro: null,
    credits: null,
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
  };
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

  it("sends every Play-from-Start click to the shared audiobook player", async () => {
    render(
      <MemoryRouter>
        <AudiobookContent item={bookWithProgress(5000)} />
      </MemoryRouter>,
    );

    expect(mocks.startPlayback).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: /listen from start/i }));
    expect(mocks.startPlayback).toHaveBeenCalledTimes(1);
    expect(mocks.startPlayback.mock.calls[0]?.[0]).toMatchObject({
      contentId: "book-1",
      title: "Test Book",
      initialPositionSeconds: 0,
    });

    await userEvent.click(screen.getByRole("button", { name: /listen from start/i }));
    expect(mocks.startPlayback).toHaveBeenCalledTimes(2);
    expect(mocks.startPlayback.mock.calls[1]?.[0]).toMatchObject({
      contentId: "book-1",
      initialPositionSeconds: 0,
    });
  });

  it("sends resume and Listen-from-Start with the correct start positions", async () => {
    render(
      <MemoryRouter>
        <AudiobookContent item={bookWithProgress(5000)} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: /^resume/i }));
    expect(mocks.startPlayback).toHaveBeenCalledTimes(1);
    expect(mocks.startPlayback.mock.calls[0]?.[0]).toMatchObject({
      initialPositionSeconds: 5000,
    });

    await userEvent.click(screen.getByRole("button", { name: /listen from start/i }));
    expect(mocks.startPlayback).toHaveBeenCalledTimes(2);
    expect(mocks.startPlayback.mock.calls[1]?.[0]).toMatchObject({
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
