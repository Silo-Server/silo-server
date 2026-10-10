import type { RequestMediaType, RequestState } from "@/api/types";
import { v2 } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";

type Schemas = components["schemas"];

/**
 * A watchlist entry for a title the library doesn't have. `status` is active,
 * needs_review (TMDB deleted the ID and several titles could replace it) or
 * removed (TMDB deleted it and nothing replaces it); the server may add
 * values, which read as active.
 */
export interface WatchlistTitle extends Omit<Schemas["WatchlistTitle"], "media_type" | "request"> {
  media_type: RequestMediaType;
  request: RequestState;
}

/** What an add answers: where the entry went, and the request state after it. */
export interface WatchlistTitleEntry extends Omit<
  Schemas["WatchlistTitleEntry"],
  "media_type" | "request"
> {
  media_type: RequestMediaType;
  request: RequestState;
}

/** The largest page the server serves. */
const WATCHLIST_TITLES_PAGE_SIZE = 200;

function watchlistTitleFromV2(t: Schemas["WatchlistTitle"]): WatchlistTitle {
  return t as WatchlistTitle;
}

/**
 * Every watchlist entry for titles the library doesn't have, newest first.
 * The tab sorts and counts the whole list on the client, so this follows the
 * cursor until the server has no more pages. A cursor the server repeats
 * fails the read rather than looping or showing part of the list as all of it.
 */
export async function listWatchlistTitlesV2(signal?: AbortSignal): Promise<WatchlistTitle[]> {
  const out: WatchlistTitle[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  for (;;) {
    const result = await v2("GET /api/v2/watchlist/titles", {
      query: { limit: WATCHLIST_TITLES_PAGE_SIZE, cursor },
      signal,
    });
    out.push(...result.items.map(watchlistTitleFromV2));
    const next = result.page?.has_more ? result.page.next_cursor : undefined;
    if (!next) return out;
    if (seen.has(next)) throw new Error("The watchlist titles list repeated a page cursor.");
    seen.add(next);
    cursor = next;
  }
}

/**
 * Adds a title to the watchlist by its TMDB ID. The server files it under the
 * library item when the library has the title, and may also request it; the
 * answer carries the request state either way.
 */
export function addWatchlistTitleV2(
  mediaType: RequestMediaType,
  tmdbID: number,
): Promise<WatchlistTitleEntry> {
  return v2("PUT /api/v2/watchlist/titles/{media_type}/{tmdb_id}", {
    path: { media_type: mediaType, tmdb_id: tmdbID },
  }).then((entry) => entry as WatchlistTitleEntry);
}

/** Removes a title from the watchlist, and its library item's entry too. */
export async function deleteWatchlistTitleV2(
  mediaType: RequestMediaType,
  tmdbID: number,
): Promise<void> {
  await v2("DELETE /api/v2/watchlist/titles/{media_type}/{tmdb_id}", {
    path: { media_type: mediaType, tmdb_id: tmdbID },
  });
}
