import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { StepFrame } from "./StepFrame";

function step(busy: boolean) {
  return (
    <StepFrame title="Server" lede="" busy={busy} onSubmit={(e) => e.preventDefault()}>
      <input aria-label="Server name" />
      <button type="button" role="switch" aria-checked="false" aria-label="Remote access" />
    </StepFrame>
  );
}

describe("StepFrame", () => {
  it("locks the step's fields while Continue is saving", () => {
    const { rerender } = render(step(true));

    // An edit made now would miss the batch being saved and the step would
    // advance past it, so nothing in the step takes input until it settles.
    const name = screen.getByLabelText("Server name");
    expect(name).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Remote access" })).toBeDisabled();
    // A Radix select opens on pointerdown even inside a disabled fieldset.
    expect(name.closest("fieldset")).toHaveClass("pointer-events-none");

    rerender(step(false));
    expect(name).toBeEnabled();
    expect(screen.getByRole("switch", { name: "Remote access" })).toBeEnabled();
    expect(name.closest("fieldset")).not.toHaveClass("pointer-events-none");
  });

  it("returns focus to the field Enter was pressed in when the save fails", () => {
    const { rerender } = render(step(false));
    const name = screen.getByLabelText("Server name");
    name.focus();
    fireEvent.submit(name.closest("form")!);

    rerender(step(true));
    // Browsers drop focus from a control that becomes disabled.
    name.blur();
    rerender(step(false));

    expect(name).toHaveFocus();
  });
});
