import type { ItemDetail } from "@/api/types";

/** A public provider page for a title, such as its IMDb or TMDB entry. */
export interface TitleLink {
  label: string;
  href: string;
}

/** The kinds of title with a provider page whose URL the IDs alone can build. */
export type TitleLinkKind = "movie" | "series" | "episode";

export interface TitleLinkIDs {
  imdb?: string | null;
  tmdb?: string | number | null;
  tvdb?: string | number | null;
}

const IMDB_ID = /^tt\d+$/;
const NUMERIC_ID = /^[1-9]\d*$/;

function numericID(id: string | number | null | undefined): string | undefined {
  const value = id == null ? "" : String(id).trim();
  return NUMERIC_ID.test(value) ? value : undefined;
}

/**
 * Links to a title's IMDb, TMDB and TVDB pages, for whichever IDs it has, in
 * that order. An ID that does not have its provider's shape is skipped rather
 * than linked to a page that would not exist. An episode gets no TMDB link:
 * TMDB addresses episodes by series, season and number, not by the episode ID
 * the catalog stores. The URL forms match the Discord embeds'
 * (internal/notifications/discord_embed_meta.go).
 */
export function titleLinks(kind: TitleLinkKind, ids: TitleLinkIDs): TitleLink[] {
  const links: TitleLink[] = [];
  const imdb = ids.imdb?.trim();
  if (imdb && IMDB_ID.test(imdb)) {
    links.push({ label: "IMDb", href: `https://www.imdb.com/title/${imdb}/` });
  }
  const tmdb = numericID(ids.tmdb);
  if (tmdb && kind !== "episode") {
    links.push({
      label: "TMDB",
      href: `https://www.themoviedb.org/${kind === "series" ? "tv" : "movie"}/${tmdb}`,
    });
  }
  const tvdb = numericID(ids.tvdb);
  if (tvdb) {
    links.push({ label: "TVDB", href: `https://thetvdb.com/dereferrer/${kind}/${tvdb}` });
  }
  return links;
}

/**
 * A library item's provider links. Seasons get none: the catalog stores no
 * provider IDs for them.
 */
export function itemTitleLinks(
  item: Pick<ItemDetail, "type" | "imdb_id" | "tmdb_id" | "tvdb_id">,
): TitleLink[] {
  if (item.type !== "movie" && item.type !== "series" && item.type !== "episode") return [];
  return titleLinks(item.type, { imdb: item.imdb_id, tmdb: item.tmdb_id, tvdb: item.tvdb_id });
}
