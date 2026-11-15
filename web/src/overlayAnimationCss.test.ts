// @vitest-environment node

import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { compile } from "tailwindcss";
import { describe, expect, it } from "vitest";

/**
 * The overlay primitives in components/ui animate through tw-animate-css
 * utilities, and app.css replaces that package's enter/exit keyframes so
 * reduced motion drops the slide and zoom. Both fail silently: without the
 * package the classes compile to nothing and every overlay snaps open and
 * shut, and if a package upgrade changes its keyframes the override stops
 * applying and overlays slide again for people who asked for less motion.
 * So these tests compile app.css with Tailwind and check the output.
 */
const srcDir = dirname(fileURLToPath(import.meta.url));
const nodeModules = join(srcDir, "..", "node_modules");

async function loadStylesheet(id: string, base: string) {
  let path: string;
  if (id.startsWith(".")) {
    path = resolve(base, id);
  } else {
    const pkgDir = join(nodeModules, id);
    const pkg = JSON.parse(readFileSync(join(pkgDir, "package.json"), "utf8")) as {
      style?: string;
      exports?: { ".": { style?: string } };
    };
    const style = pkg.exports?.["."]?.style ?? pkg.style;
    if (!style) throw new Error(`${id} has no stylesheet entry`);
    path = join(pkgDir, style);
  }
  return { path, base: dirname(path), content: readFileSync(path, "utf8") };
}

async function build(candidates: string[]): Promise<string> {
  const css = readFileSync(join(srcDir, "app.css"), "utf8");
  const compiler = await compile(css, { base: srcDir, loadStylesheet });
  return compiler.build(candidates);
}

function keyframes(css: string, name: string): string[] {
  const out: string[] = [];
  for (
    let at = css.indexOf(`@keyframes ${name} `);
    at !== -1;
    at = css.indexOf(`@keyframes ${name} `, at + 1)
  ) {
    let depth = 0;
    for (let i = css.indexOf("{", at); i < css.length; i++) {
      if (css[i] === "{") depth++;
      else if (css[i] === "}" && --depth === 0) {
        out.push(css.slice(at, i + 1));
        break;
      }
    }
  }
  return out;
}

describe("overlay animation CSS", () => {
  it("generates the enter and exit utilities the overlay primitives use", async () => {
    const css = await build([
      "data-[state=open]:animate-in",
      "data-[state=closed]:animate-out",
      "data-[state=open]:fade-in-0",
      "data-[state=open]:zoom-in-95",
      "data-[state=open]:slide-in-from-start",
    ]);
    expect(css).toMatch(/&\[data-state="open"\] \{\s*animation: enter /);
    expect(css).toMatch(/&\[data-state="closed"\] \{\s*animation: exit /);
    expect(css).toMatch(/--tw-enter-opacity: 0;/);
    expect(css).toMatch(/--tw-enter-scale: \.95;|--tw-enter-scale: 0\.95;/);
    expect(css).toMatch(/:dir\(rtl\)/);
  });

  it("keeps the replaced keyframes to opacity and a transform reduced motion can turn off", async () => {
    const css = await build(["animate-in", "animate-out"]);
    for (const name of ["enter", "exit"]) {
      // The package's own copy is emitted too; the last one is what applies.
      const last = keyframes(css, name).at(-1);
      expect(last).toMatch(/transform: var\(\s*--overlay-motion,/);
      expect(last).not.toContain("filter");
    }
    const reduced = css.slice(css.indexOf("@media (prefers-reduced-motion: reduce)"));
    expect(reduced).toMatch(/--overlay-motion: none;/);
  });
});
