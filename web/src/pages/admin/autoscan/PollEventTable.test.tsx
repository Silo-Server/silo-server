import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import type {
  AutoscanEvent,
  AutoscanEventChange,
  AutoscanEventScanRun,
  Library,
} from "@/api/types";
import type { SourceLabelLookups } from "@/lib/autoscanLabels";

import { PollEventTable } from "./ActivityPanel";
import { splitDisplayPath } from "./pollEventDisplay";

const LIBRARIES = new Map<number, Library>([
  [1, { id: 1, name: "Movies" } as unknown as Library],
  [2, { id: 2, name: "TV Shows" } as unknown as Library],
]);

const LOOKUPS: SourceLabelLookups = {
  sourceByID: new Map(),
  connectionByID: new Map(),
  displayNames: new Map([["cephfs:scan-source.cephfs", "CephFS"]]),
};

function makeScanRun(
  index: number,
  overrides: Partial<AutoscanEventScanRun> = {},
): AutoscanEventScanRun {
  const num = String(index + 1).padStart(3, "0");
  return {
    id: `01RUN${num}`,
    library_id: index % 2 === 0 ? 1 : 2,
    mode: "subtree",
    path: `/mnt/media/movies/title-${num}`,
    trigger: "autoscan",
    status: "completed",
    requested_at: "2026-11-20T02:00:00Z",
    completed_at: "2026-11-20T02:00:05Z",
    result: {
      new: 1,
      updated: 0,
      unchanged: 0,
      missing: 0,
      missing_skipped_protected: 0,
      files_deleted: 0,
      items_deleted: 0,
      memberships_removed: 0,
      errors: 0,
      skipped: 0,
    },
    ...overrides,
  };
}

function makeChange(
  index: number,
  overrides: Partial<AutoscanEventChange> = {},
): AutoscanEventChange {
  const num = String(index + 1).padStart(3, "0");
  return {
    source_path: `/mnt/media/movies/title-${num}/movie-${num}.mkv`,
    rewritten_path: `/mnt/media/movies/title-${num}/movie-${num}.mkv`,
    outcome: "queued",
    library_id: 1,
    target_mode: "subtree",
    target_path: `/mnt/media/movies/title-${num}`,
    scan_run_id: `01RUN${num}`,
    ...overrides,
  };
}

function makeEvent(overrides: Partial<AutoscanEvent> = {}): AutoscanEvent {
  return {
    id: 42,
    source_id: null,
    plugin_id: "cephfs",
    capability_id: "scan-source.cephfs",
    started_at: "2026-11-20T02:00:00Z",
    completed_at: "2026-11-20T02:00:02Z",
    duration_ms: 2000,
    status: "success",
    changes_returned: 2,
    changes_resolved: 2,
    targets_claimed: 2,
    scans_created: 2,
    scans_reused: 0,
    scans_suppressed: 0,
    scan_runs: [makeScanRun(0), makeScanRun(1)],
    changes: [makeChange(0), makeChange(1)],
    changes_truncated: false,
    ...overrides,
  };
}

describe("splitDisplayPath", () => {
  it("splits parent directory and leaf filename cleanly", () => {
    expect(splitDisplayPath("/mnt/media/movies/Show/ep01.mkv")).toEqual({
      dir: "/mnt/media/movies/Show/",
      leaf: "ep01.mkv",
    });
  });

  it("highlights the final folder when a path has a trailing slash", () => {
    expect(splitDisplayPath("/mnt/media/movies/Season 01/")).toEqual({
      dir: "/mnt/media/movies/",
      leaf: "Season 01/",
    });
  });

  it("supports Windows paths and bare filenames", () => {
    expect(splitDisplayPath("D:\\Media\\Movie.mkv")).toEqual({
      dir: "D:\\Media\\",
      leaf: "Movie.mkv",
    });
    expect(splitDisplayPath("movie.mkv")).toEqual({
      dir: "",
      leaf: "movie.mkv",
    });
  });
});

