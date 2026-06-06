/**
 * Build a compact run of page indices with ellipsis gaps, always anchoring the
 * first and last page plus the current page and its immediate neighbours, e.g.
 * `[0, "ellipsis", 4, 5, 6, "ellipsis", 19]`. Pages are zero-indexed.
 */
export function pageWindow(current: number, pageCount: number): (number | "ellipsis")[] {
  const last = pageCount - 1;
  if (pageCount <= 7) {
    return Array.from({ length: pageCount }, (_, i) => i);
  }
  const anchors = [0, last, current, current - 1, current + 1].filter((p) => p >= 0 && p <= last);
  const unique = [...new Set(anchors)].sort((a, b) => a - b);
  const out: (number | "ellipsis")[] = [];
  let prev: number | null = null;
  for (const p of unique) {
    if (prev !== null && p - prev > 1) out.push("ellipsis");
    out.push(p);
    prev = p;
  }
  return out;
}
