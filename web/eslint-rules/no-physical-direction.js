// Flags Tailwind utilities that name a physical side (`pl-4`, `left-0`,
// `text-right`, `rounded-l-md`, `border-r`…) and fixes them to the logical
// form (`ps-4`, `start-0`, `text-end`, `rounded-s-md`, `border-e`), which
// mirrors under `dir="rtl"` and is identical under `dir="ltr"`.
//
// Physical is right where direction is literal: media timelines, charts,
// positions on artwork, coordinates measured from `getBoundingClientRect`.
// Those files are excluded in eslint.config.js; a single line takes
// `// eslint-disable-next-line silo/no-physical-direction -- <why>`.
//
// Never flagged:
// - `left-1/2`, `right-1/2` and `[50%]`: paired with `-translate-x-1/2` they
//   centre, and a logical start would break that;
// - values built from `env(safe-area-inset-left|right)`, which name a
//   physical edge of the screen;
// - utilities under an `rtl:` or `ltr:` variant, which already say which
//   direction they are for.

import { classParts, classTokenVisitors } from "./class-tokens.js";

const VALUE = String.raw`(?:\d+(?:\.\d+)?|\d+\/\d+|auto|full|px|\[[^\]\s]+\]|\([^)\s]+\))`;
const RADIUS = String.raw`(?:-(?:none|xs|sm|md|lg|xl|2xl|3xl|4xl|full|\[[^\]\s]+\]|\([^)\s]+\)))?`;
const BORDER = String.raw`(?:-[\w./#%\[\]()-]+)?`;

/** [pattern for the utility after variants, replacement]; order matters. */
const MAPPINGS = [
  [String.raw`(-?)(p|m)l-(${VALUE})`, (_, neg, pm, value) => `${neg}${pm}s-${value}`],
  [String.raw`(-?)(p|m)r-(${VALUE})`, (_, neg, pm, value) => `${neg}${pm}e-${value}`],
  [String.raw`(-?)scroll-(p|m)l-(${VALUE})`, (_, neg, pm, v) => `${neg}scroll-${pm}s-${v}`],
  [String.raw`(-?)scroll-(p|m)r-(${VALUE})`, (_, neg, pm, v) => `${neg}scroll-${pm}e-${v}`],
  [String.raw`(-?)left-(${VALUE})`, (_, neg, value) => `${neg}start-${value}`],
  [String.raw`(-?)right-(${VALUE})`, (_, neg, value) => `${neg}end-${value}`],
  [String.raw`text-(left|right)`, (_, side) => `text-${side === "left" ? "start" : "end"}`],
  [
    String.raw`(float|clear)-(left|right)`,
    (_, p, side) => `${p}-${side === "left" ? "start" : "end"}`,
  ],
  [
    String.raw`rounded-(tl|tr|bl|br)(${RADIUS})`,
    (_, corner, radius) => `rounded-${{ tl: "ss", tr: "se", bl: "es", br: "ee" }[corner]}${radius}`,
  ],
  [
    String.raw`rounded-(l|r)(${RADIUS})`,
    (_, side, radius) => `rounded-${side === "l" ? "s" : "e"}${radius}`,
  ],
  [
    String.raw`border-(l|r)(${BORDER})`,
    (_, side, rest) => `border-${side === "l" ? "s" : "e"}${rest}`,
  ],
];

const CENTERING = /^-?(?:left|right)-(?:1\/2|\[50%\])$/;
const PHYSICAL_VALUE = /safe-area-inset-(?:left|right)/;
const DIRECTION_VARIANT = /(?:^|:)(?:rtl|ltr):/;
const COMPILED = MAPPINGS.map(([pattern, replace]) => [new RegExp(`^${pattern}$`), replace]);

/** The logical form of one class token, or null when it isn't physical. */
function logicalClass(token) {
  const { variants, lead, utility, trail } = classParts(token);
  if (CENTERING.test(utility) || PHYSICAL_VALUE.test(utility) || DIRECTION_VARIANT.test(variants)) {
    return null;
  }
  for (const [regex, replace] of COMPILED) {
    if (regex.test(utility)) return variants + lead + utility.replace(regex, replace) + trail;
  }
  return null;
}

export default {
  meta: {
    type: "problem",
    fixable: "code",
    docs: {
      description: "Use logical Tailwind utilities so the layout mirrors right to left",
    },
    messages: {
      physical:
        "Physical-direction utilities don't mirror under dir=rtl: {{list}}. Use the logical form, or disable with the reason direction is literal here.",
    },
    schema: [],
  },
  create(context) {
    return classTokenVisitors(context, logicalClass, "physical");
  },
};
