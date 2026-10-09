import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ShowToOtherProfilesField } from "./ShowToOtherProfilesField";

describe("ShowToOtherProfilesField", () => {
  it("is one labelled switch that explains who sees the collection", () => {
    const onCheckedChange = vi.fn();
    render(<ShowToOtherProfilesField checked={false} onCheckedChange={onCheckedChange} />);
    const toggle = screen.getByRole("switch", { name: "Show to other profiles" });
    expect(
      screen.getByText(
        "Every profile on this account sees it, minus titles it can't access. Nobody else on the server can see it.",
      ),
    ).toBeTruthy();
    expect(screen.queryByText(/Allowed Profiles/)).toBeNull();
    fireEvent.click(toggle);
    expect(onCheckedChange).toHaveBeenCalledWith(true);
  });

  it("cannot be changed when disabled", () => {
    const onCheckedChange = vi.fn();
    render(<ShowToOtherProfilesField checked onCheckedChange={onCheckedChange} disabled />);
    const toggle = screen.getByRole("switch", { name: "Show to other profiles" });
    expect(toggle.getAttribute("aria-checked")).toBe("true");
    fireEvent.click(toggle);
    expect(onCheckedChange).not.toHaveBeenCalled();
  });
});
