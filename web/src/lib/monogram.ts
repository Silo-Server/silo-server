import { graphemeClusters } from "./text";

// U+200C ZERO WIDTH NON-JOINER: placed between letters taken from different words
// so that a word-final virama can't fuse with the next letter into one conjunct.
const NON_JOINER = "\u200C";

/** Two letters for a provider whose logo square is just its name, e.g. "AniList" → "AN". */
export function providerMonogram(name: string): string {
  // Pick whole characters from the name as written, so a letter keeps its accents
  // and vowel signs.
  let out = "";
  let taken = 0;
  let skipped = false;
  for (const segment of graphemeClusters(name)) {
    if (taken === 2) break;
    if (!/[\p{L}\p{N}]/u.test(segment)) {
      skipped = taken > 0;
      continue;
    }
    if (skipped) out += NON_JOINER;
    // A keycap emoji such as 1️⃣ counts as its digit.
    out += segment.replace(/[\uFE0F\u20E3]/gu, "");
    taken += 1;
  }
  return out.toUpperCase() || "??";
}
