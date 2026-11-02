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
});
