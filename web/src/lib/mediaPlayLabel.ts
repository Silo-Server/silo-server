/*
 * What a title's play button says.
 *
 * Extracted from the hero so the featured cards, which show the same button on
 * an opening tile, cannot drift from it: the two read the same wording off the
 * same item. Pure, so the branches — a book is read, an audiobook is listened
 * to, a half-finished one resumes — are testable without a browser.
 */

import type { SectionItem } from "@/api/types";

type PlayLabelItem = Pick<
  SectionItem,
  "type" | "position_seconds" | "duration_seconds" | "user_state"
>;

export function mediaPlayLabel(
  item: PlayLabelItem,
  activeAudiobookPlaying?: boolean | null,
): string {
  if (item.type === "ebook") {
    return "Read";
  }
  if (item.type !== "audiobook") {
    return "Play";
  }
  if (activeAudiobookPlaying === true) {
    return "Pause";
  }
  if (activeAudiobookPlaying === false) {
    return "Resume";
  }
  const position = item.position_seconds ?? 0;
  const duration = item.duration_seconds ?? 0;
  if (position > 0 && (duration <= 0 || position < duration)) {
    return "Resume";
  }
  if (item.user_state?.played) {
    return "Listen Again";
  }
  return "Listen";
}
