// Built once at load. Without the guard, a browser that lacks Intl.Segmenter
// (Firefox before 125) would fail to load any page that imports this module.
const graphemes =
  typeof Intl.Segmenter === "function"
    ? new Intl.Segmenter(undefined, { granularity: "grapheme" })
    : undefined;

/**
 * The characters of `text` as a reader sees them. An emoji, a flag, or a letter
 * with its accents is one character and is never cut in half, which `charAt` and
 * `slice` do to anything outside the Basic Multilingual Plane. Without
 * Intl.Segmenter it yields code points: still never half a character, though a
 * flag or a joined emoji comes out as separate parts.
 */
export function* graphemeClusters(text: string): Generator<string> {
  if (!graphemes) {
    yield* Array.from(text);
    return;
  }
  for (const { segment } of graphemes.segment(text)) yield segment;
}

/** The first `count` characters of `text`, as {@link graphemeClusters} counts them. */
export function leadingGraphemes(text: string, count: number): string {
  let out = "";
  let taken = 0;
  for (const segment of graphemeClusters(text)) {
    if (taken === count) break;
    out += segment;
    taken += 1;
  }
  return out;
}

/**
 * The initial for an avatar without an image: the name's first character,
 * upper-cased, or "" when it has none.
 */
export function getInitial(name: string): string {
  return leadingGraphemes(name.trim(), 1).toUpperCase();
}

export function getInitials(name: string): string {
  if (!name) return "?";
  const parts = name.split(" ");
  const first = parts[0];
  const last = parts[parts.length - 1];
  if (parts.length >= 2 && first && last) {
    return getInitial(first) + getInitial(last);
  }
  return leadingGraphemes(name, 2).toUpperCase();
}
