// Mirrors collectionutil.ParseTMDBListURL on the server so forms can flag a
// value that is not a TMDB list before submitting. The server stays the
// authority and re-validates every request.

const TMDB_LIST_HOSTS = new Set(["themoviedb.org", "www.themoviedb.org"]);

function parseListID(value: string): number | null {
  if (!/^\d+$/.test(value)) return null;
  const id = Number(value);
  return Number.isSafeInteger(id) && id > 0 ? id : null;
}

/**
 * Returns the list ID named by a TMDB list page URL
 * (https://www.themoviedb.org/list/310-my-movie-list), the same URL without a
 * scheme, or a bare numeric ID. Returns null for anything else.
 */
export function parseTMDBListID(raw: string): number | null {
  const trimmed = raw.trim();
  if (trimmed === "") return null;
  const bare = parseListID(trimmed);
  if (bare !== null) return bare;

  let parsed: URL;
  try {
    parsed = new URL(trimmed.includes("://") ? trimmed : `https://${trimmed}`);
  } catch {
    return null;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return null;
  if (!TMDB_LIST_HOSTS.has(parsed.hostname.toLowerCase().replace(/\.$/, ""))) return null;
  const [section, page, ...rest] = parsed.pathname.replace(/^\/+|\/+$/g, "").split("/");
  if (section !== "list" || page === undefined || rest.length > 0) return null;
  // The page path is /list/{id}-{slug}; the slug is cosmetic.
  return parseListID(page.split("-")[0] ?? "");
}

/** True when value is non-empty and names a TMDB list. */
export function isValidTMDBListURL(value: string): boolean {
  return parseTMDBListID(value) !== null;
}

export const TMDB_LIST_URL_PLACEHOLDER = "https://www.themoviedb.org/list/310-my-movie-list";
