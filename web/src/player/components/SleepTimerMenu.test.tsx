import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SleepTimerMenu, type SleepSetting } from "./SleepTimerMenu";

describe("SleepTimerMenu", () => {
  it("shows 'Sleep' when off and the countdown when armed", () => {
    const { rerender } = render(
      <SleepTimerMenu setting={{ kind: "off" }} remainingMs={null} onChange={() => {}} />,
    );
    expect(screen.getByRole("button", { name: /sleep timer/i })).toHaveTextContent("Sleep");
    rerender(
      <SleepTimerMenu
        setting={{ kind: "duration", seconds: 300 }}
        remainingMs={272_000}
        onChange={() => {}}
      />,
    );
    expect(screen.getByRole("button", { name: /sleep timer/i })).toHaveTextContent("Sleep 4:32");
  });

  it("emits a duration setting when a preset is chosen", async () => {
    const onChange = vi.fn();
    render(<SleepTimerMenu setting={{ kind: "off" }} remainingMs={null} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: /sleep timer/i }));
    await userEvent.click(screen.getByRole("menuitem", { name: "15 min" }));
    expect(onChange).toHaveBeenCalledWith({ kind: "duration", seconds: 900 } as SleepSetting);
  });

  it("emits end-of-chapter when that option is chosen", async () => {
    const onChange = vi.fn();
    render(<SleepTimerMenu setting={{ kind: "off" }} remainingMs={null} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: /sleep timer/i }));
    await userEvent.click(screen.getByRole("menuitem", { name: /end of chapter/i }));
    expect(onChange).toHaveBeenCalledWith({ kind: "end-of-chapter" });
  });
});
