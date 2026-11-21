import { leadingGraphemes } from "./text";

/** Two letters for a provider whose logo square is just its name, e.g. "AniList" → "AN". */
export function providerMonogram(name: string): string {
  // Keep each letter or digit with the combining marks after it (accents, vowel signs).
  const letters = name.match(/[\p{L}\p{N}]\p{M}*/gu)?.join("") ?? "";
  return leadingGraphemes(letters, 2).toUpperCase() || "??";
}
