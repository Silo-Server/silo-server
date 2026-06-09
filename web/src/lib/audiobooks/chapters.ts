import type { AudiobookChapter, AudiobookFile } from "@/lib/audiobooks/types";

export interface ChapterListEntry {
  chapter: AudiobookChapter;
  absoluteStart: number;
  fileId: number;
  label: string;
}

/**
 * Flattens the per-file chapter lists of a multi-file audiobook into a single
 * list with absolute start offsets across the whole book.
 */
export function buildChapterList(files: AudiobookFile[]): ChapterListEntry[] {
  const result: ChapterListEntry[] = [];
  let offset = 0;
  for (const file of files) {
    for (const chapter of file.chapters ?? []) {
      result.push({
        chapter,
        absoluteStart: offset + chapter.start_seconds,
        fileId: file.id,
        label: chapter.title || `Chapter ${chapter.index + 1}`,
      });
    }
    offset += file.duration_seconds ?? 0;
  }
  return result;
}

/** Returns the chapter containing the given absolute position, 1-indexed. */
export function findChapterAt(
  chapters: ChapterListEntry[],
  seconds: number,
): { label: string; index: number } | null {
  for (let i = chapters.length - 1; i >= 0; i--) {
    const chapter = chapters[i];
    if (chapter && seconds >= chapter.absoluteStart) {
      return { label: chapter.label, index: i + 1 };
    }
  }
  return chapters[0] ? { label: chapters[0].label, index: 1 } : null;
}

/** Total duration of a multi-file audiobook in seconds. */
export function totalAudiobookDuration(files: AudiobookFile[]): number {
  return files.reduce((acc, file) => acc + (file.duration_seconds ?? 0), 0);
}
