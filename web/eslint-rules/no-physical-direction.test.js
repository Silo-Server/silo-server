import { RuleTester } from "eslint";
import tseslint from "typescript-eslint";
import { afterAll, describe, it } from "vitest";

import rule from "./no-physical-direction.js";

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

tester.run("no-physical-direction", rule, {
  valid: [
    // Centring pairs, safe-area edges and explicit direction variants are literal.
    `<div className="absolute left-1/2 -translate-x-1/2" />`,
    `<div className="fixed top-[50%] left-[50%] translate-x-[-50%]" />`,
    `<div className="right-[max(1rem,env(safe-area-inset-right))]" />`,
    `<div className="rtl:left-auto ltr:pl-2" />`,
    // Words and data keys that look like utilities aren't.
    `const position = { "left-edge": 1, "top-left": 2 };`,
    `const css = "border-left: 0; text-lime-500 rounded-lg border-red-200 pl";`,
    // Already logical.
    `<div className="ps-4 me-auto start-0 text-end rounded-s-md border-e" />`,
  ],
  invalid: [
    fixed(
      `<div className="pl-4 md:pr-2.5 -ml-1 mr-auto hover:text-left" />`,
      `<div className="ps-4 md:pe-2.5 -ms-1 me-auto hover:text-start" />`,
    ),
    // One report per string literal or template part.
    {
      code: `cn("left-0 -right-3 sm:left-auto", open && "right-[calc(100%-2px)]!")`,
      output: `cn("start-0 -end-3 sm:start-auto", open && "end-[calc(100%-2px)]!")`,
      errors: 2,
    },
    {
      code: "const c = `rounded-l-md rounded-tr border-l-2 ${x} border-r-primary/40 pl-(--gutter)`;",
      output:
        "const c = `rounded-s-md rounded-se border-s-2 ${x} border-e-primary/40 ps-(--gutter)`;",
      errors: 2,
    },
    fixed(
      `const k = "float-left scroll-ml-4 !pl-0";`,
      `const k = "float-start scroll-ms-4 !ps-0";`,
    ),
  ],
});
