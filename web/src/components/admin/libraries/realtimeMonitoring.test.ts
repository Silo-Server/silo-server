import { describe, expect, it } from "vitest";

import type { LibraryRealtimeMonitoringEntry, LibraryRealtimeMonitoringState } from "@/api/types";

import {
  realtimeMonitoringNeedsAttention,
  realtimeMonitoringStatusText,
} from "./realtimeMonitoring";

function entry(
  state: LibraryRealtimeMonitoringState,
  over: Partial<LibraryRealtimeMonitoringEntry> = {},
): LibraryRealtimeMonitoringEntry {
  return {
    library_id: 1,
    enabled: true,
    state,
    backend: "",
    detail: "",
    directories: 0,
    ...over,
  };
}

describe("realtimeMonitoringStatusText", () => {
  it.each([
    ["inotify", 4812, "Monitoring 4,812 folders (inotify)"],
    ["fanotify", 1, "Monitoring 1 folder (fanotify)"],
    ["", 12, "Monitoring 12 folders"],
  ])("counts folders and names the %s backend while monitoring", (backend, directories, text) => {
    expect(realtimeMonitoringStatusText(entry("monitoring", { backend, directories }))).toBe(text);
  });

  it("appends a caveat the server reports while monitoring", () => {
    expect(
      realtimeMonitoringStatusText(
        entry("monitoring", {
          backend: "inotify",
          directories: 3,
          detail: "Changes made outside this FUSE mount aren't seen",
        }),
      ),
    ).toBe("Monitoring 3 folders (inotify) · Changes made outside this FUSE mount aren't seen");
  });

  it.each<[LibraryRealtimeMonitoringState, string]>([
    ["server_disabled", "Turned off server-wide"],
    ["library_disabled", "Library is disabled"],
    ["monitoring_off", "Off"],
    ["not_reporting", "Not reporting"],
    ["starting", "Starting"],
    ["unsupported_filesystem", "Unsupported filesystem"],
    ["unsupported_platform", "Unsupported platform"],
    ["limit_reached", "Watch limit reached"],
    ["root_unavailable", "Folder unavailable"],
    ["error", "Error"],
  ])("labels %s and adds the server's detail", (state, label) => {
    expect(realtimeMonitoringStatusText(entry(state))).toBe(label);
    expect(realtimeMonitoringStatusText(entry(state, { detail: "Because." }))).toBe(
      `${label} · Because.`,
    );
  });

  it("reads a state this build does not know as an error", () => {
    const unknown = entry("error", {
      state: "paused" as LibraryRealtimeMonitoringState,
      detail: "x",
    });
    expect(realtimeMonitoringStatusText(unknown)).toBe("Error · x");
  });
});

describe("realtimeMonitoringNeedsAttention", () => {
  it.each<[LibraryRealtimeMonitoringState, boolean]>([
    ["limit_reached", true],
    ["root_unavailable", true],
    ["unsupported_filesystem", true],
    ["error", true],
    ["not_reporting", false],
    ["unsupported_platform", false],
    ["starting", false],
    ["monitoring", false],
    ["server_disabled", false],
    ["library_disabled", false],
    ["monitoring_off", false],
  ])("flags %s: %s", (state, flagged) => {
    expect(realtimeMonitoringNeedsAttention(entry(state))).toBe(flagged);
  });

  it("does not flag a library without a status entry", () => {
    expect(realtimeMonitoringNeedsAttention(undefined)).toBe(false);
  });
});
