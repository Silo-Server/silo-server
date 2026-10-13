// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { AdminUserDownload, AdminUserDownloadSubscription } from "@/api/v2/adminUserActivity";
import {
  downloadStatusBadge,
  groupDeviceDownloads,
  seriesGroupStatus,
} from "./downloadPresentation";

function download(overrides: Partial<AdminUserDownload> = {}): AdminUserDownload {
  return {
    id: "1",
    profile_id: "p1",
    device_id: "dev-a",
    content_id: "movie-1",
    title: "Dune: Part Two",
    media_type: "movie",
    episode: null,
    status: "completed",
    quality: "original",
    effective_quality: "original",
    delivery_format: "original",
    target_bitrate_kbps: 0,
    file_size: 1,
    created_at: "2026-09-20T10:00:00.000Z",
    updated_at: "2026-09-20T10:00:00.000Z",
    completed_at: null,
    status_event_at: null,
    ...overrides,
  };
}

function episode(n: number, overrides: Partial<AdminUserDownload> = {}): AdminUserDownload {
  return download({
    id: `ep-${overrides.episode?.season_number ?? 3}-${n}`,
    content_id: "bear",
    episode_id: `bear-e${n}`,
    title: "The Bear",
    media_type: "series",
    episode: { season_number: 3, episode_number: n, title: `Episode ${n}` },
    ...overrides,
  });
}

function monitor(
  overrides: Partial<AdminUserDownloadSubscription> = {},
): AdminUserDownloadSubscription {
  return {
    id: "m1",
    profile_id: "p1",
    device_id: "dev-a",
    series_id: "bear",
    series_title: "The Bear",
    mode: "all",
    season_numbers: [],
    target_season: null,
    delete_watched: false,
    max_storage_bytes: 0,
    active: true,
    on_device: 0,
    in_progress: 0,
    removed_episodes: 0,
    created_at: "2026-09-20T10:00:00.000Z",
    updated_at: "2026-09-20T10:00:00.000Z",
    ...overrides,
  };
}

describe("downloadStatusBadge", () => {
  it.each([
    ["completed", false, "On device", "ok"],
    ["downloading", false, "Downloading", "accent"],
    ["ready", false, "Waiting for device", "neutral"],
    ["ready", true, "Requested", "neutral"],
    // Android never reports progress, so a downloading row is still a request.
    ["downloading", true, "Requested", "neutral"],
    ["preparing", true, "Server preparing", "neutral"],
    ["failed", false, "Failed", "danger"],
    ["revoked", false, "Revoked", "warn"],
    ["paused", false, "paused", "neutral"],
  ])("maps %s (android %s) to %s", (status, android, label, tone) => {
    expect(downloadStatusBadge(status, android)).toEqual({ label, tone });
  });
});

describe("groupDeviceDownloads", () => {
  it("folds a series' episodes into one ordered group and keeps movies apart", () => {
    const rows = [episode(3), download(), episode(1), episode(2)];
    const groups = groupDeviceDownloads(rows, [monitor()]);
    expect(groups.map((group) => group.key)).toEqual(["series:p1:bear", "item:1"]);
    const [bear, dune] = groups;
    expect(bear!.episodes.map((row) => row.episode?.episode_number)).toEqual([1, 2, 3]);
    expect(bear!.monitored).toBe(true);
    expect(dune!.movie?.title).toBe("Dune: Part Two");
    expect(dune!.monitored).toBe(false);
  });

  it("marks a series monitored only for an active monitor on the same device and profile", () => {
    const rows = [episode(1)];
    expect(groupDeviceDownloads(rows, [monitor({ active: false })])[0]!.monitored).toBe(false);
    expect(groupDeviceDownloads(rows, [monitor({ device_id: "dev-b" })])[0]!.monitored).toBe(false);
    expect(groupDeviceDownloads(rows, [monitor({ profile_id: "p2" })])[0]!.monitored).toBe(false);
  });

  it("sums a series group's status", () => {
    const [done] = groupDeviceDownloads([episode(1), episode(2)], []);
    expect(seriesGroupStatus(done!, false)).toEqual({ label: "On device", tone: "ok" });
    const [busy] = groupDeviceDownloads(
      [episode(1), episode(2), episode(3, { status: "downloading" })],
      [],
    );
    expect(seriesGroupStatus(busy!, false)).toEqual({ label: "1 downloading", tone: "accent" });
    const [asked] = groupDeviceDownloads(
      [episode(1, { status: "ready" }), episode(2, { status: "ready" })],
      [],
    );
    expect(seriesGroupStatus(asked!, true)).toEqual({ label: "Requested", tone: "neutral" });
    const [mixed] = groupDeviceDownloads([episode(1), episode(2, { status: "ready" })], []);
    expect(seriesGroupStatus(mixed!, false).label).toBe("1 waiting");
    const working = groupDeviceDownloads(
      [episode(1, { status: "downloading" }), episode(2, { status: "ready" })],
      [],
    );
    expect(seriesGroupStatus(working[0]!, false)).toEqual({
      label: "2 in progress",
      tone: "accent",
    });
    // On Android neither row reports progress, so both are requests.
    expect(seriesGroupStatus(working[0]!, true)).toEqual({ label: "Requested", tone: "neutral" });
    expect(seriesGroupStatus(mixed!, true).label).toBe("1 requested");
  });
});
