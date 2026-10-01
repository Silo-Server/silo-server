import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import type { ExecutionResult } from "@/api/types";
import { TaskStatusBadge } from "./TaskStatusBadge";

describe("TaskStatusBadge", () => {
  it.each<{
    taskKey: string;
    status: ExecutionResult["status"];
    failed: number;
    label: string;
  }>([
    {
      taskKey: "contribute_markers",
      status: "completed",
      failed: 18,
      label: "Completed with errors",
    },
    { taskKey: "contribute_markers", status: "completed", failed: 0, label: "completed" },
    { taskKey: "contribute_markers", status: "failed", failed: 18, label: "failed" },
    { taskKey: "contribute_markers", status: "cancelled", failed: 18, label: "cancelled" },
    { taskKey: "other_task", status: "completed", failed: 18, label: "completed" },
  ])(
    "shows $label for $taskKey ($status, $failed failures)",
    ({ taskKey, status, failed, label }) => {
      render(
        <TaskStatusBadge
          result={{
            task_key: taskKey,
            status,
            result_data: { submitted: 0, skipped: 0, invalid: 0, failed, retry_after_seconds: 0 },
          }}
        />,
      );

      expect(screen.getByText(label)).toBeInTheDocument();
      if (label === "Completed with errors") {
        expect(screen.getByText(label)).toHaveAttribute("data-variant", "destructive");
      }
    },
  );

  it("keeps the failure reason available when the task itself failed", async () => {
    const user = userEvent.setup();
    render(
      <TaskStatusBadge
        result={{
          task_key: "contribute_markers",
          status: "failed",
          result_data: { submitted: 0, skipped: 0, invalid: 0, failed: 18, retry_after_seconds: 0 },
          error_message: "Could not load marker candidates",
        }}
      />,
    );

    await user.tab();

    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "Could not load marker candidates",
    );
    expect(screen.queryByText("Completed with errors")).not.toBeInTheDocument();
  });
});
