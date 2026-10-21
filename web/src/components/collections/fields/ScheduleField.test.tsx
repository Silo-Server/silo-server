import { fireEvent, render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, it, vi } from "vitest";

import { ScheduleField } from "./ScheduleField";

beforeAll(() => {
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
});

function open(combobox: HTMLElement) {
  fireEvent.pointerDown(combobox, { button: 0, ctrlKey: false, pointerType: "mouse" });
}

function schedule() {
  return screen.getByRole("combobox", { name: "Sync schedule" });
}

describe("ScheduleField, server", () => {
  it("labels times with the server's offset, and its zone name only when reported", () => {
    const { rerender } = render(
      <ScheduleField
        scope="server"
        value=""
        onChange={vi.fn()}
        timeZone={{ utc_offset: "-05:00" }}
      />,
    );
    expect(screen.getByText("Server time (UTC−5)")).toBeInTheDocument();
    expect(schedule()).toHaveAccessibleDescription("Server time (UTC−5)");
    rerender(
      <ScheduleField
        scope="server"
        value=""
        onChange={vi.fn()}
        timeZone={{ utc_offset: "+05:30", name: "Asia/Kolkata" }}
      />,
    );
    expect(screen.getByText("Server time (UTC+5:30, Asia/Kolkata)")).toBeInTheDocument();
    rerender(<ScheduleField scope="server" value="" onChange={vi.fn()} />);
    expect(screen.getByText("Server time")).toBeInTheDocument();
  });

  it("names a template's schedule in words", () => {
    render(<ScheduleField scope="server" value="0 6 * * 1" onChange={vi.fn()} />);
    expect(schedule()).toHaveTextContent("Every Monday at 6:00 AM");
  });

  it('sends the chosen preset and "" for no automatic sync', () => {
    const onChange = vi.fn();
    render(<ScheduleField scope="server" value="0 3 * * *" onChange={onChange} />);
    open(schedule());
    fireEvent.click(screen.getByRole("option", { name: "Every 6 hours" }));
    expect(onChange).toHaveBeenLastCalledWith("0 */6 * * *");
    open(schedule());
    fireEvent.click(screen.getByRole("option", { name: "No automatic sync" }));
    expect(onChange).toHaveBeenLastCalledWith("");
  });

  it("takes a cron expression under Custom schedule", () => {
    const onChange = vi.fn();
    render(<ScheduleField scope="server" value="0 3 * * *" onChange={onChange} />);
    open(schedule());
    fireEvent.click(screen.getByRole("option", { name: "Custom schedule…" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Cron schedule" }), {
      target: { value: "15 2 * * *" },
    });
    expect(onChange).toHaveBeenLastCalledWith("15 2 * * *");
  });
});

describe("ScheduleField, personal", () => {
  it('offers the named schedules and sends "" for Manual only', () => {
    const onChange = vi.fn();
    render(<ScheduleField scope="personal" value="daily" onChange={onChange} />);
    expect(schedule()).toHaveTextContent("Daily");
    open(schedule());
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual([
      "Manual only",
      "Daily",
      "Weekly",
      "Monthly",
    ]);
    fireEvent.click(screen.getByRole("option", { name: "Manual only" }));
    expect(onChange).toHaveBeenLastCalledWith("");
  });

  it("keeps a custom schedule as the current choice until another is picked", () => {
    render(<ScheduleField scope="personal" value="custom" onChange={vi.fn()} />);
    expect(schedule()).toHaveTextContent("Custom schedule (current)");
  });
});
