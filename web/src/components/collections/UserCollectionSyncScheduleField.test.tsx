import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { userCollectionScheduleRequestValue } from "@/lib/userCollectionSyncSchedule";

import { UserCollectionSyncScheduleField } from "./UserCollectionSyncScheduleField";

if (!window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

describe("UserCollectionSyncScheduleField", () => {
  it("gives admins the server collection schedule presets", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <UserCollectionSyncScheduleField value="0 3 * * *" onChange={onChange} allowCustomCron />,
    );

    await user.click(screen.getByRole("combobox"));

    expect(screen.getByRole("option", { name: "Every hour" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Every 6 hours" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Daily at 3:00 AM" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Weekly (Monday 3:00 AM)" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Weekly (Sunday 3:00 AM)" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Monthly (1st at 3:00 AM)" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Custom cron expression" })).toBeInTheDocument();

    await user.click(screen.getByRole("option", { name: "Every 6 hours" }));
    expect(onChange).toHaveBeenCalledWith("0 */6 * * *");
  });

  it("keeps regular accounts on bounded cadence labels", () => {
    render(
      <UserCollectionSyncScheduleField
        value="0 */6 * * *"
        onChange={() => {}}
        allowCustomCron={false}
      />,
    );

    expect(screen.getByRole("combobox")).toHaveTextContent("Daily");
    expect(userCollectionScheduleRequestValue("0 */6 * * *", false)).toBe("daily");
  });

  it("maps bounded values to the matching server preset for an admin", () => {
    expect(userCollectionScheduleRequestValue("daily", true)).toBe("0 3 * * *");
    expect(userCollectionScheduleRequestValue("weekly", true)).toBe("0 3 * * 0");
    expect(userCollectionScheduleRequestValue("monthly", true)).toBe("0 3 1 * *");
  });
});
