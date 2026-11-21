/** Whether the element lays out right to left (its computed `direction`). */
export function isRightToLeft(element: Element): boolean {
  return getComputedStyle(element).direction === "rtl";
}

// Letters of the right-to-left scripts: Hebrew, Arabic, Syriac, Thaana, N'Ko and
// the rest of U+0590 to U+08FF, their presentation forms, and the right-to-left
// blocks of the supplementary planes.
const RTL_LETTER =
  /[\u0590-\u08ff\ufb1d-\ufdff\ufe70-\ufeff\u{10800}-\u{10fff}\u{1e800}-\u{1efff}]/u;
const LETTER = /\p{L}/u;

/**
 * `"ltr"` unless the text's first letter is from a right-to-left script; then
 * `undefined`, so the element keeps the page's direction. For one-line text cut
 * with an ellipsis, such as an email: an address that starts with Latin letters
 * keeps its start on a right-to-left page, and one written in Arabic keeps its
 * own start too. Unlike `dir="auto"`, it never turns text right to left on a
 * left-to-right page.
 */
export function ltrUnlessRightToLeft(text: string): "ltr" | undefined {
  const letter = LETTER.exec(text)?.[0];
  return letter !== undefined && RTL_LETTER.test(letter) ? undefined : "ltr";
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
