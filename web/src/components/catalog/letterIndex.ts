import type { CatalogLetterRun } from "@/hooks/queries/catalog";

/** The bar's letters in display order; `#` holds digits, symbols, and other scripts. */
export const LETTER_BAR_LETTERS = ["#", ..."ABCDEFGHIJKLMNOPQRSTUVWXYZ"] as const;

export function spokenLetter(letter: string) {
  return letter === "#" ? "Numbers and symbols" : letter;
}

export function titleCount(count: number) {
  return count === 1 ? "1 title" : `${count} titles`;
}

/**
 * The letter whose titles the list is showing at `index`: the letter of the
 * last run that starts at or before it. Runs, not each letter's first
 * position, because a collation can sort some of a letter's titles apart
 * from the rest.
 */
export function letterAtIndex(runs: readonly CatalogLetterRun[], index: number) {
  let current: CatalogLetterRun | undefined;
  for (const run of runs) {
    if (run.position > index) break;
    current = run;
  }
  return current?.letter;
}
