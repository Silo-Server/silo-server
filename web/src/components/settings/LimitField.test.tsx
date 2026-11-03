import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { LimitField, type LimitFieldProps } from "@/components/settings/LimitField";

function Harness({
  initial,
  onChange,
  label = "Per-user bandwidth",
  unit = "Mbps",
  ...field
}: {
  initial: string;
  onChange?: (value: string) => void;
} & Partial<Pick<LimitFieldProps, "label" | "unit" | "unlimitedValue" | "scale">>) {
  const [value, setValue] = useState(initial);
  return (
    <LimitField
      label={label}
      unit={unit}
      {...field}
      value={value}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

describe("LimitField", () => {
  it("restores the previous limit when Unlimited is unchecked", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial="50" onChange={onChange} />);

    const checkbox = screen.getByRole("checkbox", { name: "Unlimited" });
    expect(checkbox).not.toBeChecked();
    await user.click(checkbox);
    expect(onChange).toHaveBeenLastCalledWith("0");
    expect(screen.getByLabelText("Per-user bandwidth")).toBeDisabled();
    await user.click(checkbox);

    expect(onChange).toHaveBeenLastCalledWith("50");
    expect(screen.getByLabelText("Per-user bandwidth")).toHaveValue(50);
  });

  it("falls back to an empty limit when unlimited was the saved value", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial="0" onChange={onChange} />);

    expect(screen.getByRole("checkbox", { name: "Unlimited" })).toBeChecked();
    const input = screen.getByLabelText("Per-user bandwidth");
    expect(input).toBeDisabled();
    expect(input).toHaveValue(null);
    await user.click(screen.getByRole("checkbox", { name: "Unlimited" }));
    expect(onChange).toHaveBeenLastCalledWith("");
    expect(screen.getByLabelText("Per-user bandwidth")).toBeEnabled();
  });

  it("supports a non-zero unlimited sentinel", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial="10" unlimitedValue="-1" onChange={onChange} />);

    expect(screen.getByRole("checkbox", { name: "Unlimited" })).not.toBeChecked();
    await user.click(screen.getByRole("checkbox", { name: "Unlimited" }));
    expect(onChange).toHaveBeenLastCalledWith("-1");
    expect(screen.getByRole("checkbox", { name: "Unlimited" })).toBeChecked();
  });
});

const scaled = { label: "Prepared file storage budget", unit: "GB", scale: 1_000_000_000 };

describe("LimitField scaled units", () => {
  it("shows a stored byte value in the display unit", () => {
    render(<Harness {...scaled} initial="53687091200" />);

    expect(screen.getByLabelText("Prepared file storage budget")).toHaveValue(53.687);
  });

  it.each([
    ["stores what was typed in the underlying unit", "50", "50000000000"],
    [
      "keeps a fractional entry typeable instead of rewriting it mid-keystroke",
      "1.5",
      "1500000000",
    ],
  ])("%s", async (_, typed, stored) => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness {...scaled} initial="" onChange={onChange} />);

    await user.type(screen.getByLabelText("Prepared file storage budget"), typed);
    expect(onChange).toHaveBeenLastCalledWith(stored);
  });

  it("writes the unscaled sentinel for unlimited", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness {...scaled} initial="50000000000" onChange={onChange} />);

    await user.click(screen.getByRole("checkbox", { name: "Unlimited" }));
    expect(onChange).toHaveBeenLastCalledWith("0");
    expect(screen.getByLabelText("Prepared file storage budget")).toHaveValue(null);
  });
});
