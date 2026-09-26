import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SubtitleSearchModal } from "./SubtitleSearchModal";
import type { PlayerConfig } from "../context/PlayerConfigContext";

const mocks = vi.hoisted(() => ({ v2: vi.fn() }));
vi.mock("../player-v2", () => ({ playerV2: mocks.v2 }));
vi.mock("@/components/subtitles/SubtitleUploadForm", () => ({ SubtitleUploadForm: () => null }));
afterEach(() => {
  cleanup();
  mocks.v2.mockReset();
});

it.each(["profile", "close"])(
  "does not publish an old download after %s changes",
  async (change) => {
    let profile = "profile-1";
    const config: PlayerConfig = {
      apiBaseUrl: "/api/v1",
      getAccessToken: () => "synthetic",
      getProfileId: () => profile,
      getDeviceId: () => "synthetic-device",
    };
    let finish!: (value: unknown) => void;
    const pending = new Promise((resolve) => {
      finish = resolve;
    });
    mocks.v2
      .mockResolvedValueOnce({
        results: [
          {
            provider: "example",
            id: "opaque",
            language: "en",
            release_name: "Synthetic selection",
            format: "srt",
            score: 80,
            hearing_impaired: false,
            downloads: 1,
          },
        ],
        warnings: [],
      })
      .mockReturnValueOnce(pending);
    const onSubtitleDownloaded = vi.fn(),
      onClose = vi.fn();
    const props = {
      playerConfig: config,
      mediaFileId: 42,
      isOpen: true,
      onSubtitleDownloaded,
      onClose,
    };
    const view = render(<SubtitleSearchModal {...props} />);
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.click(await screen.findByRole("button", { name: /Synthetic selection/ }));
    expect(mocks.v2).toHaveBeenLastCalledWith(config, "POST /api/v2/subtitles/download", {
      body: {
        media_file_id: "42",
        provider: "example",
        subtitle_id: "opaque",
        language: "en",
        release_name: "Synthetic selection",
        score: 80,
        hearing_impaired: false,
      },
    });
    if (change === "profile") profile = "profile-2";
    else view.rerender(<SubtitleSearchModal {...props} isOpen={false} />);
    await act(async () => {
      finish({ subtitle: { id: "7", media_file_id: "42" } });
      await pending;
    });
    expect(onSubtitleDownloaded).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(mocks.v2).toHaveBeenCalledTimes(2);
  },
);
