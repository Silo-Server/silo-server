import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { LimitField } from "@/components/settings/LimitField";

function Harness({
  initial,
  onChange,
  unlimitedValue,
}: {
  initial: string;
  onChange?: (value: string) => void;
  unlimitedValue?: string;
}) {
  const [value, setValue] = useState(initial);
  return (
    <LimitField
      label="Per-user bandwidth"
      value={value}
      unlimitedValue={unlimitedValue}
      unit="Mbps"
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

describe("LimitField", () => {
  it("reads the sentinel as unlimited and hides it from the input", () => {
    render(<Harness initial="0" />);

    expect(screen.getByRole("checkbox", { name: "Unlimited" })).toBeChecked();
    const input = screen.getByLabelText("Per-user bandwidth");
    expect(input).toBeDisabled();
    expect(input).toHaveValue(null);
  });

  it("writes the sentinel when Unlimited is checked", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial="50" onChange={onChange} />);

    const checkbox = screen.getByRole("checkbox", { name: "Unlimited" });
    expect(checkbox).not.toBeChecked();

    await user.click(checkbox);
    expect(onChange).toHaveBeenLastCalledWith("0");
    expect(screen.getByLabelText("Per-user bandwidth")).toBeDisabled();
  });

  it("restores the previous limit when Unlimited is unchecked", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial="50" onChange={onChange} />);

    const checkbox = screen.getByRole("checkbox", { name: "Unlimited" });
    await user.click(checkbox);
    await user.click(checkbox);

    expect(onChange).toHaveBeenLastCalledWith("50");
    expect(screen.getByLabelText("Per-user bandwidth")).toHaveValue(50);
  });

  it("falls back to an empty limit when unlimited was the saved value", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial="0" onChange={onChange} />);

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

  it("passes typed limits straight through", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial="" onChange={onChange} />);

    await user.type(screen.getByLabelText("Per-user bandwidth"), "25");
    expect(onChange).toHaveBeenLastCalledWith("25");
  });
});

function ScaledHarness({ initial, onChange }: { initial: string; onChange?: (v: string) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <LimitField
      label="Prepared file storage budget"
      value={value}
      unit="GB"
      scale={1_000_000_000}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

describe("LimitField scaled units", () => {
  it("shows a stored byte value in the display unit", () => {
    render(<ScaledHarness initial="53687091200" />);

    expect(screen.getByLabelText("Prepared file storage budget")).toHaveValue(53.687);
  });

  it("stores what was typed in the underlying unit", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<ScaledHarness initial="" onChange={onChange} />);

    await user.type(screen.getByLabelText("Prepared file storage budget"), "50");
    expect(onChange).toHaveBeenLastCalledWith("50000000000");
  });

  it("keeps a fractional entry typeable instead of rewriting it mid-keystroke", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<ScaledHarness initial="" onChange={onChange} />);

    await user.type(screen.getByLabelText("Prepared file storage budget"), "1.5");
    expect(onChange).toHaveBeenLastCalledWith("1500000000");
  });

  it("writes the unscaled sentinel for unlimited", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<ScaledHarness initial="50000000000" onChange={onChange} />);

    await user.click(screen.getByRole("checkbox", { name: "Unlimited" }));
    expect(onChange).toHaveBeenLastCalledWith("0");
    expect(screen.getByLabelText("Prepared file storage budget")).toHaveValue(null);
  });
});
