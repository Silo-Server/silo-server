// @vitest-environment node

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * Tailwind v3 wrote a custom property in an arbitrary value as `[--token]`.
 * Tailwind v4 reads that as a bare token and emits `transition-duration:
 * --duration-fast`, which is not valid CSS — browsers drop the declaration and
 * the utility silently falls back to `0s`, so the transition simply does not
 * run. The v4 spelling is `duration-(--token)`, which emits `var(--token)`.
 *
 * The failure is invisible in review and in the browser (a missing transition
 * looks like a design choice), so it is worth a contract test rather than
 * trusting the next person to spot it.
 */
const SOURCE_ROOT = fileURLToPath(new URL(".", import.meta.url));

function sourceFiles(directory: string): string[] {
  const found: string[] = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      found.push(...sourceFiles(path));
    } else if (/\.(ts|tsx|css)$/.test(entry.name)) {
      found.push(path);
    }
  }
  return found;
}

describe("tailwind arbitrary custom-property syntax", () => {
  it("never uses the v3 bracket spelling for a theme token", () => {
    // Utilities that take a bare value — duration, delay, and the rest — all
    // compile the bracket form to an invalid bare token.
    const offenders: string[] = [];
    for (const file of sourceFiles(SOURCE_ROOT)) {
      if (file.endsWith("tailwindTokenSyntax.test.ts")) continue;
      const source = readFileSync(file, "utf8");
      for (const [match] of source.matchAll(/\b(?:duration|delay|ease)-\[--[a-z-]+\]/g)) {
        offenders.push(`${file.slice(SOURCE_ROOT.length)}: ${match}`);
      }
    }

    expect(offenders).toEqual([]);
  });
});
