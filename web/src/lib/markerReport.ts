/**
 * Reporting an incorrect segment to TheIntroDB from the play prompt.
 *
 * The call goes through the Silo server rather than straight to TheIntroDB,
 * because the credential behind it is the server's key. Handing one shared secret
 * to every viewer's browser is not a trade worth making, and there is no
 * per-viewer key to use instead. The plugin holds the key and serves the route.
 *
 * The plugin-content mount does not accept the app's bearer token, so a report is
 * two requests: mint a short-lived plugin access cookie, then call the route.
 */
import { v2 } from "@/api/v2/request";

/** Where the host mounts plugin routes. */
const PLUGIN_CONTENT_PREFIX = "/api/v2/plugin-content";

export interface MarkerReportTarget {
  type: "movie" | "tv";
  tvdb_id?: number;
  tmdb_id?: number;
  imdb_id?: string;
  season?: number;
  episode?: number;
}

export type MarkerReportResult =
  | { status: "reported" }
  | { status: "already_reported" }
  | { status: "rejected"; message: string }
  | { status: "unauthorized" };

/**
 * The ids to report by, read from the item's content id.
 *
 * Silo addresses an item as `<type>-<provider>-<id>`, with `<season>-<episode>`
 * appended for an episode. The provider segment is the identity the item was
 * actually matched by, so it is the id to send — reporting a TMDB-matched film by a
 * TVDB id nobody looked up is how a report lands on the wrong media. The season and
 * episode are then already in the numbering that provider implies, which is what the
 * endpoint expects.
 *
 * Returns null for anything this shape does not cover, so the caller offers no
 * control rather than one that reports the wrong thing.
 */
export function parseMarkerReportTarget(
  contentId: string | null | undefined,
): MarkerReportTarget | null {
  if (!contentId) return null;

  const parts = contentId.split("-");
  if (parts.length < 3) return null;

  const [kind, rawProvider, ...rest] = parts;
  const provider = rawProvider ?? "";
  if (!provider) return null;

  if (kind === "movie") {
    const id = rest.join("-");
    if (!id) return null;
    const target: MarkerReportTarget = { type: "movie" };
    return withProviderId(target, provider, id);
  }

  if (kind === "episode") {
    // <series id...>-<season>-<episode>: the trailing pair is the coordinates, so
    // everything before it is the series id (which is not always one segment).
    if (rest.length < 3) return null;
    const episode = Number(rest[rest.length - 1]);
    const season = Number(rest[rest.length - 2]);
    const seriesId = rest.slice(0, rest.length - 2).join("-");
    if (!seriesId || !Number.isFinite(season) || !Number.isFinite(episode)) return null;
    const target: MarkerReportTarget = { type: "tv" };
    const identified = withProviderId(target, provider, seriesId);
    if (!identified) return null;
    return { ...identified, season, episode };
  }

  return null;
}

function withProviderId(
  target: MarkerReportTarget,
  provider: string,
  id: string,
): MarkerReportTarget | null {
  switch (provider) {
    case "tvdb": {
      const parsed = Number(id);
      return Number.isFinite(parsed) && parsed > 0 ? { ...target, tvdb_id: parsed } : null;
    }
    case "tmdb": {
      const parsed = Number(id);
      return Number.isFinite(parsed) && parsed > 0 ? { ...target, tmdb_id: parsed } : null;
    }
    case "imdb":
      return id.startsWith("tt") ? { ...target, imdb_id: id } : null;
    default:
      return null;
  }
}

/**
 * Mint the plugin access cookie.
 *
 * Minted per report rather than remembered. The cookie is bound to the profile that
 * asked for it, so a remembered one outlives a profile switch elsewhere in the app and
 * would send a report under the earlier profile's authority. A report is a deliberate,
 * occasional action, so one launch for it is cheaper than tracking which profile is
 * being served.
 */
async function mintPluginAccess(): Promise<boolean> {
  try {
    await v2("POST /api/v2/auth/plugin-launch");
    return true;
  } catch {
    return false;
  }
}

/**
 * Report one segment as incorrect.
 *
 * A 409 is not a failure: TheIntroDB allows one vote per account per media, so it
 * means this account already reported it. Saying so is the honest outcome — retrying
 * cannot change anything.
 */
export async function reportMarkerSegment(
  installationId: number,
  target: MarkerReportTarget,
): Promise<MarkerReportResult> {
  if (!(await mintPluginAccess())) return { status: "unauthorized" };

  let response: Response;
  try {
    response = await fetch(`${PLUGIN_CONTENT_PREFIX}/plugins/${installationId}/report`, {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(target),
    });
  } catch {
    return { status: "rejected", message: "Silo could not be reached." };
  }

  if (response.status === 409) return { status: "already_reported" };
  if (response.status === 401 || response.status === 403) {
    // Nothing is remembered to invalidate: the next report mints its own launch, which
    // is also what re-reads whoever is signed in by then.
    return { status: "unauthorized" };
  }
  if (response.status === 503) {
    // The plugin has no key of its own to report with, which the viewer can do
    // nothing about; the plugin's internal wording would only mislead here.
    return { status: "rejected", message: "TheIntroDB reporting is not set up on this server." };
  }
  if (response.ok) return { status: "reported" };

  // The plugin answers a refusal with {"error": "..."} in its own words, written
  // for a person. Show those rather than inventing a second phrasing for the rule.
  let message = "The report could not be sent.";
  try {
    const body = (await response.json()) as { error?: string; message?: string };
    if (body?.error) message = body.error;
    else if (body?.message) message = body.message;
  } catch {
    /* a body that is not JSON leaves the fallback above */
  }
  return { status: "rejected", message };
}
