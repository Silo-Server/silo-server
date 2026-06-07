import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CircleButton } from "./CircleButton";

describe("CircleButton", () => {
  it("calls onClick when clicked", async () => {
    const onClick = vi.fn();
    render(
      <CircleButton size="sm" variant="secondary" ariaLabel="Test" onClick={onClick}>
        x
      </CircleButton>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("applies the primary skin class for variant=primary", () => {
    render(
      <CircleButton size="md" variant="primary" ariaLabel="Play">
        ▶
      </CircleButton>,
    );
    const btn = screen.getByRole("button", { name: "Play" });
    expect(btn.className).toContain("player-disc-primary");
  });

  it("sets data-paused when prop is true", () => {
    render(
      <CircleButton size="md" variant="primary" ariaLabel="Play" data-paused>
        ▶
      </CircleButton>,
    );
    expect(screen.getByRole("button")).toHaveAttribute("data-paused", "true");
  });
});
