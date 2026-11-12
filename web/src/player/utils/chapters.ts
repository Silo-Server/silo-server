import type { PlayerChapter } from "../types";

/** The chapter playing at `currentTime`, or -1 between or outside chapters. */
export function findActiveChapterIndex(chapters: PlayerChapter[], currentTime: number): number {
  return chapters.findIndex(
    (chapter) => currentTime >= chapter.start_seconds && currentTime < chapter.end_seconds,
  );
}
