import { appLanguage } from "@/lib/appLocale";

// Built once at load. Without the guard, a browser that lacks Intl.Segmenter
// (Firefox before 125) would fail to load any page that imports this module.
const graphemes =
  typeof Intl.Segmenter === "function"
    ? new Intl.Segmenter(undefined, { granularity: "grapheme" })
    : undefined;

/**
 * The first `count` characters of `text` as a reader sees them. An emoji, a flag,
 * or a letter with its accents counts as one and is never cut in half, which
 * `charAt` and `slice` do to anything outside the Basic Multilingual Plane.
 * Without Intl.Segmenter it counts code points: still never half a character,
 * though a flag or a joined emoji comes out as its first part.
 */
export function leadingGraphemes(text: string, count: number): string {
  if (!graphemes) return Array.from(text).slice(0, count).join("");
  let out = "";
  let taken = 0;
  for (const { segment } of graphemes.segment(text)) {
    if (taken === count) break;
    out += segment;
    taken += 1;
  }
  return out;
}

/**
 * The first `count` characters of `text`, upper-cased by the interface
 * language's rules: Turkish makes "i" into "İ", as CSS `uppercase` does under
 * the page's `lang`. Call it while rendering: it reads the current language.
 */
export function leadingInitials(text: string, count: number): string {
  return leadingGraphemes(text, count).toLocaleUpperCase(appLanguage());
}

/**
 * The initial for an avatar without an image: the name's first character,
 * upper-cased, or "" when it has none.
 */
export function getInitial(name: string): string {
  return leadingInitials(name.trim(), 1);
}

export function getInitials(name: string): string {
  if (!name) return "?";
  const parts = name.split(" ");
  const first = parts[0];
  const last = parts[parts.length - 1];
  if (parts.length >= 2 && first && last) {
    return getInitial(first) + getInitial(last);
  }
  return leadingInitials(name, 2);
}
