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

// looseChapterLabel prefers a "Chapter <n>" form derived from the index,
// falling back to the chapter's own trimmed title when no index is available.
function looseChapterLabel(chapter: MangaChapter): string {
  if (typeof chapter.chapter_index === "number") {
    return `Chapter ${chapter.chapter_index}`;
  }
  return chapter.title.trim() || "Chapter";
}

// chapterSortKey returns a comparable index where missing indices sort last.
function chapterSortKey(chapter: MangaChapter): number {
  return typeof chapter.chapter_index === "number"
    ? chapter.chapter_index
    : Number.POSITIVE_INFINITY;
}

function byChapterIndex(a: MangaChapter, b: MangaChapter): number {
  return chapterSortKey(a) - chapterSortKey(b);
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
export function buildMangaList(chapters: MangaChapter[]): MangaListEntry[] {
  const volumeBuckets = new Map<string, MangaChapter[]>();
  const loose: MangaChapter[] = [];

  for (const chapter of chapters) {
    const token = chapter.volume?.trim();
    if (token) {
      const bucket = volumeBuckets.get(token);
      if (bucket) {
        bucket.push(chapter);
      } else {
        volumeBuckets.set(token, [chapter]);
      }
    } else {
      loose.push(chapter);
    }
  }

  const ranked: { sortKey: number; entry: MangaListEntry }[] = [];

  for (const chapter of loose) {
    ranked.push({
      sortKey: chapterSortKey(chapter),
      entry: { kind: "chapter", chapter, label: looseChapterLabel(chapter) },
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
