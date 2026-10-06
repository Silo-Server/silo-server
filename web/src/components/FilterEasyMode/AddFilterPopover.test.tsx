import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import AddFilterPopover from "./AddFilterPopover";

describe("AddFilterPopover", () => {
  it("does not submit when field is blank", async () => {
    const onAdd = vi.fn();
    render(<AddFilterPopover open onAdd={onAdd} onCancel={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: /add/i }));
    expect(onAdd).not.toHaveBeenCalled();
  });
});
