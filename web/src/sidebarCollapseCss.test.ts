// @vitest-environment node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { SIDEBAR_COLLAPSE_DURATION_MS } from "./components/sidebarItemNavigation";
import { SIDEBAR_RAIL_WIDTH, SIDEBAR_SURFACE_WIDTH } from "./components/AppSidebar.logic";

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

  it("slides the frame left by exactly the 196px it has to give up", () => {
    const travel = SIDEBAR_SURFACE_WIDTH - SIDEBAR_RAIL_WIDTH;
    expect(ruleBody('.sidebar-surface[data-collapsed="true"] {')).toContain(
      `transform: translateX(-${travel}px)`,
    );
  });

  it("counter-translates the contents by the same distance so nothing moves on screen", () => {
    const inner = ruleBody(".sidebar-inner {");
    expect(inner).toContain("transform: translateX(0)");
    expect(inner).toMatch(/transition:\s*transform var\(--duration-sidebar-collapse\)/);
    expect(ruleBody('.sidebar-surface[data-collapsed="true"] .sidebar-inner {')).toContain(
      `transform: translateX(${SIDEBAR_SURFACE_WIDTH - SIDEBAR_RAIL_WIDTH}px)`,
    );
  });

  it("moves the main stage on the same compositor timeline as the sidebar", () => {
    const main = ruleBody(".sidebar-main-stage {");
    expect(main).toContain("transform: none");
    expect(main).toMatch(/transition:\s*transform var\(--duration-sidebar-collapse\)/);
    const entering = ruleBody('.sidebar-main-stage[data-sidebar-target-collapsed="true"]:not(');
    expect(entering).toContain("transform: translateX(196px)");
    expect(entering).toContain("transition: none");
    expect(css).toMatch(
      /\.sidebar-main-stage:not\([\s\S]*?data-sidebar-target-collapsed="true"[\s\S]*?\)\[data-sidebar-visual-collapsed="true"\]\s*\{\s*transform:\s*translateX\(-196px\);\s*transition:\s*none/,
    );
  });

  it("uses the shared collapse duration on a compositor-friendly ease", () => {
    const duration = css.match(/--duration-sidebar-collapse:\s*(\d+)ms/);
    expect(duration).not.toBeNull();
    expect(Number(duration![1])).toBe(SIDEBAR_COLLAPSE_DURATION_MS);
    expect(css).toMatch(/--ease-sidebar-collapse:\s*cubic-bezier\(/);
  });

  it("cross-fades and translates sidebar contents rather than resizing them", () => {
    expect(ruleBody(".sidebar-fade {")).toMatch(
      /transition:\s*opacity var\(--duration-sidebar-collapse\)/,
    );
    expect(ruleBody(".sidebar-row-shift {")).toMatch(
      /transition:\s*transform var\(--duration-sidebar-collapse\)/,
    );
    expect(ruleBody('.sidebar-surface[data-collapsed="true"] .sidebar-row-shift {')).toContain(
      "transform: translateX(var(--sidebar-row-shift, 0px))",
    );
  });

  it("no longer animates any layout property, or any main-thread-only one", () => {
    // The old `.sidebar-transition` / `.main-transition` pair is gone; nothing
    // in the sidebar or the main column interpolates geometry any more.
    expect(css).not.toContain(".sidebar-transition");
    expect(css).not.toContain(".main-transition");
    expect(css).not.toMatch(/transition:\s*margin-left/);
    expect(css).not.toMatch(/transition:\s*clip-path/);
    // The bespoke sidebar View Transition snapshot rules are gone.
    expect(css).not.toContain("app-sidebar)");
    expect(css).not.toContain("data-sidebar-collapse-transition");
    expect(css).not.toContain(":active-view-transition");
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

  it("snaps the impersonation banner's margin instead of animating it", () => {
    const banner = ruleBody(".impersonation-banner {");
    expect(banner).toContain("margin-left: var(--app-sidebar-offset)");
    // Animating margin would put the document back on the per-frame layout path.
    expect(banner).not.toContain("transition: margin-left");
    expect(banner).not.toMatch(/transition:[^;]*\b(margin|width|left)\b/);
  });

  it("carries the impersonation banner's edge with a compensating transform", () => {
    // The banner renders outside Layout, so it mirrors `.sidebar-main-stage`
    // off root attributes: hold the previous visual offset with no transition,
    // then release to zero over the shared sidebar duration/easing.
    expect(ruleBody(".impersonation-banner {")).not.toContain("transform:");

    const entering = ruleBody(
      ':root[data-sidebar-collapsed="true"]:not([data-sidebar-visual-collapsed="true"])',
    );
    expect(entering).toContain("transform: translateX(196px)");
    expect(entering).toContain("transition: none");

    const leaving = ruleBody(
      ':root:not([data-sidebar-collapsed="true"])[data-sidebar-visual-collapsed="true"]',
    );
    expect(leaving).toContain("transform: translateX(-196px)");
    expect(leaving).toContain("transition: none");

    // The settled rule animates transform on the same tokens as the sidebar.
    expect(css).toMatch(
      /\.impersonation-banner \{\s*transition: transform var\(--duration-sidebar-collapse\) var\(--ease-sidebar-collapse\);/,
    );
  });

  it("compensates the banner on the same breakpoint that moves its margin", () => {
    // `html[data-text-scale]` re-bases rem, so 64rem and 1024px diverge at large
    // text. The compensation must follow `--app-sidebar-offset`'s own query.
    const nearestQueryBefore = (needle: string) => {
      const at = css.indexOf(needle);
      expect(at, `missing: ${needle}`).toBeGreaterThan(-1);
      const queryAt = css.lastIndexOf("@media", at);
      return css.slice(queryAt, css.indexOf("{", queryAt)).trim();
    };

    expect(nearestQueryBefore("--app-sidebar-offset: 64px")).toBe("@media (min-width: 1024px)");
    expect(
      nearestQueryBefore(
        ':root[data-sidebar-collapsed="true"]:not([data-sidebar-visual-collapsed="true"])',
      ),
    ).toBe("@media (min-width: 1024px)");
  });
});
