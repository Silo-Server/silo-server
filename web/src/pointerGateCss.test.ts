// @vitest-environment node

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * Hover reveals are gated in CSS on `data-fine-pointer`, which
 * `lib/pointerCapability.ts` publishes from observed pointer events. These are
 * contract tests over app.css, the same shape as
 * `navigationTransitionCss.test.ts`.
 *
 * What matters: nothing here may fall back to a hover/pointer media feature.
 * Chromium on some Windows machines with a touchscreen reports
 * `any-hover: none` and `any-pointer: coarse` while a mouse is actively
 * hovering, so any rule left behind such a query is silently dead on exactly
 * the devices this gate exists to serve.
 */
const source = readFileSync(fileURLToPath(new URL("./app.css", import.meta.url)), "utf8");

/**
 * Comments here discuss the very media features these tests forbid, so the
 * negative assertions below run against declarations only.
 */
const css = source.replace(/\/\*[\s\S]*?\*\//g, "");

describe("pointer-capability gating", () => {
  it("overrides Tailwind's hover variant onto the attribute", () => {
    // Without this, every `hover:` and `group-hover:` utility compiles into
    // `@media (hover: hover)` and is inert on the affected devices.
    expect(css).toMatch(
      /@custom-variant hover \(:where\(:root\[data-fine-pointer="true"\]\) &:hover\);/,
    );
  });

  it("keeps the gate at zero specificity everywhere it appears", () => {
    // Tailwind orders its state variants by specificity. An unwrapped gate
    // would make every hover: utility out-weigh disabled:, active: and
    // focus-visible: on every device, not just the ones this works around.
    const all = css.match(/:root\[data-fine-pointer="true"\]/g) ?? [];
    const wrapped = css.match(/:where\(:root\[data-fine-pointer="true"\]\)/g) ?? [];
    expect(all.length).toBeGreaterThan(0);
    expect(wrapped).toHaveLength(all.length);
  });

  it("gates the card reveals on the attribute", () => {
    for (const selector of [
      String.raw`:where\(:root\[data-fine-pointer="true"\]\) \.group\\/card:hover \.media-card-action-trigger`,
      String.raw`:where\(:root\[data-fine-pointer="true"\]\) \.group\\/media:hover \.media-card-play-trigger`,
      String.raw`:where\(:root\[data-fine-pointer="true"\]\) \.group\\/media:hover \.media-card-hover-dim`,
    ]) {
      expect(css).toMatch(new RegExp(selector));
    }
  });

  it("leaves no hover or pointer media query gating a reveal", () => {
    expect(css).not.toMatch(/@media[^{]*\bany-hover\b/);
    expect(css).not.toMatch(/@media[^{]*\bany-pointer\b/);
    expect(css).not.toMatch(/@media[^{]*\(\s*hover\s*:/);
  });

  it("never forces the controls back to hidden on a coarse pointer", () => {
    // Such a rule would out-specify the `[data-state="open"]` and
    // `:focus-visible` reveals and hide the trigger while its own menu is open
    // on a touch device.
    expect(css).not.toMatch(/\[data-fine-pointer="false"\]/);
  });
});
