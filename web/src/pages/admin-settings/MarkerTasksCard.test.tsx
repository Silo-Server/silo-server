import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { TaskInfo } from "@/api/types";
import { formatDateTime } from "@/lib/datetime";
import { MarkerTasksCard } from "./MarkerTasksCard";

let tasks: TaskInfo[] = [];
const runTask = vi.fn();

vi.mock("@/components/realtimeEventsContext", () => ({
  useEventChannel: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/tasks", () => ({
  useTasks: () => ({ data: tasks }),
  useRunTask: () => ({ mutateAsync: runTask }),
}));

function task(
  key: string,
  resultData?: Partial<NonNullable<TaskInfo["last_execution"]>["result_data"]>,
): TaskInfo {
  return {
    key,
    name: key,
    description: "Marker task",
    category: "library",
    execution_scope: "process",
    state: "idle",
    progress: 0,
    manual_only: false,
    triggers: [],
    last_execution: {
      task_key: key,
      status: "completed",
      started_at: "2026-09-21T00:00:00Z",
      completed_at: "2026-09-21T00:01:00Z",
      duration_ms: 60_000,
      result_data: resultData
        ? { submitted: 0, skipped: 0, invalid: 0, failed: 0, retry_after_seconds: 0, ...resultData }
        : undefined,
    },
  };
}

function renderCard() {
  return render(
    <MemoryRouter>
      <MarkerTasksCard />
    </MemoryRouter>,
  );
}

describe("MarkerTasksCard", () => {
  beforeEach(() => {
    tasks = [];
    runTask.mockReset();
    runTask.mockResolvedValue(undefined);
  });

  it("runs online sync and links to its history before the local marker tasks", async () => {
    const user = userEvent.setup();
    tasks = [task("detect_intro_markers"), task("contribute_markers"), task("sync_markers")];

    renderCard();

    expect(screen.getAllByRole("heading", { level: 3 })[0]).toHaveTextContent("sync_markers");
    expect(screen.getAllByRole("link", { name: "History" })[0]).toHaveAttribute(
      "href",
      "/admin/tasks/sync_markers",
    );
    await user.click(screen.getAllByRole("button", { name: "Run now" })[0]!);

    expect(runTask).toHaveBeenCalledExactlyOnceWith("sync_markers");
  });

  it("disables the online sync action when the task is unavailable", () => {
    tasks = [task("detect_intro_markers"), task("contribute_markers")];

    renderCard();

    expect(screen.getAllByRole("button", { name: "Run now" })[0]).toBeDisabled();
    expect(screen.getAllByRole("link", { name: "History" })).toHaveLength(2);
    expect(runTask).not.toHaveBeenCalled();
  });

  it("labels timestamps as last run and contribution counts as last result", () => {
    const detection = task("detect_intro_markers");
    tasks = [detection, task("contribute_markers", { submitted: 2, skipped: 3, failed: 0 })];

    renderCard();

    expect(
      screen.getByText(`Last run: ${formatDateTime(detection.last_execution!.completed_at)}`),
    ).toBeInTheDocument();
    expect(screen.getByText("Last result: 2 submitted, 3 skipped, 0 failed")).toBeInTheDocument();
  });

  it("reports items the provider refused", () => {
    tasks = [task("contribute_markers", { submitted: 4, skipped: 10, invalid: 3, failed: 0 })];

    renderCard();

    expect(
      screen.getByText("Last result: 4 submitted, 10 skipped, 3 refused by the provider, 0 failed"),
    ).toBeInTheDocument();
  });

  it("describes a recorded rate limit without presenting it as a pending retry", () => {
    tasks = [
      task("contribute_markers", {
        submitted: 0,
        skipped: 1039,
        failed: 18,
        retry_after_seconds: 1,
      }),
    ];

    renderCard();

    expect(screen.getByText("Completed with errors")).toBeInTheDocument();
    expect(screen.getByText(/Last result: 0 submitted, 1039 skipped, 18 failed/)).toHaveTextContent(
      "Provider rate limit reached during this run.",
    );
    expect(screen.queryByText(/retry after/i)).not.toBeInTheDocument();
  });
});
