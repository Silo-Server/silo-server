import { describe, expect, it } from "vitest";
import { pickNearest, type NavBox, type NavDirection } from "./spatialNavigation";

const box = (left: number, top: number, width = 100, height = 150): NavBox => ({
  left,
  top,
  right: left + width,
  bottom: top + height,
});

// Two rows of cards with the second row offset by half a card, a sidebar
// link on the left, and a wide button above the rows.
const sidebar = box(0, 200, 60, 40);
const header = box(100, 0, 400, 40);
const rowA = [box(100, 100), box(220, 100), box(340, 100)] as const;
const rowB = [box(150, 300), box(270, 300), box(390, 300)] as const;
const all = [sidebar, header, ...rowA, ...rowB];

describe("pickNearest", () => {
  it.each<[string, NavBox, NavDirection, NavBox | null]>([
    ["right along a row", rowA[0], "right", rowA[1]],
    ["left along a row", rowA[2], "left", rowA[1]],
    ["down to the overlapping card best lined up", rowA[1], "down", rowB[1]],
    ["right from the sidebar to the card level with it", sidebar, "right", rowA[0]],
    ["up to the header", rowA[1], "up", header],
    ["left out of the row to the sidebar", rowA[0], "left", sidebar],
    ["nothing past the last card", rowB[2], "right", null],
    ["not across the screen from a corner", header, "left", null],
    ["nothing below the last row", rowB[0], "down", null],
  ])("%s", (_name, from, direction, expected) => {
    const others = all.filter((b) => b !== from);
    const index = pickNearest(from, others, direction);
    expect(index === -1 ? null : others[index]).toBe(expected);
  });

  it("prefers the card beside it to a short sidebar link a hair nearer", () => {
    const card = { left: 267.5, top: 188, right: 452.5, bottom: 465.5 };
    const sidebarLink = { left: 24, top: 272, right: 248, bottom: 312 };
    const previousCard = { left: 62.5, top: 188, right: 247.5, bottom: 465.5 };
    expect(pickNearest(card, [sidebarLink, previousCard], "left")).toBe(1);
  });

  it("goes to the card in line above before a nearer logo off to the side", () => {
    const card = { left: 308, top: 96, right: 493, bottom: 374 };
    const logo = { left: 0, top: 0, right: 260, bottom: 96 };
    const cardAbove = { left: 308, top: -300, right: 493, bottom: -22 };
    expect(pickNearest(card, [logo, cardAbove], "up")).toBe(1);
  });

  it("reaches past the cone only when asked", () => {
    const next = box(800, 500, 60, 30);
    const picker = box(400, 420, 200, 30);
    expect(pickNearest(next, [picker], "up")).toBe(-1);
    expect(pickNearest(next, [picker], "up", false)).toBe(0);
  });

  it("ignores a box that overlaps more than a ring's width", () => {
    // A control drawn inside the card is not "to the right" of it.
    const inner = { left: 150, top: 200, right: 210, bottom: 240 };
    expect(pickNearest(rowA[0], [inner], "right")).toBe(-1);
  });
});
