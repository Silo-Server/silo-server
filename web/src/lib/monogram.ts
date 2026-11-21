import { leadingGraphemes } from "./text";

/** Two letters for a provider whose logo square is just its name, e.g. "AniList" → "AN". */
export function providerMonogram(name: string): string {
  return leadingGraphemes(name.replace(/[^\p{L}\p{N}]/gu, ""), 2).toUpperCase() || "??";
}
