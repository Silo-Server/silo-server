// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { FileVersion } from "@/api/types";
import { PROVISIONAL_INVENTORY_TIMEOUT_MS } from "@/player/utils/inventoryProvenance";
import ActionBar from "./ActionBar";

// The media page reads the selected file's probed tracks from the watch detail.
// These tests drive that read directly so they can hold it at "no rows yet"
// and then publish probe evidence for the same file.
const watchDetail = vi.hoisted(() => ({
  data: undefined as { versions?: FileVersion[] } | undefined,
  refetch: vi.fn(),
}));

const playback = vi.hoisted(() => ({ startPlayback: vi.fn() }));

vi.mock("@/hooks/queries/items", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/queries/items")>();
  return {
    ...actual,
    useWatchDetail: () => watchDetail,
    useDeleteMediaItem: () => ({ isPending: false, mutate: vi.fn() }),
  };
});

vi.mock("@/hooks/queries/subtitles", () => ({
  useDownloadedSubtitles: () => ({ data: [], isLoading: false }),
}));

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: playback.startPlayback }),
}));

vi.mock("@/components/AddToCollectionDialog", () => ({
  default: () => null,
}));

const selectedVersion: FileVersion = {
  file_id: 7,
  file_path: "/media/heat.mkv",
  resolution: "1080p",
  codec_video: "h264",
  codec_audio: "eac3",
  hdr: false,
  container: "mkv",
  file_size: 0,
  duration: 7_200,
  bitrate: 0,
  // The selected row's own release listing. It is not probe evidence, but it
  // is this row's inventory and must render immediately.
  audio_tracks: [{ language: "eng", codec: "eac3", channels: 6 }],
  subtitle_tracks: [{ index: 0, language: "en", codec: "subrip", title: "English" }],
};

const probedVersion: FileVersion = {
  ...selectedVersion,
  probed_at: "2026-11-13T19:36:20Z",
  audio_tracks: [{ language: "eng", codec: "eac3", channels: 6, default: true }],
  subtitle_tracks: [
    { index: 0, language: "en", codec: "subrip", title: "English" },
    { index: 1, language: "fr", codec: "subrip", title: "French" },
  ],
};

function actionBarUi(
  queryClient: QueryClient,
  overrides: Partial<Parameters<typeof ActionBar>[0]> = {},
) {
  return (
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ActionBar
          contentId="movie-1"
          versions={[selectedVersion]}
          selectedVersion={selectedVersion}
          playHref="/watch/movie-1"
          onSelectSubtitle={vi.fn()}
          onSelectSubtitleOff={vi.fn()}
          onSelectAudioTrack={vi.fn()}
          {...overrides}
        />
      </MemoryRouter>
    </QueryClientProvider>
  );
}

function renderActionBar(overrides: Partial<Parameters<typeof ActionBar>[0]> = {}) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const view = render(actionBarUi(queryClient, overrides));
  return {
    ...view,
    rerender: (next: Partial<Parameters<typeof ActionBar>[0]> = {}) =>
      view.rerender(actionBarUi(queryClient, { ...overrides, ...next })),
  };
}

function openPopover(name: RegExp) {
  fireEvent.click(screen.getByRole("button", { name }));
}

