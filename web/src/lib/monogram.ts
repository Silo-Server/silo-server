/** Two letters for a provider whose logo square is just its name, e.g. "AniList" → "AN". */
export function providerMonogram(name: string): string {
  return (
    name
      .replace(/[^\p{L}\p{N}]/gu, "")
      .slice(0, 2)
      .toUpperCase() || "??"
  );
}
