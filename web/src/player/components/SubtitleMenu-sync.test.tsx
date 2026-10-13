import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import type { PlayerConfig } from "../context/PlayerConfigContext";
import type { SubtitleSync, SubtitleSyncEntry } from "../hooks/useSubtitleSync";
import type { PlayerSubtitleInfo } from "../types";
import type { StoredSubtitle, SubtitleSyncJob, SubtitleSyncState } from "../utils/subtitleSync";
import { SubtitleMenu } from "./SubtitleMenu";

const mocks = vi.hoisted(() => ({ v2: vi.fn() }));
vi.mock("../player-v2", () => ({ playerV2: mocks.v2 }));
vi.mock("./SubtitleAppearancePanel", () => ({ SubtitleAppearancePanel: () => null }));
vi.mock("./SubtitleSearchModal", () => ({
  SubtitleSearchModal: ({
    onSubtitleDownloaded,
  }: {
    onSubtitleDownloaded: (subtitle?: StoredSubtitle) => void;
  }) => (
    <button type="button" onClick={() => onSubtitleDownloaded(downloaded)}>
      Finish download
    </button>
  ),
}));

const config: PlayerConfig = {
  apiBaseUrl: "/api/v2",
  getAccessToken: () => "token",
  getProfileId: () => "profile-1",
  getDeviceId: () => "device",
};

function job(
  status: SubtitleSyncJob["status"],
  result?: SubtitleSyncJob["result"],
  extra?: Partial<SubtitleSyncJob>,
): SubtitleSyncJob {
  return {
    id: "1",
    status,
    trigger: "auto",
    confidence: 0.92,
    created_at: "2026-01-02T03:04:05.000Z",
    finished_at: null,
    result,
    ...extra,
  };
}

const SIDECAR = "external-" + "c".repeat(64);

function stored(key: string, overrides: Partial<SubtitleSyncState> = {}): SubtitleSyncState {
  const external = key.startsWith("external-");
  return {
    key,
    media_file_id: "42",
    source: external ? "external" : "downloaded",
    stored_subtitle_id: external ? undefined : key.replace("stored-", ""),
    language: "en",
    format: "srt",
    label: "Synthetic",
    timing: { offset_ms: 0, scale: 1 },
    ...overrides,
  };
}

const downloaded: StoredSubtitle = {
  id: "9",
  media_file_id: "42",
  provider: "opensubtitles",
  language: "en",
  format: "srt",
  release_name: "Synthetic",
  score: 80,
  hearing_impaired: false,
  created_at: "2026-01-02T03:04:05.000Z",
  timing: { offset_ms: 0, scale: 1 },
  sync: { ...job("pending"), subtitle_id: "9" },
};

const tracks: PlayerSubtitleInfo[] = [
  {
    index: 0,
    language: "en",
    label: "English",
    source: "embedded",
    url: "/stream/s/subtitles/0.vtt",
  },
  {
    index: 1,
    language: "en",
    label: "English",
    source: "downloaded",
    sync_key: "stored-7",
    url: "/stream/s/subtitles/1.vtt?file_id=42&downloaded_subtitle_id=7",
  },
  {
    index: 2,
    language: "fr",
    label: "French",
    source: "downloaded",
    sync_key: "stored-8",
    url: "/stream/s/subtitles/2.vtt?file_id=42&downloaded_subtitle_id=8",
  },
  {
    index: 3,
    language: "de",
    label: "Movie.de.srt",
    source: "external",
    sync_key: SIDECAR,
    url: "/stream/s/subtitles/3.vtt?file_id=42",
  },
];

function controller(entries: Record<string, SubtitleSyncEntry>): SubtitleSync {
  return {
    syncAvailable: true,
    entries,
    requestSync: vi.fn(async () => {}),
    resetTiming: vi.fn(async () => {}),
    remember: vi.fn(),
    timingChanged: vi.fn(),
    syncUpdated: vi.fn(),
    reload: vi.fn(),
  };
}

function openMenu(sync: SubtitleSync, activeIndex: number | null = 1) {
  render(
    <SubtitleMenu
      tracks={tracks}
      activeIndex={activeIndex}
      onSelect={() => {}}
      delayMs={0}
      onDelayChange={() => {}}
      mediaFileId={42}
      playerConfig={config}
      subtitleSync={sync}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /captions/ }));
}

beforeEach(() => {
  mocks.v2.mockResolvedValue({});
});

afterEach(() => {
  cleanup();
  mocks.v2.mockReset();
});

