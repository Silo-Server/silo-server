// @vitest-environment node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * The sidebar-collapse behaviour lives mostly in CSS, so these are contract
 * tests over app.css rather than over rendered markup.
 *
 * The rules that matter: the sidebar surface stays 260px wide in every state,
 * entering /item/* snaps the real layout from a 260px to a 64px offset in one
 * pass, and the only animated property is `transform`. Transform is the one
 * choice Chrome runs on the compositor — a clipping-path version of this was
 * swallowed whole by the detail page's render (267ms and 209ms frozen frames).
 */
const css = readFileSync(fileURLToPath(new URL("./app.css", import.meta.url)), "utf8");

/**
 * Body of the rule whose selector list starts at `selector`, searched from
 * after the reduced-motion block so the `transition: none` overrides in there
 * are never mistaken for the real declarations.
 */
function ruleBody(selector: string): string {
  // This anchor is intentionally after reduced motion, whose selector list
  // also contains `.sidebar-main-stage` but only declares transition: none.
  const from = css.indexOf("@media (forced-colors: active)");
  expect(from, "forced-colors anchor must remain after reduced motion").toBeGreaterThan(-1);
  const start = css.indexOf(selector, from);
  expect(start, `missing rule: ${selector}`).toBeGreaterThan(-1);
  return css.slice(css.indexOf("{", start), css.indexOf("}", start));
}

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

describe("sidebar collapse CSS", () => {
  it("animates transform and nothing else on the surface", () => {
    const surface = ruleBody(".sidebar-surface {");
    expect(surface).toContain("overflow: hidden");
    expect(surface).toContain("transform: translateX(0)");
    expect(surface).toMatch(/transition:\s*transform var\(--duration-sidebar-collapse\)/);
    // Any layout property here would put the document back on the reflow path,
    // and any non-composited property would be starved by the route render.
    expect(surface).not.toMatch(/\b(width|margin-left|left|clip-path|grid-template-columns)\s*:/);
  });

  it("makes reduced motion an instant state change", () => {
    const block = reducedMotionBlock();
    expect(block).toContain("--duration-sidebar-collapse: 0ms");
    for (const selector of [
      ".sidebar-surface,",
      ".sidebar-inner,",
      ".sidebar-fade,",
      ".sidebar-row-shift,",
      ".sidebar-main-stage,",
      ".impersonation-banner",
    ]) {
      expect(block).toContain(selector);
    }
  });

  it("only reserves sidebar room for out-of-tree chrome while a shell is mounted", () => {
    // The banner and the audiobook MiniBar render above every route, including
    // /profiles, /login and /watch, which live outside Layout and have no
    // sidebar. Layout publishes `data-app-shell` for exactly the sidebar's
    // lifetime and AdminLayout publishes `data-admin-shell` for its own fixed
    // 240px sidebar, so every desktop offset must hang off one of them; an
    // unconditional 260px left a blank strip on the profile picker while
    // impersonating (#1290).
    expect(ruleBody(":root[data-app-shell] {")).toContain("--app-sidebar-offset: 260px");
    expect(ruleBody(':root[data-app-shell][data-sidebar-collapsed="true"] {')).toContain(
      "--app-sidebar-offset: 64px",
    );
    expect(ruleBody(":root[data-admin-shell] {")).toContain("--app-sidebar-offset: 240px");
    // Nothing else may reintroduce a non-zero offset without a shell gate.
    const desktopOffsets =
      css.match(/^\s*([^{\n]+)\{\s*--app-sidebar-offset: (?:260|240|64)px;/gm) ?? [];
    expect(desktopOffsets.length).toBe(3);
    for (const rule of desktopOffsets) expect(rule).toMatch(/\[data-(?:app|admin)-shell\]/);
    // Off-shell the variable stays at its 0px default.
    expect(css).toMatch(/:root \{\s*--app-sidebar-offset: 0px;/);
  });

  it("moves the offset and the banner compensation on the sidebar's own breakpoint", () => {
    // Layout shows the sidebar at Tailwind's `lg` (64rem), and a rem media query
    // follows the browser's default font size. A px breakpoint kept the offset
    // while a larger browser font had already hidden the sidebar (#1816). The
    // compensation must follow the query that moves the margin it compensates.
    const nearestQueryBefore = (needle: string) => {
      const at = css.indexOf(needle);
      expect(at, `missing: ${needle}`).toBeGreaterThan(-1);
      const queryAt = css.lastIndexOf("@media", at);
      return css.slice(queryAt, css.indexOf("{", queryAt)).trim();
    };

    expect(nearestQueryBefore("--app-sidebar-offset: 64px")).toBe("@media (min-width: 64rem)");
    expect(
      nearestQueryBefore(
        ':root[data-sidebar-collapsed="true"]:not([data-sidebar-visual-collapsed="true"])',
      ),
    ).toBe("@media (min-width: 64rem)");
  });
});
