/** Whether the element lays out right to left (its computed `direction`). */
export function isRightToLeft(element: Element): boolean {
  return getComputedStyle(element).direction === "rtl";
}

// Characters of the right-to-left scripts: Hebrew, Arabic, Syriac, Thaana, N'Ko and
// the rest of U+0590 to U+08FF, their presentation forms, and the right-to-left
// blocks of the supplementary planes.
const RTL_CHAR = /[\u0590-\u08ff\ufb1d-\ufdff\ufe70-\ufeff\u{10800}-\u{10fff}\u{1e800}-\u{1efff}]/u;
// The first letter or digit that isn't an ASCII digit: ASCII digits read the same
// in either direction, but Adlam or N'Ko digits read right to left.
const FIRST_DIRECTIONAL = /(?![0-9])[\p{L}\p{N}]/u;

/**
 * The `dir` for an email cut short with an ellipsis. `"ltr"` unless the part
 * before the `@` starts with a right-to-left letter or digit; then `undefined`,
 * so the element keeps the page's direction. A Latin address then keeps its
 * start on a right-to-left page, and one written in Arabic keeps its own start
 * there too. A name part of ASCII digits reads left to right whatever its
 * domain. Unlike `dir="auto"`, it never turns an email right to left on a
 * left-to-right page.
 */
export function emailDir(email: string): "ltr" | undefined {
  const at = email.lastIndexOf("@");
  const name = at > 0 ? email.slice(0, at) : email;
  const first = FIRST_DIRECTIONAL.exec(name)?.[0];
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
