import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import type { AutoscanEvent, AutoscanScan, Library } from "@/api/types";
import type { SourceLabelLookups } from "@/lib/autoscanLabels";
import { PollEventTable, ScanHistoryTable } from "./ActivityPanel";

const lookups: SourceLabelLookups = {
  sourceByID: new Map(),
  connectionByID: new Map(),
  displayNames: new Map(),
};
const librariesByID = new Map<number, Library>([[7, { id: 7, name: "TV Shows" } as Library]]);

const baseEvent: AutoscanEvent = {
  id: 1,
  source_id: null,
  plugin_id: "silo.autoscan.arr",
  capability_id: "radarr",
  started_at: "2026-09-01T00:00:00.000Z",
  completed_at: "2026-09-01T00:00:01.000Z",
  duration_ms: 1000,
  status: "success",
  changes_returned: 0,
  changes_resolved: 0,
  targets_claimed: 0,
  scans_created: 0,
  scans_reused: 0,
  scans_suppressed: 0,
  scan_runs: [],
  changes: [],
  changes_truncated: false,
};

const baseScan: AutoscanScan = {
  id: "01SCAN",
  library_id: 7,
  mode: "subtree",
  path: "/mnt/tv/Show",
  trigger: "autoscan",
  status: "completed",
  completed_at: "2026-09-01T00:00:05.000Z",
};

afterEach(cleanup);

// The desktop table and the mobile cards both render in jsdom; assert on the
// desktop table so each fact is found once.
function desktopTable(): HTMLElement {
  return screen.getByRole("table");
}

it("shows the unmapped path and why it did not resolve", () => {
  render(
    <PollEventTable
      events={[
        {
          ...baseEvent,
          status: "unresolved",
          changes_returned: 1,
          error_message: "returned 1 path(s) but none matched a Silo library folder",
          changes: [
            {
              source_path: "/movies/Film (2024)/Film.mkv",
              rewritten_path: "/movies/Film (2024)/Film.mkv",
              scope: "file",
              outcome: "unresolved",
              reason: "no_library_match",
              detail: "No library matches the given path",
            },
          ],
        },
      ]}
      lookups={lookups}
      librariesByID={librariesByID}
    />,
  );
  const table = within(desktopTable());
  expect(table.getByText("1 path · 0 linked")).toBeTruthy();
  expect(table.getByText("/movies/Film (2024)/Film.mkv")).toBeTruthy();
  expect(table.getByText(/No Silo library folder contains this path/)).toBeTruthy();
  expect(table.queryByText(/Rewritten to/)).toBeNull();
});

it("shows rewritten paths, joined scans, and suppressed changes", () => {
  render(
    <PollEventTable
      events={[
        {
          ...baseEvent,
          changes_returned: 60,
          changes_truncated: true,
          scans_reused: 1,
          scans_suppressed: 1,
          changes: [
            {
              source_path: "/data/tv/Show/S01E01.mkv",
              rewritten_path: "/mnt/tv/Show/S01E01.mkv",
              outcome: "joined",
              library_id: 7,
              target_mode: "subtree",
              target_path: "/mnt/tv/Show",
              scan_run_id: "01JOINEDRUN",
            },
            {
              source_path: "/data/tv/Show/S01E02.mkv",
              rewritten_path: "/mnt/tv/Show/S01E02.mkv",
              outcome: "suppressed",
              library_id: 7,
              target_mode: "file",
              target_path: "/mnt/tv/Show/S01E02.mkv",
            },
          ],
        },
      ]}
      lookups={lookups}
      librariesByID={librariesByID}
    />,
  );
  const table = within(desktopTable());
  expect(table.getByText("/mnt/tv/Show/S01E01.mkv")).toBeTruthy();
  expect(table.getAllByText("Rewritten to")).toHaveLength(2);
  expect(table.getByText("Joined scan")).toBeTruthy();
  expect(
    table.getByText(
      "Added to a scan that was already queued: Subtree scan · /mnt/tv/Show in TV Shows",
    ),
  ).toBeTruthy();
  expect(table.getByText("01JOINEDRUN")).toBeTruthy();
  expect(table.getByText("Suppressed")).toBeTruthy();
  expect(table.getByText(/Already requested within the debounce window/)).toBeTruthy();
  expect(table.getByText("Showing the first 2 of 60 changes.")).toBeTruthy();
  // The joined run was created by another event, but it is still linked. The
  // log is truncated, so runs joined by unrecorded changes may be missing.
  expect(table.getByText("60 paths · 1+ linked")).toBeTruthy();
  expect(
    table.getByText(/The changes joined scans that were already queued or running/),
  ).toBeTruthy();
});

