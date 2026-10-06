import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import QuickStartTemplates from "./QuickStartTemplates";

describe("QuickStartTemplates", () => {
  it("invokes onPick with the template chips", async () => {
    const onPick = vi.fn();
    render(<QuickStartTemplates onPick={onPick} />);
    await userEvent.click(screen.getByRole("button", { name: /Highly rated/ }));
    expect(onPick).toHaveBeenCalled();
    const chips = onPick.mock.calls[0]![0];
    expect(chips.length).toBeGreaterThan(0);
    expect(chips[0].field).toBe("rating_imdb");
  });
});
