import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import SeasonEpisodeGrid from "./SeasonEpisodeGrid";

const capturedMenuProps: Record<string, unknown>[] = [];
const spoilerPrefs = { hideImages: false, hideOverviews: false };

vi.mock("@/components/MediaItemMenu", () => ({
  default: (props: Record<string, unknown>) => {
    capturedMenuProps.push(props);
    return null;
  },
}));

vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ prefs: null, quickActionMode: "watched" }),
}));

vi.mock("@/hooks/useEpisodeSpoilerPrefs", () => ({
  useEpisodeSpoilerPrefs: () => spoilerPrefs,
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  usePrefetchCatalogItemDetail: () => vi.fn(),
}));

describe("SeasonEpisodeGrid", () => {
  beforeEach(() => {
    capturedMenuProps.length = 0;
    spoilerPrefs.hideImages = false;
    spoilerPrefs.hideOverviews = false;
  });

  it("hides the still and overview only for episodes the profile has not started", () => {
    spoilerPrefs.hideImages = true;
    spoilerPrefs.hideOverviews = true;
    const episode = (n: number, userData?: Record<string, unknown>) => ({
      content_id: `ep-${n}`,
      season_number: 1,
      episode_number: n,
      title: `Episode title ${n}`,
      overview: `Overview ${n}`,
      air_date: null,
      runtime: 42,
      still_url: `https://img.test/still-${n}.jpg`,
      still_thumbhash: "",
      files: [],
      user_data: userData as never,
    });
    render(
      <MemoryRouter>
        <SeasonEpisodeGrid
          isLoading={false}
          episodes={[
            episode(1, { played: true }),
            episode(2, { played: false, is_in_progress: true, position_seconds: 300 }),
            episode(3, { played: false }),
            episode(4),
          ]}
        />
      </MemoryRouter>,
    );

    for (const n of [1, 2]) {
      expect(screen.getByAltText(`Episode title ${n}`)).not.toHaveClass("blur-xl");
      expect(screen.getByText(`Overview ${n}`)).toBeInTheDocument();
    }
    for (const n of [3, 4]) {
      expect(screen.getByAltText(`Episode title ${n}`)).toHaveClass("blur-xl");
      expect(screen.queryByText(`Overview ${n}`)).not.toBeInTheDocument();
    }
  });

  it("marks an episode only when none of its files can be read", () => {
    const file = {
      resolution: "",
      codec_video: "",
      hdr: false,
      audio_channels: 0,
      container: "",
      file_size: 0,
    };
    const episode = (id: string, number: number, files: object[]) => ({
      content_id: id,
      season_number: 1,
      episode_number: number,
      title: `Episode title ${number}`,
      overview: "",
      air_date: null,
      runtime: 0,
      still_url: "",
      still_thumbhash: "",
      files: files as never,
    });
    render(
      <MemoryRouter>
        <SeasonEpisodeGrid
          isLoading={false}
          episodes={[
            episode("ep-3", 3, [{ ...file, file_id: 3, unreadable: true }]),
            episode("ep-4", 4, [
              { ...file, file_id: 4, unreadable: true },
              { ...file, file_id: 5, resolution: "1080p" },
            ]),
            episode("ep-5", 5, [{ ...file, file_id: 6, resolution: "1080p" }]),
          ]}
        />
      </MemoryRouter>,
    );

    expect(capturedMenuProps[0]).toMatchObject({
      contentId: "ep-3",
      mediaType: "episode",
      userState: {
        played: false,
        is_favorite: false,
        in_watchlist: false,
      },
      showCollectionActions: false,
      showWatchedShortcut: true,
      hasPartialProgress: false,
      quickActionMode: "watched",
    });

    // Only episode 3 has no readable version; episode 4 still plays its
    // second file.
    expect(screen.getAllByText("Damaged file")).toHaveLength(1);
    const card = screen.getByText("Episode title 3").closest(".media-card");
    expect(card).not.toBeNull();
    expect(card).toHaveTextContent("Damaged file");
  });
});
