import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { TaskInfo } from "@/api/types";

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
vi.mock("@/components/realtimeEventsContext", () => ({
  useRealtimeEvents: () => ({ connectionState: "live" }),
}));

import ServerActivity from "./ServerActivity";

describe("ServerActivity task progress", () => {
  it("shows an indeterminate running state instead of a misleading zero percent", () => {
    render(
      <MemoryRouter>
        <ServerActivity />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Server activity: 1 active" }));

    expect(screen.getByText("Running")).toBeInTheDocument();
    expect(screen.queryByText("0%")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Cache Metadata Images" })).toHaveAttribute(
      "href",
      "/admin/tasks/cache_metadata_images",
    );
  });
});
