import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SpeedMenu } from "./SpeedMenu";

const RATES = [0.75, 1, 1.25, 1.5, 2] as const;

describe("SpeedMenu", () => {
  it("shows the current rate label on the trigger", () => {
    render(<SpeedMenu rates={RATES} value={1.5} onChange={() => {}} />);
    expect(screen.getByRole("button", { name: /playback speed/i })).toHaveTextContent("1.5×");
  });

  it("opens, lists all rates, and emits the chosen value", async () => {
    const onChange = vi.fn();
    render(<SpeedMenu rates={RATES} value={1} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: /playback speed/i }));
    expect(screen.getAllByRole("menuitem")).toHaveLength(RATES.length);
    await userEvent.click(screen.getByRole("menuitem", { name: "1.25×" }));
    expect(onChange).toHaveBeenCalledWith(1.25);
  });

  it("marks the current rate as active", async () => {
    render(<SpeedMenu rates={RATES} value={1.5} onChange={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: /playback speed/i }));
    const active = screen.getByRole("menuitem", { name: "1.5×" });
    expect(active).toHaveAttribute("data-active", "true");
  });
});
