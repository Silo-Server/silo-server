// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import LetterBar from "./LetterBar";
import { letterAtIndex } from "./letterIndex";

const letters = [
  { letter: "#", count: 2, position: 0 },
  { letter: "A", count: 5, position: 2 },
  { letter: "C", count: 1, position: 7 },
  { letter: "Z", count: 3, position: 8 },
];

afterEach(cleanup);

describe("letterAtIndex", () => {
  it("names the letter a list position falls in", () => {
    expect(letterAtIndex(letters, 0)).toBe("#");
    expect(letterAtIndex(letters, 6)).toBe("A");
    expect(letterAtIndex(letters, 7)).toBe("C");
    expect(letterAtIndex(letters, 500)).toBe("Z");
    expect(letterAtIndex([], 3)).toBeUndefined();
  });

  it("follows list order when titles run Z to A", () => {
    const descending = [
      { letter: "Z", position: 0 },
      { letter: "A", position: 3 },
    ];
    expect(letterAtIndex(descending, 2)).toBe("Z");
    expect(letterAtIndex(descending, 3)).toBe("A");
  });

  it("names a letter whose titles the collation sorts apart", () => {
    // Under C, "Élite" sorts after "Zebra" but files under E, and non-Latin
    // titles come after every Latin letter.
    const runs = [
      { letter: "E", position: 0 },
      { letter: "Z", position: 1 },
      { letter: "E", position: 2 },
      { letter: "#", position: 3 },
    ];
    expect(letterAtIndex(runs, 1)).toBe("Z");
    expect(letterAtIndex(runs, 2)).toBe("E");
    expect(letterAtIndex(runs, 9)).toBe("#");
  });
});

describe("LetterBar", () => {
  it("lists # and A to Z, greying out letters without titles", () => {
    render(<LetterBar letters={letters} activeLetter="A" onJump={() => {}} />);

    const bar = screen.getByRole("toolbar", { name: "Jump to letter" });
    expect(bar.querySelectorAll("button")).toHaveLength(27);
    expect(screen.getByRole("button", { name: "Numbers and symbols, 2 titles" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "C, 1 title" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "B, no titles" }).getAttribute("aria-disabled")).toBe(
      "true",
    );
    expect(screen.getByRole("button", { name: "A, 5 titles" }).getAttribute("aria-current")).toBe(
      "true",
    );
  });

  it("jumps to a letter's first title and announces it", () => {
    const onJump = vi.fn();
    render(<LetterBar letters={letters} onJump={onJump} />);

    fireEvent.click(screen.getByRole("button", { name: "C, 1 title" }));
    expect(onJump).toHaveBeenCalledWith(letters[2]);
    expect(screen.getByRole("status").textContent).toBe("Showing titles under C");

    fireEvent.click(screen.getByRole("button", { name: "B, no titles" }));
    expect(onJump).toHaveBeenCalledTimes(1);
  });

  it("is one tab stop that arrow keys, Home, End, and typed letters move through", () => {
    render(<LetterBar letters={letters} activeLetter="A" onJump={() => {}} />);
    const button = (name: string) => screen.getByRole("button", { name: new RegExp(`^${name},`) });
    const tabbable = () =>
      screen.getAllByRole("button").filter((element) => element.tabIndex === 0);

    expect(tabbable()).toEqual([button("A")]);
    button("A").focus();

    fireEvent.keyDown(button("A"), { key: "ArrowDown" });
    expect(document.activeElement).toBe(button("C"));
    fireEvent.keyDown(button("C"), { key: "ArrowUp" });
    expect(document.activeElement).toBe(button("A"));
    fireEvent.keyDown(button("A"), { key: "End" });
    expect(document.activeElement).toBe(button("Z"));
    fireEvent.keyDown(button("Z"), { key: "Home" });
    expect(document.activeElement).toBe(button("Numbers and symbols"));
    fireEvent.keyDown(document.activeElement!, { key: "c" });
    expect(document.activeElement).toBe(button("C"));
    fireEvent.keyDown(button("C"), { key: "b" });
    expect(document.activeElement).toBe(button("C"));
    expect(tabbable()).toEqual([button("C")]);
  });

  it("leaves Space to activate the focused letter", () => {
    render(<LetterBar letters={letters} activeLetter="A" onJump={() => {}} />);
    const a = screen.getByRole("button", { name: "A, 5 titles" });
    a.focus();

    const event = new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true });
    a.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(false);
    expect(document.activeElement).toBe(a);
  });

  it("holds its place, greyed and inert, while the index loads", () => {
    const onJump = vi.fn();
    render(<LetterBar letters={undefined} onJump={onJump} />);

    const bar = screen.getByRole("toolbar", { name: "Jump to letter" });
    expect(bar.getAttribute("aria-busy")).toBe("true");
    const buttons = screen.getAllByRole("button");
    expect(buttons).toHaveLength(27);
    expect(buttons.every((b) => b.getAttribute("aria-disabled") === "true")).toBe(true);
    expect(buttons.every((b) => b.tabIndex === -1)).toBe(true);

    const q = screen.getByRole("button", { name: "Q" });
    fireEvent.click(q);
    fireEvent.keyDown(q, { key: "ArrowDown" });
    expect(onJump).not.toHaveBeenCalled();
  });
});
