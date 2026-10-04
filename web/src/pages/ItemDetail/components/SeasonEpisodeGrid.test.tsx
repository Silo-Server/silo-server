import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import SeasonEpisodeGrid from "./SeasonEpisodeGrid";

const capturedMenuProps: Record<string, unknown>[] = [];

vi.mock("@/components/MediaItemMenu", () => ({
  default: (props: Record<string, unknown>) => {
    capturedMenuProps.push(props);
    return null;
  },
}));

vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ prefs: null, quickActionMode: "watched" }),
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  usePrefetchCatalogItemDetail: () => vi.fn(),
}));

describe("SeasonEpisodeGrid", () => {
  beforeEach(() => {
    capturedMenuProps.length = 0;
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
