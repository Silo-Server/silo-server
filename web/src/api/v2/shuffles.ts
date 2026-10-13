import type { components } from "./schema";
import { v2 } from "./request";

export type Shuffle = components["schemas"]["Shuffle"];
export type ShuffleScopeRequest = components["schemas"]["ShuffleScopeRequest"];
export type ShuffleScopeKind = ShuffleScopeRequest["kind"];

/** Starts a shuffle over a library, series, season, or collection. */
export async function createShuffle(scope: ShuffleScopeRequest): Promise<Shuffle> {
  return v2("POST /api/v2/shuffles", { body: { scope }, query: { image_size: "large" } });
}

export async function getShuffle(shuffleId: string, signal?: AbortSignal): Promise<Shuffle> {
  return v2("GET /api/v2/shuffles/{shuffle_id}", {
    path: { shuffle_id: shuffleId },
    query: { image_size: "large" },
    signal,
  });
}

/** Moves the shuffle past `fromContentId`; a repeat for the same item changes nothing. */
export async function advanceShuffle(shuffleId: string, fromContentId: string): Promise<Shuffle> {
  return v2("POST /api/v2/shuffles/{shuffle_id}/advance", {
    path: { shuffle_id: shuffleId },
    query: { image_size: "large" },
    body: { from_content_id: fromContentId },
  });
}

/** Replaces the shuffle's next item; a repeat for the same item changes nothing. */
export async function skipShuffleItem(shuffleId: string, nextContentId: string): Promise<Shuffle> {
  return v2("POST /api/v2/shuffles/{shuffle_id}/skip", {
    path: { shuffle_id: shuffleId },
    query: { image_size: "large" },
    body: { next_content_id: nextContentId },
  });
}

export async function deleteShuffle(shuffleId: string): Promise<void> {
  await v2("DELETE /api/v2/shuffles/{shuffle_id}", { path: { shuffle_id: shuffleId } });
}