describe("ActionBar pre-play track inventory", () => {
  afterEach(() => {
    vi.useRealTimers();
    watchDetail.data = undefined;
    watchDetail.refetch.mockReset();
    playback.startPlayback.mockReset();
  });

  it("renders the selected row's declared tracks immediately, with no discovery state", () => {
    renderActionBar();

    expect(screen.getByRole("button", { name: /Audio/ })).not.toHaveTextContent("Discovering…");
    expect(screen.getByRole("button", { name: /Subs/ })).not.toHaveTextContent("Discovering…");

    openPopover(/Audio/);
    expect(screen.queryByText("Discovering audio tracks…")).toBeNull();
    expect(screen.getAllByText("English").length).toBeGreaterThan(0);
    expect(screen.getByText("From release listing")).toBeTruthy();
  });

  it("renders declared subtitles immediately instead of reporting them undiscovered", () => {
    renderActionBar();

    openPopover(/Subs/);
    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
    expect(screen.getAllByText("English").length).toBeGreaterThan(0);
    expect(screen.getByText("From release listing")).toBeTruthy();
  });

  it("replaces the declared view with probe evidence when it lands", () => {
    const view = renderActionBar();
    openPopover(/Subs/);
    expect(screen.getByText("From release listing")).toBeTruthy();
    expect(screen.queryByText("French")).toBeNull();

    watchDetail.data = { versions: [probedVersion] };
    view.rerender();

    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
    expect(screen.queryByText("From release listing")).toBeNull();
    expect(screen.getAllByText("English").length).toBeGreaterThan(0);
    expect(screen.getAllByText("French").length).toBeGreaterThan(0);
  });

  it("never reports a row with declared tracks as unavailable", () => {
    vi.useFakeTimers();
    renderActionBar();
    openPopover(/Audio/);

    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });

    expect(screen.queryByText("Audio tracks unavailable.")).toBeNull();
    expect(screen.queryByText("Unavailable")).toBeNull();
    expect(screen.getAllByText("English").length).toBeGreaterThan(0);
  });

  it("keeps Play available while the inventory probe is still running", () => {
    watchDetail.data = { versions: [{ ...selectedVersion, subtitle_tracks: [] }] };
    renderActionBar();

    const play = screen.getByRole("button", { name: /Play/ });
    expect(play).toBeEnabled();

    // The declared lists render immediately; starting playback never waits for
    // a probe.
    fireEvent.click(play);
    expect(playback.startPlayback).toHaveBeenCalledOnce();
  });

  it("shows discovery for a row with no declared tracks, then retries after the probe window", () => {
    vi.useFakeTimers();
    const emptyVersion: FileVersion = {
      ...selectedVersion,
      audio_tracks: [],
      subtitle_tracks: [],
    };
    watchDetail.data = { versions: [emptyVersion] };
    renderActionBar({ versions: [emptyVersion], selectedVersion: emptyVersion });
    openPopover(/Subs/);

    expect(screen.getByText("Discovering subtitles…")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Subs/ })).toHaveTextContent("Discovering…");

    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });

    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
    expect(screen.getByText("Subtitles unavailable.")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    expect(watchDetail.refetch).toHaveBeenCalledOnce();
    expect(screen.getByText("Discovering subtitles…")).toBeTruthy();
  });

  it("shows the same discovery-then-retry path for an empty audio inventory", () => {
    vi.useFakeTimers();
    const emptyVersion: FileVersion = {
      ...selectedVersion,
      audio_tracks: [],
      subtitle_tracks: [],
    };
    watchDetail.data = { versions: [emptyVersion] };
    renderActionBar({ versions: [emptyVersion], selectedVersion: emptyVersion });
    openPopover(/Audio/);

    expect(screen.getByText("Discovering audio tracks…")).toBeTruthy();

    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });

    expect(screen.getByText("Audio tracks unavailable.")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    expect(watchDetail.refetch).toHaveBeenCalledOnce();
    expect(screen.getByText("Discovering audio tracks…")).toBeTruthy();
  });

  it("keeps the empty subtitle state for a probed version with no subtitles", () => {
    const emptyProbedVersion: FileVersion = { ...probedVersion, subtitle_tracks: [] };
    watchDetail.data = { versions: [emptyProbedVersion] };
    renderActionBar({
      versions: [emptyProbedVersion],
      selectedVersion: emptyProbedVersion,
    });

    expect(screen.getByRole("button", { name: /Subs/ })).not.toHaveTextContent("Discovering…");
    openPopover(/Subs/);
    expect(screen.getByText("No subtitles available.")).toBeTruthy();
    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
  });

  it("does not report an unprobed empty inventory as no subtitles", () => {
    // The selected version has no probe stamp, so an empty list is "not
    // enumerated yet", not evidence the release carries no subtitles.
    watchDetail.data = { versions: [{ ...selectedVersion, subtitle_tracks: [] }] };
    const emptyVersion: FileVersion = { ...selectedVersion, subtitle_tracks: [] };
    renderActionBar({
      versions: [emptyVersion],
      selectedVersion: emptyVersion,
    });

    expect(screen.getByRole("button", { name: /Subs/ })).toHaveTextContent("Discovering…");
    openPopover(/Subs/);
    expect(screen.getByText("Discovering subtitles…")).toBeTruthy();
    expect(screen.queryByText("No subtitles available.")).toBeNull();
  });

  it("groups a declaration with no language or codec instead of inventing a track", () => {
    const placeholderVersion: FileVersion = {
      ...selectedVersion,
      audio_tracks: [{ channels: 2 }],
    };
    watchDetail.data = { versions: [placeholderVersion] };
    renderActionBar({ versions: [placeholderVersion], selectedVersion: placeholderVersion });

    openPopover(/Audio/);
    expect(screen.getByText("Audio · from release listing")).toBeTruthy();
    expect(screen.queryByText("Unknown")).toBeNull();
    expect(screen.queryByText("Track 1")).toBeNull();
    expect(screen.queryByText("Discovering audio tracks…")).toBeNull();
  });

  it("lists audio tracks once the watch detail carries a probe stamp", () => {
    watchDetail.data = { versions: [probedVersion] };
    renderActionBar({ versions: [probedVersion], selectedVersion: probedVersion });

    expect(screen.getByRole("button", { name: /Audio/ })).toHaveTextContent("English");

    openPopover(/Audio/);
    expect(screen.getAllByText("English").length).toBeGreaterThan(0);
    expect(screen.queryByText("Discovering audio tracks…")).toBeNull();
    expect(screen.queryByText("From release listing")).toBeNull();
  });
});
