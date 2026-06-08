import { useEffect } from "react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import type { ItemDetail } from "@/api/types";

const mocks = vi.hoisted(() => ({
  playerMounts: [] as Array<{ initialPositionSeconds: number; mountId: number }>,
}));

let playerMountCounter = 0;
vi.mock("@/pages/audiobooks/player/AudiobookPlayer", () => ({
  default: function PlayerStub({ initialPositionSeconds }: { initialPositionSeconds: number }) {
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

describe("AudiobookContent Play-from-Start remount", () => {
  beforeEach(() => {
    mocks.playerMounts.length = 0;
    playerMountCounter = 0;
  });

  it("forces a player remount on every openPlayer call, even when startSeconds is unchanged", async () => {
    render(
      <MemoryRouter>
        <AudiobookContent item={bookWithProgress(5000)} />
      </MemoryRouter>,
    );

    expect(mocks.playerMounts).toHaveLength(0);

    await userEvent.click(screen.getByRole("button", { name: /play from start/i }));
    expect(mocks.playerMounts).toHaveLength(1);
    expect(mocks.playerMounts[0]?.initialPositionSeconds).toBe(0);

    await userEvent.click(screen.getByRole("button", { name: /play from start/i }));
    expect(mocks.playerMounts).toHaveLength(2);
    expect(mocks.playerMounts[1]?.initialPositionSeconds).toBe(0);
    expect(mocks.playerMounts[1]?.mountId).not.toBe(mocks.playerMounts[0]?.mountId);
  });

  it("remounts when switching from Resume to Play-from-Start", async () => {
    render(
      <MemoryRouter>
        <AudiobookContent item={bookWithProgress(5000)} />
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