it("shows each syncable track's sync status and re-reads it when the menu opens", () => {
  const sync = controller({
    "stored-7": {
      state: stored("stored-7", {
        timing: { offset_ms: 2300, scale: 25 / 23.976 },
        sync: job("synced", { offset_ms: 2300, scale: 25 / 23.976 }),
      }),
    },
    "stored-8": { state: stored("stored-8", { sync: job("running") }) },
    [SIDECAR]: {
      state: stored(SIDECAR, {
        sync: job("running", undefined, { phase: "analyzing", progress: 0.4 }),
      }),
    },
  });
  openMenu(sync);

  expect(sync.reload).toHaveBeenCalledOnce();
  const labels = screen.getAllByTestId("subtitle-sync-status").map((el) => el.textContent);
  // The menu lists files next to the media first.
  expect(labels).toEqual(["Syncing… 40%", "Synced +2.3 s · 25→23.976 fps", "Syncing…"]);
});

it("offers sync and reset for the selected stored track", () => {
  const sync = controller({
    "stored-7": {
      state: stored("stored-7", { timing: { offset_ms: 2300, scale: 1 }, sync: job("synced") }),
    },
  });
  openMenu(sync);

  fireEvent.click(screen.getByRole("button", { name: "Sync to audio" }));
  expect(sync.requestSync).toHaveBeenCalledWith("stored-7");
  fireEvent.click(screen.getByRole("button", { name: "Reset timing" }));
  expect(sync.resetTiming).toHaveBeenCalledWith("stored-7");
});

it("hides reset for original timing and disables sync while one runs", () => {
  const sync = controller({
    "stored-7": {
      state: stored("stored-7", {
        sync: job("running", undefined, { phase: "matching", progress: 0.95 }),
      }),
    },
  });
  openMenu(sync);

  expect(screen.queryByRole("button", { name: "Reset timing" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Syncing…" })).toBeDisabled();
  const timing = screen.getByTestId("subtitle-timing");
  expect(within(timing).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "95");
  expect(within(timing).getByText("Matching lines to speech…")).toBeInTheDocument();
});

it("hides the sync action when the server cannot sync", () => {
  const sync = {
    ...controller({
      "stored-7": { state: stored("stored-7", { timing: { offset_ms: 100, scale: 1 } }) },
    }),
    syncAvailable: false,
  };
  openMenu(sync);

  expect(screen.queryByRole("button", { name: "Sync to audio" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Reset timing" })).toBeInTheDocument();
});

it("replaces the actions with a short explanation after a 403", () => {
  const sync = controller({
    "stored-7": {
      state: stored("stored-7", { timing: { offset_ms: 100, scale: 1 } }),
      forbidden: true,
    },
  });
  openMenu(sync);

  expect(screen.queryByRole("button", { name: "Sync to audio" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Reset timing" })).not.toBeInTheDocument();
  expect(
    screen.getByText("This server doesn't allow changing subtitle timing."),
  ).toBeInTheDocument();
});

it("shows no timing controls for an embedded track", () => {
  const sync = controller({ "stored-7": { state: stored("stored-7") } });
  openMenu(sync, 0);

  expect(screen.queryByText("Timing")).not.toBeInTheDocument();
});

it("hands a downloaded subtitle to the sync state so its auto-sync shows", () => {
  const sync = controller({});
  const onRefreshSubtitles = vi.fn();
  render(
    <SubtitleMenu
      tracks={tracks}
      activeIndex={null}
      onSelect={() => {}}
      delayMs={0}
      onDelayChange={() => {}}
      mediaFileId={42}
      playerConfig={config}
      onRefreshSubtitles={onRefreshSubtitles}
      subtitleSync={sync}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /captions/ }));
  fireEvent.click(screen.getByRole("menuitem", { name: "Add Subtitles…" }));
  fireEvent.click(within(document.body).getByRole("button", { name: "Finish download" }));

  expect(sync.remember).toHaveBeenCalledWith(downloaded);
  expect(onRefreshSubtitles).toHaveBeenCalledOnce();
});

it("offers sync for a subtitle file next to the media and says the file stays as it is", () => {
  const sync = controller({ [SIDECAR]: { state: stored(SIDECAR) } });
  openMenu(sync, 3);

  expect(
    screen.getByText(
      "Matches the timing to the audio for everyone. The file itself isn't changed.",
    ),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Sync to audio" }));
  expect(sync.requestSync).toHaveBeenCalledWith(SIDECAR);
});

it("explains how the last sync ended", () => {
  const sync = controller({
    [SIDECAR]: {
      state: stored(SIDECAR, { sync: job("failed", undefined, { failure: "no_audio" }) }),
    },
  });
  openMenu(sync, 3);
  expect(screen.getByText("This video has no audio Silo can read.")).toBeInTheDocument();
});
