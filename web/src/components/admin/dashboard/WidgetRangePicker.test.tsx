import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { RangeSegmentedControl, WidgetRangePicker } from "./WidgetRangePicker";
import { WidgetChromeProvider } from "./widgetChrome";

describe("RangeSegmentedControl", () => {
  it("renders only the offered windows, in order", () => {
    render(
      <RangeSegmentedControl value="week" options={["day", "week", "month"]} onChange={() => {}} />,
    );

    expect(screen.getAllByRole("button").map((button) => button.textContent)).toEqual([
      "24h",
      "7d",
      "30d",
    ]);
    expect(screen.queryByText("1h")).toBeNull();
  });

  it("marks the current window pressed and the others not", () => {
    render(
      <RangeSegmentedControl value="week" options={["day", "week", "month"]} onChange={() => {}} />,
    );

    expect(screen.getByLabelText("Show the last 7 days").getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByLabelText("Show the last 24 hours").getAttribute("aria-pressed")).toBe(
      "false",
    );
  });

  it("reports the clicked window", () => {
    const onChange = vi.fn();
    render(
      <RangeSegmentedControl value="week" options={["day", "week", "month"]} onChange={onChange} />,
    );

    fireEvent.click(screen.getByLabelText("Show the last 30 days"));

    expect(onChange).toHaveBeenCalledWith("month");
  });

  it("renders nothing when there is no choice to make", () => {
    const { container } = render(
      <RangeSegmentedControl value="day" options={["day"]} onChange={() => {}} />,
    );

    expect(container.firstChild).toBeNull();
  });
});

describe("WidgetRangePicker", () => {
  it("takes its options from the registry and calls the grid's setter", () => {
    const setRange = vi.fn();
    render(
      <WidgetChromeProvider id="top-titles" range="week" setRange={setRange}>
        <WidgetRangePicker />
      </WidgetChromeProvider>,
    );

    expect(screen.getAllByRole("button")).toHaveLength(3);

    fireEvent.click(screen.getByLabelText("Show the last 24 hours"));

    expect(setRange).toHaveBeenCalledWith("top-titles", "day");
  });

  // Widgets are also rendered outside the grid (unit tests); the picker is the
  // grid's chrome and simply is not there.
  it("renders nothing outside a widget chrome", () => {
    const { container } = render(<WidgetRangePicker />);

    expect(container.firstChild).toBeNull();
  });

  it("renders nothing for a widget without windows", () => {
    const { container } = render(
      <WidgetChromeProvider id="libraries" range={undefined} setRange={() => {}}>
        <WidgetRangePicker />
      </WidgetChromeProvider>,
    );

    expect(container.firstChild).toBeNull();
  });
});
