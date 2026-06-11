import type { MangaChapter } from "@/api/types";

// A MangaListEntry is one row in the manga detail list. Most manga releases are
// one file per volume, so the common cases are flat: a `volume` unit (a single
// cbz that is a whole volume) or a loose `chapter` (a single cbz with no volume
// token). Nesting via a `section` only happens when one volume genuinely holds
// multiple chapters.
export type MangaListEntry =
  | { kind: "volume"; chapter: MangaChapter; label: string }
  | { kind: "chapter"; chapter: MangaChapter; label: string }
  | { kind: "section"; label: string; chapters: MangaChapter[] };

const VOLUME_TOKEN_PATTERN = /^v?(\d+)$/i;

// prettifyVolumeLabel turns a raw volume token into a display label. "v13" and
// "13" both become "Volume 13"; non-numeric tokens (e.g. "Omnibus") pass
// through unchanged so unusual volume schemes still render sensibly.
export function prettifyVolumeLabel(volume: string): string {
  const match = volume.trim().match(VOLUME_TOKEN_PATTERN);
  return match ? `Volume ${Number(match[1])}` : volume.trim();
}

// chapterLabel prefers a "Chapter <n>" form derived from the index, falling
// back to the chapter's own trimmed title when no index is available.
export function chapterLabel(chapter: MangaChapter): string {
  if (typeof chapter.chapter_index === "number") {
    return `Chapter ${chapter.chapter_index}`;
  }
  return chapter.title?.trim() || "Chapter";
}

// chapterSortKey returns a comparable index where missing indices sort last.
function chapterSortKey(chapter: MangaChapter): number {
  return typeof chapter.chapter_index === "number"
    ? chapter.chapter_index
    : Number.POSITIVE_INFINITY;
}

function byChapterIndex(a: MangaChapter, b: MangaChapter): number {
  const ka = chapterSortKey(a);
  const kb = chapterSortKey(b);
  // Both missing → both POSITIVE_INFINITY; the subtraction would be NaN (which
  // Array.sort treats as 0, leaving order undefined). Compare explicitly so
  // un-indexed chapters keep a stable order.
  if (ka === kb) return 0;
  return ka < kb ? -1 : 1;
}

// buildMangaList turns a flat chapter list into ordered display entries.
//
// Grouping rules:
//   1. Bucket chapters by trimmed volume token (empty/absent → no-volume).
//   2. No-volume chapters each become their own loose `chapter` entry.
//   3. A volume bucket with exactly one chapter becomes a `volume` unit;
//      with two or more it becomes a `section` (chapters ordered by index).
//   4. All top-level entries order by a representative index: a unit/loose by
//      its own index (nulls last), a section by its minimum chapter index.
// volumeBucketKey canonicalizes a volume token for grouping: "v01", "01" and
// "1" all describe Volume 1 and must land in one bucket (mixed release naming
// otherwise yields duplicate "Volume 1" entries). Non-numeric tokens group by
// their trimmed text.
function volumeBucketKey(token: string): string {
  const match = token.match(VOLUME_TOKEN_PATTERN);
  return match ? String(Number(match[1])) : token;
}

export function buildMangaList(chapters: MangaChapter[]): MangaListEntry[] {
  const volumeBuckets = new Map<string, MangaChapter[]>();
  const loose: MangaChapter[] = [];

  for (const chapter of chapters) {
    const token = chapter.volume?.trim();
    if (token) {
      const key = volumeBucketKey(token);
      const bucket = volumeBuckets.get(key);
      if (bucket) {
        bucket.push(chapter);
      } else {
        volumeBuckets.set(key, [chapter]);
      }
    } else {
      loose.push(chapter);
    }
  }

  const ranked: { sortKey: number; entry: MangaListEntry }[] = [];

  for (const chapter of loose) {
    ranked.push({
      sortKey: chapterSortKey(chapter),
      entry: { kind: "chapter", chapter, label: chapterLabel(chapter) },
    });
  }

  for (const [token, bucket] of volumeBuckets) {
    const ordered = [...bucket].sort(byChapterIndex);
    const label = prettifyVolumeLabel(token);
    const [first] = ordered;
    if (ordered.length === 1 && first) {
      ranked.push({
        sortKey: chapterSortKey(first),
        entry: { kind: "volume", chapter: first, label },
      });
    } else {
      const minIndex = ordered.reduce(
        (min, chapter) => Math.min(min, chapterSortKey(chapter)),
        Number.POSITIVE_INFINITY,
      );
      ranked.push({
        sortKey: minIndex,
        entry: { kind: "section", label, chapters: ordered },
      });
    }
  }

  return ranked.sort((a, b) => a.sortKey - b.sortKey).map((r) => r.entry);
}

// A FlatMangaChapter is one readable unit in series order, with a label that
// stays meaningful out of context ("Volume 3 · Chapter 12" for a chapter
// nested in a volume section). Used by the series Continue CTA and the
// reader's next-chapter navigation.
export interface FlatMangaChapter {
  chapter: MangaChapter;
  label: string;
}

// flattenMangaList unrolls display entries into the flat reading order.
export function flattenMangaList(entries: MangaListEntry[]): FlatMangaChapter[] {
  const flat: FlatMangaChapter[] = [];
  for (const entry of entries) {
    if (entry.kind === "section") {
      for (const chapter of entry.chapters) {
        flat.push({ chapter, label: `${entry.label} · ${chapterLabel(chapter)}` });
      }
    } else {
      flat.push({ chapter: entry.chapter, label: entry.label });
    }
  }
  return flat;
}

// firstUnreadChapter returns the resume target: the first chapter in reading
// order the viewer has not finished, or null when everything is read (or the
// list is empty).
export function firstUnreadChapter(entries: MangaListEntry[]): FlatMangaChapter | null {
  for (const flat of flattenMangaList(entries)) {
    if (flat.chapter.read !== true) {
      return flat;
    }
  }
  return null;
}
