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
