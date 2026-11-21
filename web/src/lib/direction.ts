/** Whether the element lays out right to left (its computed `direction`). */
export function isRightToLeft(element: Element): boolean {
  return getComputedStyle(element).direction === "rtl";
}

// Characters of the right-to-left scripts: Hebrew, Arabic, Syriac, Thaana, N'Ko and
// the rest of U+0590 to U+08FF, their presentation forms, and the right-to-left
// blocks of the supplementary planes.
const RTL_CHAR = /[\u0590-\u08ff\ufb1d-\ufdff\ufe70-\ufeff\u{10800}-\u{10fff}\u{1e800}-\u{1efff}]/u;
// The characters that set a line's direction: letters, plus the right-to-left
// punctuation and digits that do (bidi class R): the Hebrew maqaf, paseq, sof
// pasuq, nun hafukha, geresh and gershayim, and the N'Ko and Adlam digits.
// Arabic-Indic digits count too: a left-to-right line treats ASCII, Persian and
// fullwidth digits as left-to-right text, but not these. Other digits and
// punctuation are skipped.
const DIRECTIONAL =
  /[\p{L}\u05be\u05c0\u05c3\u05c6\u05f3\u05f4\u0660-\u0669\u07c0-\u07c9\u{1e950}-\u{1e959}]/u;

/**
 * The `dir` for an email cut short with an ellipsis. `"ltr"` unless the first
 * character that sets direction in the part before the `@` is right to left;
 * then `undefined`, so the element keeps the page's direction. A Latin address
 * then keeps its start on a right-to-left page, and one written in Arabic keeps
 * its own start there too. A name part with no such character, such as one of
 * ASCII digits, reads left to right whatever its domain. Unlike `dir="auto"`, it
 * never turns an email right to left on a left-to-right page.
 */
export function emailDir(email: string): "ltr" | undefined {
  const at = email.lastIndexOf("@");
  const name = at > 0 ? email.slice(0, at) : email;
  const first = DIRECTIONAL.exec(name)?.[0];
  return first !== undefined && RTL_CHAR.test(first) ? undefined : "ltr";
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
