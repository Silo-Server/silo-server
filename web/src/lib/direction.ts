/** Whether the element lays out right to left (its computed `direction`). */
export function isRightToLeft(element: Element): boolean {
  return getComputedStyle(element).direction === "rtl";
}

// Characters of the right-to-left scripts: Hebrew, Arabic, Syriac, Thaana, N'Ko and
// the rest of U+0590 to U+08FF, their presentation forms, and the right-to-left
// blocks of the supplementary planes.
const RTL_CHAR = /[\u0590-\u08ff\ufb1d-\ufdff\ufe70-\ufeff\u{10800}-\u{10fff}\u{1e800}-\u{1efff}]/u;
// The characters that set a line's direction (bidi classes L, R and AL): letters,
// the Hebrew maqaf, paseq, sof pasuq, nun hafukha, geresh and gershayim, the Arabic
// question mark, semicolon, full stop and other Arabic, Syriac, Samaritan and
// Mandaic punctuation and signs of those classes (Unicode 15, Basic Multilingual
// Plane), and the N'Ko, Garay and Adlam digits. Most other digits and punctuation
// don't.
const STRONG =
  /[\p{L}\u05be\u05c0\u05c3\u05c6\u05f3\u05f4\u0608\u060b\u060d\u061b\u061d-\u061f\u066d\u06d4\u06fd\u06fe\u0700-\u070d\u070f\u07c0-\u07c9\u07fe\u07ff\u0830-\u083e\u085e\u0888\ufbb2-\ufbc2\ufdfc\u{10d40}-\u{10d49}\u{1e950}-\u{1e959}]/u;
// Arabic-Indic, Hanifi Rohingya and Rumi digits (bidi class AN), which join the
// right-to-left text around them on a left-to-right line.
const ARABIC_NUMBER = /[\u0660-\u0669\u{10d30}-\u{10d39}\u{10e60}-\u{10e7e}]/u;

/**
 * The `dir` for an email cut short with an ellipsis. `"ltr"` unless the first
 * character that sets direction in the part before the `@` is right to left;
 * then `undefined`, so the element keeps the page's direction. A Latin address
 * then keeps its start on a right-to-left page, and one written in Arabic keeps
 * its own start there too. A name part with no such character reads left to
 * right, unless it has Arabic-Indic digits, which follow the domain instead.
 * Unlike `dir="auto"`, it never turns an email right to left on a
 * left-to-right page.
 */
export function emailDir(email: string): "ltr" | undefined {
  const at = email.lastIndexOf("@");
  const name = at > 0 ? email.slice(0, at) : email;
  let first = STRONG.exec(name)?.[0];
  if (first === undefined && at > 0 && ARABIC_NUMBER.test(name)) {
    first = STRONG.exec(email.slice(at + 1))?.[0];
  }
  return first !== undefined && RTL_CHAR.test(first) ? undefined : "ltr";
}

// STRONG, or a directional mark: LRM sets left to right, and RLM and ALM right
// to left (ALM is inside RTL_CHAR's range).
const STRONG_OR_MARK = new RegExp(`[${STRONG.source.slice(1, -1)}\\u200e\\u200f\\u061c]`, "u");

// A line with nothing to show: spaces, zero-width characters and directional marks.
const INVISIBLE = /^[\s\u200b-\u200f\u2060\ufeff]*$/u;

/**
 * Whether the lines of text run different ways, judged as `unicode-bidi:
 * plaintext` judges each paragraph of text shown with its line breaks: by its
 * first character that sets direction, or left to right when it has none
 * (digits, punctuation). A line with nothing to show takes no side.
 */
export function mixesDirections(text: string): boolean {
  const directions = new Set<boolean>();
  for (const line of text.split("\n")) {
    if (INVISIBLE.test(line)) continue;
    const first = STRONG_OR_MARK.exec(line)?.[0];
    directions.add(first !== undefined && (first === "\u200f" || RTL_CHAR.test(first)));
  }
  return directions.size > 1;
}

/**
 * Whether text reads right to left, judged by its first character that sets
 * direction (a letter, a right-to-left digit or punctuation mark, or a
 * directional mark) as `dir="auto"` does; other digits, spaces and punctuation
 * don't count. For text the browser can't judge itself, such as an empty
 * field's placeholder, which `dir="auto"` ignores. A name inside translated
 * text takes `dir="auto"` on its own element instead (src/i18n/README.md).
 */
export function startsRightToLeft(text: string): boolean {
  const strong = STRONG_OR_MARK.exec(text)?.[0];
  // RTL_CHAR covers ALM; RLM sits outside its blocks.
  return strong !== undefined && (strong === "\u200f" || RTL_CHAR.test(strong));
}

/**
 * The arrow key as it reads along the element's direction: right to left,
 * ArrowLeft and ArrowRight swap, so "ArrowRight" always means forward (the
 * next slide, a higher star, a later position). Other keys pass through.
 * Media keys (seek, volume) keep the physical key and don't use this.
 */
export function readingArrowKey(event: { key: string; currentTarget: Element }): string {
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return event.key;
  if (!isRightToLeft(event.currentTarget)) return event.key;
  return event.key === "ArrowLeft" ? "ArrowRight" : "ArrowLeft";
}

// A letter, not a digit: Arabic-Indic digits are in the Arabic script too.
const ARABIC_SCRIPT_LETTER = /(?=\p{L})\p{Script=Arabic}/u;

/**
 * Whether text has any Arabic-script letter (Arabic, Persian, Urdu and others),
 * whose letters join. Safari spaces them apart under letter-spacing, so a
 * letter-spaced element that shows a name or title sets `data-cursive` from
 * this to clear it (app.css). Mixed text loses the spacing on all of it.
 */
export function hasArabicScript(text: string): boolean {
  return ARABIC_SCRIPT_LETTER.test(text);
}

/** Up to this many characters a cut name is short enough to keep whole. */
const SHORT_NAME = 3;

/**
 * The least a name cut with "…" keeps once its line runs out of room: a letter and
 * "…", or all of a short name, since a fixed floor wider than the name would leave
 * a gap after it.
 */
export function nameFloor(name: string): "min-w-max" | "min-w-[2em]" {
  return [...name].length <= SHORT_NAME ? "min-w-max" : "min-w-[2em]";
}
