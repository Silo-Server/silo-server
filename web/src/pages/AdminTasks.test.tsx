import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { TaskInfo } from "@/api/types";

function task(overrides: Partial<TaskInfo>): TaskInfo {
  return {
    key: "task",
    name: "Task",
    description: "",
    category: "system",
    state: "idle",
    progress: 0,
    manual_only: false,
    execution_scope: "process",
    triggers: [],
    ...overrides,
  } as TaskInfo;
}

const mocks = vi.hoisted(() => ({ tasks: [] as TaskInfo[] }));

vi.mock("@/components/realtimeEventsContext", () => ({ useEventChannel: () => {} }));
vi.mock("@/hooks/queries/admin/tasks", () => ({
  useTasks: () => ({ data: mocks.tasks, isLoading: false }),
  useRunTask: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useCancelTask: () => ({ mutate: vi.fn(), isPending: false }),
  useTaskMetrics: () => ({ data: undefined }),
}));

import AdminTasks from "./AdminTasks";

describe("AdminTasks", () => {
  it("lists manual-only tools apart from scheduled tasks", () => {
    mocks.tasks = [
      task({
        key: "database_maintenance",
        name: "Database Maintenance",
        triggers: [{ type: "interval", interval_ms: 86_400_000 }],
      }),
      task({
        key: "reconcile_artwork_cache",
        name: "Reconcile Artwork Cache",
        category: "metadata",
        manual_only: true,
      }),
    ];

    render(
      <MemoryRouter>
        <AdminTasks />
      </MemoryRouter>,
    );

    const system = screen.getByRole("heading", { name: "System" }).closest("div.space-y-3");
    const onDemand = screen.getByRole("heading", { name: "On demand" }).closest("div.space-y-3");
    expect(system).not.toBeNull();
    expect(onDemand).not.toBeNull();
    expect(within(system as HTMLElement).getByText("Database Maintenance")).toBeInTheDocument();
    expect(
      within(onDemand as HTMLElement).getByText("Reconcile Artwork Cache"),
    ).toBeInTheDocument();
    expect(within(onDemand as HTMLElement).getByText("Manual only")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Metadata" })).not.toBeInTheDocument();
  });
});
