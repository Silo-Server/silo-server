import type { CatalogLetter } from "@/hooks/queries/catalog";

/** The bar's letters in display order; `#` holds digits, symbols, and other scripts. */
export const LETTER_BAR_LETTERS = ["#", ..."ABCDEFGHIJKLMNOPQRSTUVWXYZ"] as const;

export function spokenLetter(letter: string) {
  return letter === "#" ? "Numbers and symbols" : letter;
}

export function titleCount(count: number) {
  return count === 1 ? "1 title" : `${count} titles`;
}

/**
 * The letter whose titles the list is showing at `index`: the last letter, in
 * list order, that starts at or before it.
 */
export function letterAtIndex(letters: readonly CatalogLetter[], index: number) {
  let current: CatalogLetter | undefined;
  for (const letter of letters) {
    if (letter.position > index) break;
    current = letter;
  }
  return current?.letter;
}
