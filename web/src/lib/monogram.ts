import { graphemeClusters } from "./text";

/** Two letters for a provider whose logo square is just its name, e.g. "AniList" → "AN". */
export function providerMonogram(name: string): string {
  // Pick whole characters from the name as written, so a letter keeps its accents
  // and vowel signs and never joins a letter from the next word.
  let out = "";
  let taken = 0;
  for (const segment of graphemeClusters(name)) {
    if (taken === 2) break;
    if (!/[\p{L}\p{N}]/u.test(segment)) continue;
    out += segment;
    taken += 1;
  }
  return out.toUpperCase() || "??";
}
