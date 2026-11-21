/** Whether the element lays out right to left (its computed `direction`). */
export function isRightToLeft(element: Element): boolean {
  return getComputedStyle(element).direction === "rtl";
}

// Characters of the right-to-left scripts: Hebrew, Arabic, Syriac, Thaana, N'Ko and
// the rest of U+0590 to U+08FF, their presentation forms, and the right-to-left
// blocks of the supplementary planes.
const RTL_CHAR = /[\u0590-\u08ff\ufb1d-\ufdff\ufe70-\ufeff\u{10800}-\u{10fff}\u{1e800}-\u{1efff}]/u;
// The characters that set a line's direction (bidi classes L, R and AL): letters,
// the Hebrew maqaf, paseq, sof pasuq, nun hafukha, geresh and gershayim, and the
// N'Ko, Garay and Adlam digits. Other digits and punctuation don't.
const STRONG =
  /[\p{L}\u05be\u05c0\u05c3\u05c6\u05f3\u05f4\u07c0-\u07c9\u{10d40}-\u{10d49}\u{1e950}-\u{1e959}]/u;
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

// A letter, or a directional mark (LRM, RLM, ALM), which also sets direction.
const LETTER_OR_MARK = /[\p{L}\u200E\u200F\u061C]/u;
const RIGHT_TO_LEFT = /[\p{Script=Arabic}\p{Script=Hebrew}\u200F\u061C]/u;

/**
 * Whether text reads right to left, judged by its first letter or directional
 * mark as `dir="auto"` does (digits, spaces and punctuation don't count). For
 * text the browser can't judge itself, such as an empty field's placeholder,
 * which `dir="auto"` ignores.
 */
export function startsRightToLeft(text: string): boolean {
  const strong = LETTER_OR_MARK.exec(text);
  return strong !== null && RIGHT_TO_LEFT.test(strong[0]);
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
