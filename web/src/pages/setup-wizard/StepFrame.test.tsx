import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { StepFrame } from "./StepFrame";

describe("StepFrame", () => {
  it("locks the step's fields while Continue is saving", () => {
    const step = (busy: boolean) => (
      <StepFrame title="Server" lede="" busy={busy} onSubmit={(e) => e.preventDefault()}>
        <input aria-label="Server name" />
        <button type="button" role="switch" aria-checked="false" aria-label="Remote access" />
      </StepFrame>
    );
    const { rerender } = render(step(true));

    // An edit made now would miss the batch being saved and the step would
    // advance past it, so nothing in the step takes input until it settles.
    expect(screen.getByLabelText("Server name")).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Remote access" })).toBeDisabled();

    rerender(step(false));
    expect(screen.getByLabelText("Server name")).toBeEnabled();
    expect(screen.getByRole("switch", { name: "Remote access" })).toBeEnabled();
  });
});
