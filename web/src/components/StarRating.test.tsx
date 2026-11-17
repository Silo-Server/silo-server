import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import StarRating from "./StarRating";

/** Lays the ten half-star radios out 10px apart from x = 0, as a browser would. */
function layOutHalves() {
  screen.getAllByRole("radio").forEach((half, i) => {
    half.getBoundingClientRect = () =>
      ({ left: i * 10, right: i * 10 + 10, top: 0, bottom: 20, width: 10, height: 20 }) as DOMRect;
  });
}

function filled() {
  return screen.getAllByRole("radio").filter((half) => half.getAttribute("data-filled") === "true")
    .length;
}

describe("StarRating", () => {
  it("rates in half stars: each star is two radios for scores 1 to 10", () => {
    const onChange = vi.fn();
    render(<StarRating value={7} onChange={onChange} />);

    const halves = screen.getAllByRole("radio");
    expect(halves).toHaveLength(10);
    expect(halves[6]).toHaveAccessibleName("3.5 stars");
    expect(halves[6]).toHaveAttribute("aria-checked", "true");
    expect(halves.map((half) => half.getAttribute("data-filled"))).toEqual([
      ...Array(7).fill("true"),
      ...Array(3).fill("false"),
    ]);

    // Keyboard activation (detail 0) sets the radio's own value, half stars too.
    fireEvent.click(halves[4]!, { detail: 0 });
    expect(onChange).toHaveBeenLastCalledWith(5);
  });

  it("fills the whole star on a click, whichever half it lands on", () => {
    const onChange = vi.fn();
    const { rerender } = render(<StarRating value={null} onChange={onChange} />);
    const halves = () => screen.getAllByRole("radio");

    fireEvent.click(halves()[6]!, { detail: 1 });
    expect(onChange).toHaveBeenLastCalledWith(8);
    fireEvent.click(halves()[0]!, { detail: 1 });
    expect(onChange).toHaveBeenLastCalledWith(2);

    // From three and a half stars, clicking the fourth star fills it.
    rerender(<StarRating value={7} onChange={onChange} />);
    fireEvent.click(halves()[6]!, { detail: 1 });
    expect(onChange).toHaveBeenLastCalledWith(8);

    // Clicking either half of the star that is set clears the rating.
    rerender(<StarRating value={8} onChange={onChange} />);
    fireEvent.click(halves()[6]!, { detail: 1 });
    expect(onChange).toHaveBeenLastCalledWith(null);
  });

  it("steps half a star per arrow key, from 1 to 10", () => {
    const onChange = vi.fn();
    const { rerender } = render(<StarRating value={7} onChange={onChange} />);
    const group = screen.getByRole("radiogroup");

    fireEvent.keyDown(group, { key: "ArrowRight" });
    expect(onChange).toHaveBeenLastCalledWith(8);
    // Quick presses build on the value just picked.
    fireEvent.keyDown(group, { key: "ArrowRight" });
    expect(onChange).toHaveBeenLastCalledWith(9);
    fireEvent.keyDown(group, { key: "ArrowLeft" });
    expect(onChange).toHaveBeenLastCalledWith(8);

    rerender(<StarRating value={10} onChange={onChange} />);
    fireEvent.keyDown(group, { key: "ArrowRight" });
    expect(onChange).toHaveBeenLastCalledWith(10);
    rerender(<StarRating value={null} onChange={onChange} />);
    fireEvent.keyDown(group, { key: "ArrowRight" });
    expect(onChange).toHaveBeenLastCalledWith(1);
  });

  it("holds the hover preview after a pointer click until the pointer leaves that star", () => {
    // Clicking the current star clears the rating with the pointer still on
    // it. The CSS hover preview would keep drawing the old fill, so the click
    // sets data-suppress-preview until the pointer moves off the star.
    const { rerender } = render(<StarRating value={8} onChange={vi.fn()} />);
    const group = screen.getByRole("radiogroup");
    const fourthStar = screen.getAllByRole("radio")[7]!;

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

  it("keeps holding the hover preview while the pointer crosses into the clicked star's other half", () => {
    // Each star is two radios, so a pointer that drifts over the middle of the
    // star it just cleared leaves one half for the other without leaving the
    // star. The preview must stay off until the pointer leaves the whole star.
    const { rerender } = render(<StarRating value={8} onChange={vi.fn()} />);
    const group = screen.getByRole("radiogroup");
    const halves = screen.getAllByRole("radio");

    fireEvent.click(halves[7]!, { detail: 1 });
    rerender(<StarRating value={null} onChange={vi.fn()} />);
    expect(group).toHaveAttribute("data-suppress-preview");

    fireEvent.pointerLeave(halves[7]!, { relatedTarget: halves[6]! });
    expect(group).toHaveAttribute("data-suppress-preview");

    fireEvent.pointerLeave(halves[6]!, { relatedTarget: halves[5]! });
    expect(group).not.toHaveAttribute("data-suppress-preview");
  });

  it("drags across the stars to preview a rating and sets it on release", () => {
    const onChange = vi.fn();
    render(<StarRating value={4} onChange={onChange} />);
    layOutHalves();
    const group = screen.getByRole("radiogroup");

    // A press fills the whole star under it; half stars need the pointer to move.
    fireEvent.pointerDown(group, { pointerId: 1, pointerType: "touch", clientX: 5 });
    expect(filled()).toBe(2);
    fireEvent.pointerMove(group, { pointerId: 1, pointerType: "touch", clientX: 64, clientY: 80 });
    expect(filled()).toBe(7);
    // Past the end is five stars, before the start half a star.
    fireEvent.pointerMove(group, { pointerId: 1, pointerType: "touch", clientX: 500 });
    expect(filled()).toBe(10);
    fireEvent.pointerMove(group, { pointerId: 1, pointerType: "touch", clientX: -40 });
    expect(filled()).toBe(1);
    fireEvent.pointerMove(group, { pointerId: 1, pointerType: "touch", clientX: 64 });
    expect(onChange).not.toHaveBeenCalled();

    fireEvent.pointerUp(group, { pointerId: 1, pointerType: "touch", clientX: 64 });
    // The click the release produces must not set the value a second time.
    fireEvent.click(screen.getAllByRole("radio")[6]!, { detail: 1 });
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(7);
  });

  it("treats a press and release without moving as a tap on the whole star", () => {
    const onChange = vi.fn();
    const { rerender } = render(<StarRating value={3} onChange={onChange} />);
    layOutHalves();
    const group = screen.getByRole("radiogroup");
    const tap = (clientX: number) => {
      fireEvent.pointerDown(group, { pointerId: 1, pointerType: "mouse", button: 0, clientX });
      fireEvent.pointerUp(group, { pointerId: 1, pointerType: "mouse", button: 0, clientX });
      fireEvent.click(screen.getAllByRole("radio")[Math.floor(clientX / 10)]!, { detail: 1 });
    };

    // The left half of the second star fills it.
    tap(25);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenLastCalledWith(4);

    // Either half of the star that is set clears it.
    rerender(<StarRating value={4} onChange={onChange} />);
    layOutHalves();
    tap(25);
    expect(onChange).toHaveBeenLastCalledWith(null);
    rerender(<StarRating value={null} onChange={onChange} />);
    rerender(<StarRating value={4} onChange={onChange} />);
    tap(35);
    expect(onChange).toHaveBeenLastCalledWith(null);
    expect(onChange).toHaveBeenCalledTimes(3);
  });

  it("keeps showing the picked value until the new rating arrives", () => {
    // The parent applies the change a moment later; until then the old value
    // must not flash back in.
    const onChange = vi.fn();
    const { rerender } = render(<StarRating value={4} onChange={onChange} />);
    layOutHalves();
    const group = screen.getByRole("radiogroup");

    fireEvent.click(screen.getAllByRole("radio")[4]!, { detail: 1 });
    expect(filled()).toBe(6);
    rerender(<StarRating value={6} onChange={onChange} />);
    expect(filled()).toBe(6);

    fireEvent.pointerDown(group, { pointerId: 1, pointerType: "mouse", button: 0, clientX: 55 });
    fireEvent.pointerMove(group, { pointerId: 1, pointerType: "mouse", button: 0, clientX: 84 });
    fireEvent.pointerUp(group, { pointerId: 1, pointerType: "mouse", button: 0, clientX: 84 });
    fireEvent.click(screen.getAllByRole("radio")[8]!, { detail: 1 });
    expect(filled()).toBe(9);

    // A failed write puts the old value back, and that is what shows.
    rerender(<StarRating value={9} onChange={onChange} />);
    rerender(<StarRating value={6} onChange={onChange} />);
    expect(filled()).toBe(6);

    // Clearing shows no stars straight away.
    fireEvent.click(screen.getAllByRole("radio")[5]!, { detail: 1 });
    expect(onChange).toHaveBeenLastCalledWith(null);
    expect(filled()).toBe(0);
  });

  it("treats a press that drifts a few pixels as a tap, not a drag", () => {
    // A finger lifting off the middle of a star moves a pixel or two, which
    // can cross into the other half; that must still fill the whole star.
    const onChange = vi.fn();
    render(<StarRating value={null} onChange={onChange} />);
    layOutHalves();
    const group = screen.getByRole("radiogroup");

    fireEvent.pointerDown(group, { pointerId: 1, pointerType: "touch", clientX: 31 });
    fireEvent.pointerMove(group, { pointerId: 1, pointerType: "touch", clientX: 28 });
    expect(filled()).toBe(4);
    fireEvent.pointerUp(group, { pointerId: 1, pointerType: "touch", clientX: 28 });
    expect(onChange).toHaveBeenLastCalledWith(4);
  });

  it("drops a cancelled drag without changing the rating", () => {
    const onChange = vi.fn();
    render(<StarRating value={4} onChange={onChange} />);
    layOutHalves();
    const group = screen.getByRole("radiogroup");

    fireEvent.pointerDown(group, { pointerId: 1, pointerType: "touch", clientX: 15 });
    fireEvent.pointerMove(group, { pointerId: 1, pointerType: "touch", clientX: 85 });
    // The browser takes the gesture over, for example to scroll the page.
    fireEvent.pointerCancel(group, { pointerId: 1, pointerType: "touch" });
    expect(onChange).not.toHaveBeenCalled();
    expect(filled()).toBe(4);
  });
});