it("explains a change that waits for a follow-up scan and shows unknown outcomes raw", () => {
  render(
    <PollEventTable
      events={[
        {
          ...baseEvent,
          changes_returned: 2,
          scans_reused: 1,
          changes: [
            {
              source_path: "/mnt/tv/Show/S01E02.mkv",
              rewritten_path: "/mnt/tv/Show/S01E02.mkv",
              outcome: "joined",
              reason: "follow_up_scan",
              library_id: 7,
              target_mode: "subtree",
              target_path: "/mnt/tv/Show",
            },
            {
              source_path: "/mnt/tv/Show/S01E03.mkv",
              rewritten_path: "/mnt/tv/Show/S01E03.mkv",
              outcome: "teleported",
              detail: "Sent somewhere new.",
            },
          ],
        },
      ]}
      lookups={lookups}
      librariesByID={librariesByID}
    />,
  );
  const table = within(desktopTable());
  expect(
    table.getByText(
      "A scan of this scope was already running, so it is scanned again when that scan finishes: Subtree scan · /mnt/tv/Show in TV Shows",
    ),
  ).toBeTruthy();
  expect(table.getByText("teleported")).toBeTruthy();
  expect(table.getByText("Sent somewhere new.")).toBeTruthy();
});

it("explains events recorded before paths were logged", () => {
  render(
    <PollEventTable
      events={[{ ...baseEvent, changes_returned: 3 }]}
      lookups={lookups}
      librariesByID={librariesByID}
    />,
  );
  expect(within(desktopTable()).getByText("Paths were not recorded for this event.")).toBeTruthy();
});

it("shows linked scan results in the expanded event", () => {
  render(
    <PollEventTable
      events={[
        {
          ...baseEvent,
          changes_returned: 1,
          scans_created: 1,
          scan_runs: [
            {
              id: "01RUN",
              library_id: 7,
              mode: "subtree",
              path: "/mnt/tv/Show",
              trigger: "autoscan",
              status: "completed",
              result: {
                new: 4,
                updated: 0,
                unchanged: 10,
                missing: 0,
                missing_skipped_protected: 0,
                files_deleted: 0,
                items_deleted: 0,
                memberships_removed: 0,
                errors: 0,
                skipped: 0,
              },
            },
          ],
        },
      ]}
      lookups={lookups}
      librariesByID={librariesByID}
    />,
  );
  expect(within(desktopTable()).getByText("4 new")).toBeTruthy();
});

const emptyResult = {
  new: 0,
  updated: 0,
  unchanged: 0,
  missing: 0,
  missing_skipped_protected: 0,
  files_deleted: 0,
  items_deleted: 0,
  memberships_removed: 0,
  errors: 0,
  skipped: 0,
};

it("distinguishes skipped, productive, and pending scans in the scan history", () => {
  render(
    <ScanHistoryTable
      scans={[
        { ...baseScan, id: "01SKIPPED", result: { ...emptyResult, skipped: 1 } },
        {
          ...baseScan,
          id: "01ADDED",
          result: { ...emptyResult, new: 4, updated: 1, missing: 2 },
        },
        { ...baseScan, id: "01NOOP", result: { ...emptyResult, unchanged: 12 } },
        {
          ...baseScan,
          id: "01REMOVED",
          result: { ...emptyResult, files_deleted: 2, items_deleted: 1, unchanged: 5 },
        },
        { ...baseScan, id: "01ITEMS", result: { ...emptyResult, items_deleted: 3 } },
        { ...baseScan, id: "01MEMBERS", result: { ...emptyResult, memberships_removed: 2 } },
        { ...baseScan, id: "01RUNNING", status: "running" },
      ]}
      librariesByID={librariesByID}
      lookups={lookups}
    />,
  );
  const table = within(desktopTable());
  expect(table.getByRole("columnheader", { name: "Result" })).toBeTruthy();
  expect(table.getByText("Skipped — overlapping scan in progress")).toBeTruthy();
  expect(table.getByText("4 new · 1 updated · 2 missing")).toBeTruthy();
  expect(table.getByText("No changes · 12 unchanged")).toBeTruthy();
  expect(table.getByText("2 files removed · 1 item removed")).toBeTruthy();
  // Removing catalog items is a change even when no file was deleted.
  expect(table.getByText("3 items removed")).toBeTruthy();
  // So is dropping a title from this library without deleting it.
  expect(table.getByText("2 titles removed from library")).toBeTruthy();
  expect(table.getAllByText(/^No changes/)).toHaveLength(1);
  const runningRow = table.getByText("01RUNNING").closest("tr");
  expect(runningRow && within(runningRow).getAllByText("-").length).toBeGreaterThan(0);
});
