import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import StarRating from "./StarRating";

describe("StarRating", () => {
  it("previews the hovered rating and restores the saved rating on leave", () => {
    render(<StarRating value={2} onChange={() => {}} />);

    const group = screen.getByRole("radiogroup", { name: "Rating" });
    const stars = screen.getAllByRole("radio");

    expect(stars[1]?.querySelector("svg")).toHaveAttribute("fill-opacity", "1");
    expect(stars[2]?.querySelector("svg")).toHaveAttribute("fill-opacity", "0");

    fireEvent.mouseEnter(stars[4]!);

    for (const star of stars) {
      expect(star.querySelector("svg")).toHaveAttribute("fill-opacity", "1");
    }

    fireEvent.mouseLeave(group);

    expect(stars[1]?.querySelector("svg")).toHaveAttribute("fill-opacity", "1");
    expect(stars[2]?.querySelector("svg")).toHaveAttribute("fill-opacity", "0");
  });

  it("uses focused transitions and preserves rating selection behavior", () => {
    const onChange = vi.fn();
    render(<StarRating value={3} onChange={onChange} />);

    const stars = screen.getAllByRole("radio");
    const fourthStar = stars[3]!;

    expect(fourthStar).toHaveClass(
      "cursor-pointer",
      "transform-gpu",
      "transition-[color,scale]",
      "duration-100",
      "motion-safe:hover:scale-110",
      "motion-reduce:transition-none",
    );
    expect(fourthStar).not.toHaveClass("transition-all");
    expect(fourthStar.querySelector("svg")).toHaveClass(
      "transition-[fill-opacity]",
      "motion-reduce:transition-none",
    );

    fireEvent.click(fourthStar);
    expect(onChange).toHaveBeenCalledWith(4);

    fireEvent.click(stars[2]!);
    expect(onChange).toHaveBeenLastCalledWith(null);
  });
});
