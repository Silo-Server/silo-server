import type { AudiobookChapter, AudiobookFile } from "@/lib/audiobooks/types";
import type { PlayerChapter } from "@/player/types";

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

/**
 * Flattens per-file chapters into the player's chapter shape with absolute
 * start/end offsets and a contiguous 0-based index across the whole book.
 */
export function buildPlayerChapters(files: AudiobookFile[]): PlayerChapter[] {
  const out: PlayerChapter[] = [];
  let offset = 0;
  let nextIndex = 0;
  for (const file of files) {
    for (const chapter of file.chapters ?? []) {
      const start = offset + chapter.start_seconds;
      const end = offset + (chapter.end_seconds || chapter.start_seconds);
      out.push({
        index: nextIndex++,
        title: chapter.title || `Chapter ${chapter.index + 1}`,
        start_seconds: start,
        end_seconds: end > start ? end : start + 1,
        source: chapter.source || "embedded",
      });
    }
    offset += file.duration_seconds ?? 0;
  }
  return out;
}

/** Absolute start of the chapter after `current`, or null at the last chapter. */
export function nextChapterStart(
  chapters: PlayerChapter[],
  current: PlayerChapter | null,
): number | null {
  if (!current) return null;
  return chapters[current.index + 1]?.start_seconds ?? null;
}

/**
 * Target for a "previous chapter" press, following the universal player
 * convention: more than `restartThresholdSeconds` into the current chapter
 * restarts it; otherwise jump to the previous chapter's start.
 */
export function prevChapterStart(
  chapters: PlayerChapter[],
  current: PlayerChapter | null,
  currentTime: number,
  restartThresholdSeconds = 3,
): number | null {
  if (!current) return null;
  if (currentTime - current.start_seconds > restartThresholdSeconds) {
    return current.start_seconds;
  }
  return (chapters[current.index - 1] ?? current).start_seconds;
}
