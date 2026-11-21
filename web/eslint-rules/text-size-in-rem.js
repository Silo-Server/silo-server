// Flags Tailwind font sizes written in pixels (`text-[13px]`, `sm:text-[12.5px]`)
// and fixes them to the same size in rem (`text-[0.8125rem]`).
//
// The Text Size setting (Large, Extra large) and the browser's default font
// size both work by raising the root font size, so text grows only when its
// size is in rem. A pixel size stays put while the text around it grows.

import { classParts, classTokenVisitors } from "./class-tokens.js";

// The whole utility: a pixel size, then optionally `/` and a line height.
const PX_SIZE = /^text-\[(length:)?(\d+(?:\.\d+)?)px\](?:\/(.+))?$/i;
const PX_LINE_HEIGHT = /^\[(\d+(?:\.\d+)?)px\]$/i;

const rem = (px) => `${Number(px) / 16}rem`;

/** The rem form of one class token, or null when it isn't a pixel font size. */
function remClass(token) {
  const { variants, lead, utility, trail } = classParts(token);
  const match = PX_SIZE.exec(utility);
  if (match === null) return null;
  const [, hint = "", px, lineHeight] = match;
  // A pixel line height would stay put while the text grows, so it moves to rem too.
  const pxLineHeight = lineHeight && PX_LINE_HEIGHT.exec(lineHeight);
  const modifier = pxLineHeight ? `/[${rem(pxLineHeight[1])}]` : lineHeight ? `/${lineHeight}` : "";
  return `${variants}${lead}text-[${hint}${rem(px)}]${modifier}${trail}`;
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
