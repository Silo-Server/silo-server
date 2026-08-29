import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import StarRating from "./StarRating";

describe("StarRating", () => {
  it("marks the saved rating as the resting fill state", () => {
    render(<StarRating value={2} onChange={() => {}} />);

    const stars = screen.getAllByRole("radio");

    // Hover previews are pure CSS (.star-rating-star[data-filled]) so pointer
    // movement never schedules React renders; the DOM only carries the stored
    // rating.
    expect(stars[1]).toHaveAttribute("data-filled", "true");
    expect(stars[2]).toHaveAttribute("data-filled", "false");
    for (const star of stars) {
      expect(star).toHaveClass("star-rating-star");
    }
  });

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

  it("layers the watched-profile average beneath the personal rating", () => {
    render(
      <StarRating value={2} communityAverage={3.5} communityVoteCount={4} onChange={() => {}} />,
    );

    const group = screen.getByRole("radiogroup", {
      name: "Rating. Server average 3.5 from 4 ratings.",
    });
    const stars = screen.getAllByRole("radio");

    expect(group).toBeInTheDocument();
    expect(stars[2]).toHaveAttribute("data-community-fill", "1.00");
    expect(stars[3]).toHaveAttribute("data-community-fill", "0.50");
    expect(stars[4]).toHaveAttribute("data-community-fill", "0.00");
    expect(stars[1]).toHaveAttribute("data-filled", "true");
    expect(stars[2]).toHaveAttribute("data-filled", "false");
  });
});
