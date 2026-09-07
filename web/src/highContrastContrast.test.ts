// @vitest-environment node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * Computed WCAG contrast for the tokens high contrast mode rewrites, per theme.
 *
 * Asserting that the declarations merely exist is not enough: the block used to
 * push every token toward white, which raises contrast on a dark theme and
 * destroys it on a light one. On cinema-light that drove `--muted-foreground`
 * to 1.36:1 and `--foreground` to 1.10:1 against the page — high contrast mode
 * made the theme unreadable, and nothing in the suite noticed. These tests do
 * the colour maths so a future theme, or a changed mix percentage, cannot
 * reintroduce that silently.
 */
const css = readFileSync(fileURLToPath(new URL("./app.css", import.meta.url)), "utf8");

const THEMES = [
  "midnight-cinema",
  "cinema-light",
  "cobalt-studio",
  "oxblood-noir",
  "evergreen-studio",
] as const;

/** WCAG 2.1 AA for normal-size body text. */
const AA_TEXT = 4.5;

function themeBlock(theme: string): string {
  const match = new RegExp(String.raw`\[data-theme="${theme}"\] \{([\s\S]*?)\n {2}\}`).exec(css);
  const body = match?.[1];
  if (body === undefined) throw new Error(`no block for theme ${theme}`);
  return body;
}

function token(body: string, name: string): string {
  const match = new RegExp(String.raw`^\s*--${name}:\s*([^;]+);`, "m").exec(body);
  const value = match?.[1];
  if (value === undefined) throw new Error(`no --${name}`);
  return value.trim();
}

function rgb(hex: string): [number, number, number] {
  const h = hex.replace("#", "");
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
}

function luminance(hex: string): number {
  const channel = (c: number) => {
    const v = c / 255;
    return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  const [r, g, b] = rgb(hex);
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

function contrast(a: string, b: string): number {
  const la = luminance(a);
  const lb = luminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

/** srgb color-mix of `pct`% `toward` into `base`, matching the CSS. */
function mix(pct: number, base: string, toward: string): string {
  const [br, bg, bb] = rgb(base);
  const [tr, tg, tb] = rgb(toward);
  const p = pct / 100;
  const channel = (t: number, b: number) =>
    Math.round(t * p + b * (1 - p))
      .toString(16)
      .padStart(2, "0");
  return `#${channel(tr, br)}${channel(tg, bg)}${channel(tb, bb)}`;
}

describe.each(THEMES)("high contrast on %s", (theme) => {
  const body = themeBlock(theme);
  const background = token(body, "background");
  const boost = token(body, "contrast-boost");

  it("pushes away from the page, not toward white regardless of theme", () => {
    // The whole bug in one assertion: the boost must contrast with the page.
    expect(contrast(boost, background)).toBeGreaterThan(AA_TEXT);
  });

  it("keeps body text readable", () => {
    expect(contrast(boost, background)).toBeGreaterThanOrEqual(AA_TEXT);
  });

  it("does not make muted text worse than it already was", () => {
    const base = token(body, "muted-foreground-base");
    const normal = contrast(base, background);
    const boosted = contrast(mix(72, base, boost), background);
    expect(boosted).toBeGreaterThanOrEqual(normal);
    expect(boosted).toBeGreaterThanOrEqual(AA_TEXT);
  });

  it("does not make borders less visible than they already were", () => {
    const base = token(body, "border-base");
    expect(contrast(mix(34, base, boost), background)).toBeGreaterThanOrEqual(
      contrast(base, background),
    );
  });
});
