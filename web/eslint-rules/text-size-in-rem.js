// Flags Tailwind font sizes written in pixels (`text-[13px]`, `sm:text-[12.5px]`)
// and fixes them to the same size in rem (`text-[0.8125rem]`).
//
// The Text Size setting (Large, Extra large) and the browser's default font
// size both work by raising the root font size, so text grows only when its
// size is in rem. A pixel size stays put while the text around it grows.

import { classParts, classTokenVisitors } from "./class-tokens.js";

const PX_SIZE = /^text-\[(\d+(?:\.\d+)?)px\]/;

/** The rem form of one class token, or null when it isn't a pixel font size. */
function remClass(token) {
  const { variants, lead, utility, trail } = classParts(token);
  const match = PX_SIZE.exec(utility);
  if (match === null) return null;
  const rem = `text-[${Number(match[1]) / 16}rem]`;
  return variants + lead + rem + utility.slice(match[0].length) + trail;
}

export default {
  meta: {
    type: "problem",
    fixable: "code",
    docs: {
      description: "Size text in rem so it follows the Text Size setting",
    },
    messages: {
      px: "Pixel font sizes don't grow with the Text Size setting or the browser's font size: {{list}}. Use the rem form (eslint --fix converts it).",
    },
    schema: [],
  },
  create(context) {
    return classTokenVisitors(context, remClass, "px");
  },
};
