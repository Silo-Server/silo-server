import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import DetailOverview from "./DetailOverview";

describe("DetailOverview", () => {
  afterEach(() => vi.restoreAllMocks());

  function mockOverflow(overflowing: boolean) {
    vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(overflowing ? 200 : 84);
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(84);
  }

  it("offers More when the clamp hides text and expands on click", () => {
    mockOverflow(true);
    render(<DetailOverview overview="A long overview" clamp />);
    const text = screen.getByText("A long overview");
    expect(text).toHaveClass("line-clamp-3");
    const toggle = screen.getByRole("button", { name: "More" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(toggle);
    expect(text).not.toHaveClass("line-clamp-3");
    expect(screen.getByRole("button", { name: "Less" })).toHaveAttribute("aria-expanded", "true");
  });

  it("hides the toggle when the whole overview fits", () => {
    mockOverflow(false);
    render(<DetailOverview overview="Short" clamp />);
    expect(screen.queryByRole("button", { name: "More" })).not.toBeInTheDocument();
  });

  it("does not clamp unbounded heroes", () => {
    mockOverflow(true);
    render(<DetailOverview overview="Full text" />);
    expect(screen.getByText("Full text")).not.toHaveClass("line-clamp-3");
    expect(screen.queryByRole("button", { name: "More" })).not.toBeInTheDocument();
  });
});