describe("PollEventTable", () => {
  it("opens the inspector sheet instead of expanding inline details in the table", async () => {
    const user = userEvent.setup();
    const event = makeEvent({
      changes_returned: 107,
      targets_claimed: 41,
      scans_created: 41,
      changes_truncated: true,
      changes: [
        makeChange(0),
        makeChange(1, {
          outcome: "unresolved",
          reason: "no_library_match",
          library_id: undefined,
          target_mode: undefined,
          target_path: undefined,
          scan_run_id: undefined,
        }),
      ],
    });

    const { container } = render(
      <PollEventTable events={[event]} lookups={LOOKUPS} librariesByID={LIBRARIES} />,
    );

    // No inline <details> disclosure inside table rows.
    expect(container.querySelector("details")).toBeNull();

    // Open the inspector via the desktop table's Details button.
    const table = screen.getByRole("table");
    const detailsButton = within(table).getByRole("button", { name: /107 paths/i });
    await user.click(detailsButton);

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("CephFS")).toBeInTheDocument();
    expect(within(dialog).getByText(/Showing the first 2 of 107 changes/i)).toBeInTheDocument();
    expect(within(dialog).getByText("movie-001.mkv")).toBeInTheDocument();

    // Filter paths by outcome chip to isolate the unresolved entry.
    await user.click(within(dialog).getByRole("button", { name: /Unresolved/i }));
    expect(within(dialog).queryByText("movie-001.mkv")).not.toBeInTheDocument();
    expect(within(dialog).getByText("movie-002.mkv")).toBeInTheDocument();
  });

  it("paginates, searches, and filters polls with many scan runs", async () => {
    const user = userEvent.setup();
    const runs = Array.from({ length: 55 }, (_, i) =>
      makeScanRun(i, {
        status: i === 54 ? "failed" : "completed",
        error_message: i === 54 ? "storage root offline" : undefined,
      }),
    );
    const event = makeEvent({
      changes_returned: 55,
      targets_claimed: 55,
      scans_created: 55,
      scan_runs: runs,
      changes: [],
    });

    render(<PollEventTable events={[event]} lookups={LOOKUPS} librariesByID={LIBRARIES} />);

    // Open via desktop table Details button. Because changes is empty and scan_runs is populated,
    // the inspector opens directly on the Scans created tab.
    const table = screen.getByRole("table");
    await user.click(within(table).getByRole("button", { name: /55 paths/i }));

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByRole("tab", { name: /Scans created/i })).toHaveAttribute(
      "data-state",
      "active",
    );

    // First page shows the first 20 scans out of 55.
    expect(within(dialog).getByText("01RUN001")).toBeInTheDocument();
    expect(within(dialog).getByText("01RUN020")).toBeInTheDocument();
    expect(within(dialog).queryByText("01RUN021")).not.toBeInTheDocument();
    expect(dialog.textContent).toContain("Showing 1–20 of 55 scans");

    // Go to the last page and verify the 55th scan and its error message appear.
    await user.click(within(dialog).getByRole("button", { name: "Last page" }));
    expect(within(dialog).getByText("01RUN055")).toBeInTheDocument();
    expect(within(dialog).getByText("storage root offline")).toBeInTheDocument();

    // Filter to failed scans only.
    await user.click(within(dialog).getByRole("button", { name: /Failed/i }));
    expect(within(dialog).getByText("01RUN055")).toBeInTheDocument();
    expect(within(dialog).queryByText("01RUN041")).not.toBeInTheDocument();
    expect(dialog.textContent).toContain("Showing 1–1 of 1 scan");
  });

  it("jumps from a path's scan ID pill to that scan in the Scans created tab", async () => {
    const user = userEvent.setup();
    const runs = [makeScanRun(0), makeScanRun(1)];
    const changes = [makeChange(0), makeChange(1)];
    const event = makeEvent({ scan_runs: runs, changes });

    render(<PollEventTable events={[event]} lookups={LOOKUPS} librariesByID={LIBRARIES} />);

    const table = screen.getByRole("table");
    await user.click(within(table).getByRole("button", { name: /2 paths/i }));

    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "01RUN002" }));

    expect(within(dialog).getByRole("tab", { name: /Scans created/i })).toHaveAttribute(
      "data-state",
      "active",
    );
    expect(within(dialog).getByText("01RUN002")).toBeInTheDocument();
    expect(within(dialog).queryByText("01RUN001")).not.toBeInTheDocument();
  });

  it("restores focus to the opening Details button when closed", async () => {
    const user = userEvent.setup();
    const event = makeEvent();

    render(<PollEventTable events={[event]} lookups={LOOKUPS} librariesByID={LIBRARIES} />);

    const table = screen.getByRole("table");
    const detailsButton = within(table).getByRole("button", { name: /2 paths/i });
    await user.click(detailsButton);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(detailsButton).toHaveFocus();
  });

  it("preserves the latest observed scan update after the poll leaves the current page", async () => {
    const user = userEvent.setup();
    const initial = makeEvent({
      changes: [],
      scan_runs: [makeScanRun(0, { status: "running", result: undefined })],
    });
    const completed = makeEvent({
      changes: [],
      scan_runs: [makeScanRun(0, { status: "completed" })],
    });

    const { rerender } = render(
      <PollEventTable events={[initial]} lookups={LOOKUPS} librariesByID={LIBRARIES} />,
    );

    const table = screen.getByRole("table");
    await user.click(within(table).getByRole("button", { name: /2 paths/i }));

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("running")).toBeInTheDocument();

    rerender(<PollEventTable events={[completed]} lookups={LOOKUPS} librariesByID={LIBRARIES} />);
    expect(within(dialog).getByText("completed")).toBeInTheDocument();
    expect(within(dialog).getByText("1 new")).toBeInTheDocument();

    rerender(<PollEventTable events={[]} lookups={LOOKUPS} librariesByID={LIBRARIES} />);
    expect(within(dialog).getByText("completed")).toBeInTheDocument();
    expect(within(dialog).getByText("1 new")).toBeInTheDocument();
  });
});
