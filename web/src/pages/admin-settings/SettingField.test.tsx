import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { SettingField } from "./SettingField";

describe("SettingField", () => {
  it("keeps describing the control with its description", () => {
    render(
      <SettingField
        label="Mark watched at"
        type="number"
        value="90"
        onChange={vi.fn()}
        hint="Percent of runtime before Silo marks an item finished."
      />,
    );

    const input = screen.getByLabelText("Mark watched at");
    const describedBy = input.getAttribute("aria-describedby");
    expect(describedBy).toBeTruthy();
    expect(document.getElementById(describedBy!)).toHaveTextContent(
      "Percent of runtime before Silo marks an item finished.",
    );
  });
});
