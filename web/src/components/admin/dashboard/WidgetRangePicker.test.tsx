import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { RangeSegmentedControl, WidgetRangePicker } from "./WidgetRangePicker";
import { WidgetChromeProvider } from "./widgetChrome";

describe("RangeSegmentedControl", () => {
  it("marks the current window pressed and the others not", () => {
    render(
      <RangeSegmentedControl value="week" options={["day", "week", "month"]} onChange={() => {}} />,
    );

    expect(screen.getByLabelText("Show the last 7 days").getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByLabelText("Show the last 24 hours").getAttribute("aria-pressed")).toBe(
      "false",
    );
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

  it("renders nothing for a widget without windows", () => {
    const { container } = render(
      <WidgetChromeProvider id="libraries" range={undefined} setRange={() => {}}>
        <WidgetRangePicker />
      </WidgetChromeProvider>,
    );

    expect(container.firstChild).toBeNull();
  });
});
