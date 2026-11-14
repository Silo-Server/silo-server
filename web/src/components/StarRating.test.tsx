import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import StarRating from "./StarRating";

describe("StarRating", () => {
  it("preserves rating selection behavior", () => {
    const onChange = vi.fn();
    render(<StarRating value={3} onChange={onChange} />);

    const stars = screen.getAllByRole("radio");
    const fourthStar = stars[3]!;

    expect(fourthStar).toHaveClass("cursor-pointer");

    fireEvent.click(fourthStar);
    expect(onChange).toHaveBeenCalledWith(4);

    fireEvent.click(stars[2]!);
    expect(onChange).toHaveBeenLastCalledWith(null);
  });

  it.each([
    ["rates up with Right", 3, "ArrowRight", 4],
    ["rates down with Left", 3, "ArrowLeft", 2],
    ["rates the first star from unrated", null, "ArrowRight", 1],
    ["leaves Right past five stars", 5, "ArrowRight", undefined],
    ["leaves Left past one star", 1, "ArrowLeft", undefined],
    ["leaves Left when unrated", null, "ArrowLeft", undefined],
    ["leaves Up to the surroundings", 3, "ArrowUp", undefined],
    ["leaves Down to the surroundings", 3, "ArrowDown", undefined],
  ])("%s", (_name, value, key, expected) => {
    const onChange = vi.fn();
    render(<StarRating value={value} onChange={onChange} />);
    const claimed = !fireEvent.keyDown(screen.getByRole("radiogroup"), { key });
    if (expected === undefined) {
      expect(onChange).not.toHaveBeenCalled();
      expect(claimed).toBe(false);
    } else {
      expect(onChange).toHaveBeenCalledWith(expected);
      expect(claimed).toBe(true);
    }
  });

  it("holds the hover preview after a pointer click until the pointer leaves that star", () => {
    // Clicking the current star clears the rating with the pointer still on
    // it. The CSS hover preview would keep drawing the old fill, so the click
    // sets data-suppress-preview until the pointer moves off the star.
    const { rerender } = render(<StarRating value={4} onChange={vi.fn()} />);
    const group = screen.getByRole("radiogroup");
    const fourthStar = screen.getAllByRole("radio")[3]!;

    // Keyboard activation (detail 0) has no pointer to leave, so no flag.
    fireEvent.click(fourthStar, { detail: 0 });
    expect(group).not.toHaveAttribute("data-suppress-preview");

    fireEvent.click(fourthStar, { detail: 1 });
    rerender(<StarRating value={null} onChange={vi.fn()} />);
    expect(group).toHaveAttribute("data-suppress-preview");

    fireEvent.pointerLeave(fourthStar);
    expect(group).not.toHaveAttribute("data-suppress-preview");

    // A touch press clears data-fine-pointer before the click, and the tap's
    // pointerleave has already fired, so a flag set now would never clear.
    document.documentElement.setAttribute("data-fine-pointer", "false");
    try {
      fireEvent.click(fourthStar, { detail: 1 });
      expect(group).not.toHaveAttribute("data-suppress-preview");
    } finally {
      document.documentElement.removeAttribute("data-fine-pointer");
    }
  });
});
