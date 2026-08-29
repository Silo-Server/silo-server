// @vitest-environment node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * Directional page motion lives entirely in CSS — the app only writes
 * `html[data-navigation-direction]` — so these are contract tests over app.css,
 * the same shape as `sidebarCollapseCss.test.ts`.
 *
 * What matters: the root is never captured (its snapshot would freeze the
 * sidebar mid-collapse), the directional keyframes run on the sidebar's clock,
 * and reduced motion actually reaches them despite their higher specificity.
 */
const css = readFileSync(fileURLToPath(new URL("./app.css", import.meta.url)), "utf8");

/** Body of the first `@media (prefers-reduced-motion: reduce)` block. */
function reducedMotionBlock(): string {
  const start = css.indexOf("@media (prefers-reduced-motion: reduce)");
  expect(start).toBeGreaterThan(-1);
  let depth = 0;
  for (let i = css.indexOf("{", start); i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}" && --depth === 0) return css.slice(start, i + 1);
  }
  throw new Error("unterminated prefers-reduced-motion block");
}

describe("navigation view-transition CSS", () => {
  it("holds the root group still inside the sidebar shell, and only there", () => {
    // The UA stylesheet names the root by default; unanimated, the old page's
    // 260px sidebar would cross-fade on top of the rail as it travels to 64px.
    expect(css).toMatch(
      /html\[data-app-shell\]::view-transition-old\(root\),\s*html\[data-app-shell\]::view-transition-new\(root\) \{\s*animation: none;/,
    );
    // Scoped, not global. `main-content` is named only inside Layout, so
    // un-naming the root outright would leave /watch, /reader/*, /admin/* and
    // the auth screens with no captured element at all.
    expect(css).not.toMatch(/:root \{\s*view-transition-name: none;/);
  });

  it("moves main-content's box on the sidebar's clock, not the UA's 250ms", () => {
    expect(css).toMatch(
      /::view-transition-group\(main-content\) \{\s*animation-duration: var\(--duration-sidebar-collapse\);\s*animation-timing-function: var\(--ease-sidebar-collapse\);/,
    );
  });

  it("mirrors the two directions off one shift token", () => {
    expect(css).toMatch(
      /html\[data-navigation-direction="forward"\] \{\s*--nav-slide-shift: -24px;/,
    );
    expect(css).toMatch(/html\[data-navigation-direction="back"\] \{\s*--nav-slide-shift: 24px;/);
    // The incoming page enters from the side the outgoing page left towards.
    expect(css).toContain("transform: translateX(var(--nav-slide-shift))");
    expect(css).toContain("transform: translateX(calc(-1 * var(--nav-slide-shift)))");
  });

  it("runs both halves of the cross-fade on identical timing", () => {
    // The pseudo-elements default to `mix-blend-mode: plus-lighter`, which only
    // cross-fades cleanly while the two opacities sum to 1. Different easing
    // curves would break that sum and flash bright mid-transition.
    const timing = "var(--duration-sidebar-collapse) var(--ease-sidebar-collapse) both";
    expect(css).toContain(
      `html[data-navigation-direction]::view-transition-old(main-content) {\n  animation: ${timing} nav-slide-out;`,
    );
    expect(css).toContain(
      `html[data-navigation-direction]::view-transition-new(main-content) {\n  animation: ${timing} nav-slide-in;`,
    );
  });

  it("attaches the pseudo-element to the root rather than descending from it", () => {
    // `html[...] ::view-transition-old(...)` — with a combinator — parses fine
    // and silently never matches, so the whole feature would go quiet.
    expect(css).not.toMatch(/html\[data-navigation-direction[^\]]*\]\s+::view-transition/);
  });

  it("suppresses the directional rules under reduced motion despite their specificity", () => {
    const block = reducedMotionBlock();
    for (const selector of [
      "::view-transition-old(main-content),",
      "::view-transition-new(main-content),",
      "::view-transition-group(main-content),",
      "html[data-navigation-direction]::view-transition-old(main-content),",
      "html[data-navigation-direction]::view-transition-new(main-content)",
    ]) {
      expect(block).toContain(selector);
    }
  });
});
