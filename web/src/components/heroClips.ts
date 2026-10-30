import type { SectionItem } from "@/api/types";

/**
 * The clip to play behind a slide, when this server has one for the title.
 *
 * The server decides: a title the featured rows hold gets a clip made once (see
 * `internal/heroclips`), and the section card carries its URL. An item without
 * one is the ordinary case, and the hero keeps its backdrop.
 */
export function heroClipUrl(item: Pick<SectionItem, "hero_video_url">): string | undefined {
  const url = item.hero_video_url?.trim();
  return url ? url : undefined;
}

