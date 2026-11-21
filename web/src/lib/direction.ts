/** Whether the element lays out right to left (its computed `direction`). */
export function isRightToLeft(element: Element): boolean {
  return getComputedStyle(element).direction === "rtl";
}

const LETTER = /\p{L}/u;
const RIGHT_TO_LEFT_SCRIPT = /[\p{Script=Arabic}\p{Script=Hebrew}]/u;

/**
 * Whether text reads right to left, judged by its first letter as `dir="auto"`
 * does (digits, spaces and punctuation don't count). For text the browser
 * can't judge itself, such as an empty field's placeholder, which
 * `dir="auto"` ignores.
 */
export function startsRightToLeft(text: string): boolean {
  const letter = LETTER.exec(text);
  return letter !== null && RIGHT_TO_LEFT_SCRIPT.test(letter[0]);
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
