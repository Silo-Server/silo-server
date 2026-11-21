import { RuleTester } from "eslint";
import tseslint from "typescript-eslint";
import { afterAll, describe, it } from "vitest";

import rule from "./text-size-in-rem.js";

RuleTester.afterAll = afterAll;
RuleTester.describe = describe;
RuleTester.it = it;

const tester = new RuleTester({
  languageOptions: {
    parser: tseslint.parser,
    parserOptions: { ecmaFeatures: { jsx: true } },
  },
});

const fixed = (code, output) => ({ code, output, errors: 1 });

tester.run("text-size-in-rem", rule, {
  valid: [
    `<div className="text-sm text-[0.8125rem] text-[clamp(2rem,5vw,3.5rem)]" />`,
    // Other utilities that take pixels aren't font sizes.
    `<div className="leading-[18px] tracking-[0.5px] h-[13px]" />`,
    // Only a whole class token counts, not one glued to other text.
    `const hint = "Use text-[13px]. Or text-[13px]px";`,
  ],
  invalid: [
    fixed(
      `<div className="text-[13px] font-medium" />`,
      `<div className="text-[0.8125rem] font-medium" />`,
    ),
    // Half pixels, variants, the important flag and a line-height modifier keep their place.
    fixed(
      `<div className="text-[12.5px] sm:text-[11px] !text-[10.5px] text-[14px]/tight" />`,
      `<div className="text-[0.78125rem] sm:text-[0.6875rem] !text-[0.65625rem] text-[0.875rem]/tight" />`,
    ),
    fixed("cn(`text-[9.5px] ${size}`)", "cn(`text-[0.59375rem] ${size}`)"),
    fixed(
      `<div className="[&_[data-slot=input]]:[&:not(:has([data-a]:hover))]:text-[13px]" />`,
      `<div className="[&_[data-slot=input]]:[&:not(:has([data-a]:hover))]:text-[0.8125rem]" />`,
    ),
    fixed(
      `<div className="text-[13px]/[18px]" />`,
      `<div className="text-[0.8125rem]/[1.125rem]" />`,
    ),
    fixed(`<div className="text-[length:13px]" />`, `<div className="text-[length:0.8125rem]" />`),
  ],
});
