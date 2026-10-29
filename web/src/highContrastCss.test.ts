// @vitest-environment node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * High contrast lives entirely in CSS — the app only writes
 * `html[data-high-contrast]` — so these are contract tests over app.css, the
 * same shape as `navigationTransitionCss.test.ts`.
 *
 * What matters: none of these tokens may reference themselves. A custom
 * property whose value references itself is a dependency cycle and is invalid
 * at computed-value time, so the declaration is discarded and the token
 * computes to nothing. That failed silently for the lifetime of the repo —
 * panels lost their background entirely and every border fell back to
 * `currentColor`, i.e. the pure white this block sets as `--foreground`.
 * High contrast made contrast worse.
 */
/**
 * Both stylesheets, concatenated. A theme block may live in either file:
 * app.css holds the upstream theme, themes/weflix.css holds WeFlix. Every check
 * below runs over all of them, so a theme cannot skip a guard by moving file.
 */
const css = [
  readFileSync(fileURLToPath(new URL("./app.css", import.meta.url)), "utf8"),
  readFileSync(fileURLToPath(new URL("./themes/weflix.css", import.meta.url)), "utf8"),
].join("\n");

/** The seven tokens high contrast lightens off a theme value. */
const CONTRAST_TOKENS = [
  "muted-foreground",
  "border",
  "input",
  "surface",
  "surface-hover",
  "surface-raised",
  "accent",
] as const;

/** Body of the `html[data-high-contrast="true"]` token block. */
function highContrastBlock(): string {
  const start = css.indexOf('html[data-high-contrast="true"] {');
  expect(start).toBeGreaterThan(-1);
  let depth = 0;
  for (let i = css.indexOf("{", start); i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}" && --depth === 0) return css.slice(start, i + 1);
  }
  throw new Error("unterminated high-contrast block");
}

describe("high-contrast CSS", () => {
  it("declares no custom property that references itself", () => {
    // Guards the whole stylesheet, not just this block: the same mistake
    // anywhere else is the same silent failure.
    const selfReferencing: string[] = [];
    for (const line of css.split("\n")) {
      const decl = /^\s*--([A-Za-z0-9-]+)\s*:\s*(.*)$/.exec(line);
      if (!decl) continue;
      const name = decl[1] ?? "";
      const value = decl[2] ?? "";
      if (new RegExp(String.raw`var\(\s*--${name}\s*[,)]`).test(value)) {
        selfReferencing.push(line.trim());
      }
    }
    expect(selfReferencing).toEqual([]);
  });

  it("lightens each contrast token off its -base input", () => {
    const block = highContrastBlock();
    for (const token of CONTRAST_TOKENS) {
      expect(block).toMatch(
        new RegExp(String.raw`--${token}:\s*color-mix\([^;]*var\(--${token}-base\)\)`),
      );
    }
  });

  it("defines every -base input in every theme", () => {
    const themes = [...css.matchAll(/\[data-theme="([a-z-]+)"\] \{([\s\S]*?)\n {2}\}/g)];
    // Both themes, named, so a theme that stops being parsed fails here rather
    // than passing by absence.
    expect(themes.map((theme) => theme[1]).sort()).toEqual(["midnight-cinema", "weflix"]);
    for (const theme of themes) {
      const name = theme[1] ?? "";
      const body = theme[2] ?? "";
      for (const token of CONTRAST_TOKENS) {
        expect(
          new RegExp(String.raw`^\s*--${token}-base:`, "m").test(body),
          `theme ${name} is missing --${token}-base`,
        ).toBe(true);
      }
    }
  });
});
