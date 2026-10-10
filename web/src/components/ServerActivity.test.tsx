import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TaskInfo } from "@/api/types";
import type { AdminDownloadPreparationList } from "@/api/v2/adminDownloadPreparations";
import { makePreparation, makePreparationList } from "@/test/downloadPreparations";

const mocks = vi.hoisted(() => ({
  tasks: [
    {
      key: "cache_metadata_images",
      name: "Cache Metadata Images",
      description: "Caches provider metadata artwork into object storage",
      category: "metadata",
      state: "running",
      progress: 0,
      manual_only: false,
      execution_scope: "process",
      triggers: [],
    },
  ] as TaskInfo[],
  preparations: undefined as AdminDownloadPreparationList | undefined,
}));

vi.mock("@/hooks/queries/admin/stats", () => ({
  useAdminSessions: () => ({ data: [] }),
}));
vi.mock("@/hooks/queries/admin/tasks", () => ({
  useTasksIncludingHidden: () => ({ data: mocks.tasks }),
}));
vi.mock("@/hooks/queries/admin/scans", () => ({
  useActiveScans: () => ({ data: [] }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [] }),
}));
vi.mock("@/hooks/queries/admin/downloadPreparations", () => ({
  useAdminDownloadPreparations: () => ({ data: mocks.preparations }),
}));
vi.mock("@/components/realtimeEventsContext", () => ({
  useRealtimeEvents: () => ({ connectionState: "live" }),
}));

import ServerActivity from "./ServerActivity";

const initialTasks = mocks.tasks;

beforeEach(() => {
  mocks.tasks = initialTasks;
  mocks.preparations = undefined;
});
afterEach(cleanup);

describe("ServerActivity task progress", () => {
  it("shows an indeterminate running state instead of a misleading zero percent", () => {
    render(
      <MemoryRouter>
        <ServerActivity />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Server activity: 1 active" }));

    expect(screen.queryByText("Preparing downloads")).not.toBeInTheDocument();
    expect(screen.getByText("Running")).toBeInTheDocument();
    expect(screen.queryByText("0%")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Cache Metadata Images" })).toHaveAttribute(
      "href",
      "/admin/tasks/cache_metadata_images",
    );
  });
});

describe("ServerActivity download preparation", () => {
  function openPopover(activeCount: number) {
    render(
      <MemoryRouter>
        <ServerActivity />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: `Server activity: ${activeCount} active` }));
  }

  it("counts running and queued jobs and shows live progress", () => {
    mocks.preparations = makePreparationList(
      [
        makePreparation(),
        makePreparation({
          id: "art-2",
          format: "remux",
          worker: { kind: "server", name: "api-1" },
          progress: undefined,
          media_title: "Severance",
          series_name: "Severance",
          season_number: 2,
          episode_number: 1,
        }),
      ],
      { queued: 4, retrying: 1, failed_recent: 2 },
    );
    // 1 task + 2 running + 4 queued + 1 retrying.
    openPopover(8);

    expect(screen.getByText("Preparing downloads")).toBeInTheDocument();
    expect(screen.getByText("7")).toBeInTheDocument();
    expect(screen.getByText("25%")).toBeInTheDocument();
    expect(
      screen.getByText("Transcode 1080p H.264 · gpu-01 · about 38 min left"),
    ).toBeInTheDocument();
    // A job with no reading yet shows a running label instead of 0%.
    expect(screen.getByText("Remuxing")).toBeInTheDocument();
    expect(screen.getByText("Severance S02E01")).toBeInTheDocument();
    expect(screen.getByText("4 queued · 1 waiting to retry")).toBeInTheDocument();
    expect(screen.getByText("2 failed in the last 24 hours")).toBeInTheDocument();
    for (const link of screen.getAllByRole("link", { name: /View all|Example Movie/ })) {
      if (link.textContent?.includes("Example Movie")) {
        expect(link).toHaveAttribute("href", "/admin/downloads?tab=preparation");
      }
    }
  });

  it("folds running jobs past the visible rows into the waiting line", () => {
    mocks.preparations = makePreparationList(
      Array.from({ length: 5 }, (_, i) => makePreparation({ id: `art-${i}` })),
    );
    openPopover(6);
    expect(screen.getAllByText("25%")).toHaveLength(3);
    expect(screen.getByText("2 more running")).toBeInTheDocument();
  });

  it("still shows recent failures when nothing else is active", () => {
    mocks.tasks = [];
    mocks.preparations = makePreparationList([], { failed_recent: 1 });
    render(
      <MemoryRouter>
        <ServerActivity />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Server activity" }));
    expect(screen.queryByText("No active server activity")).not.toBeInTheDocument();
    expect(screen.getByText("1 failed in the last 24 hours")).toBeInTheDocument();
  });

  it("says so when the queue is empty", () => {
    mocks.preparations = makePreparationList([]);
    openPopover(1);
    expect(screen.getByText("No downloads being prepared")).toBeInTheDocument();
  });
});
