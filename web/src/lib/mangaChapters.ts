import type { MangaChapter } from "@/api/types";

// MangaChapterGroup is a set of chapters sharing a volume. `volume` is the raw
// volume token (e.g. "v13") for volume groups, or `null` for the catch-all
// group of chapters that carry no volume.
export interface MangaChapterGroup {
  volume: string | null;
  chapters: MangaChapter[];
}

const VOLUME_TOKEN_PATTERN = /^v?(\d+)$/i;

// prettifyVolumeLabel turns a raw volume token into a display label. "v13" and
// "13" both become "Volume 13"; non-numeric tokens (e.g. "Omnibus") pass
// through unchanged so unusual volume schemes still render sensibly.
export function prettifyVolumeLabel(volume: string): string {
  const match = volume.trim().match(VOLUME_TOKEN_PATTERN);
  return match ? `Volume ${Number(match[1])}` : volume.trim();
}

// chapterSortKey returns a comparable index where missing indices sort last.
function chapterSortKey(chapter: MangaChapter): number {
  return typeof chapter.chapter_index === "number"
    ? chapter.chapter_index
    : Number.POSITIVE_INFINITY;
}

// groupMangaChapters groups chapters by their volume token and orders both the
// chapters within each group and the groups themselves by chapter_index
// (nulls last). Chapters with an empty/absent volume collapse into a single
// trailing group keyed by `null`.
export function groupMangaChapters(chapters: MangaChapter[]): MangaChapterGroup[] {
  const groups = new Map<string | null, MangaChapter[]>();
  for (const chapter of chapters) {
    const token = chapter.volume?.trim();
    const key = token ? token : null;
    const bucket = groups.get(key);
    if (bucket) {
      bucket.push(chapter);
    } else {
      groups.set(key, [chapter]);
    }
  }

  const result: MangaChapterGroup[] = [];
  for (const [volume, groupChapters] of groups) {
    const ordered = [...groupChapters].sort((a, b) => chapterSortKey(a) - chapterSortKey(b));
    result.push({ volume, chapters: ordered });
  }

  return result.sort((a, b) => {
    // The catch-all loose group always sorts after named volumes.
    if (a.volume === null) return b.volume === null ? 0 : 1;
    if (b.volume === null) return -1;
    return groupMinIndex(a) - groupMinIndex(b);
  });
}

function groupMinIndex(group: MangaChapterGroup): number {
  return group.chapters.reduce(
    (min, chapter) => Math.min(min, chapterSortKey(chapter)),
    Number.POSITIVE_INFINITY,
  );
}
