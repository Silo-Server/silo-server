/**
 * The video version the viewer asked to play, kept apart from the row that
 * actually served the bytes.
 *
 * Progress and history persistence record the effective file as
 * `last_file_id` (the server prefers the bound virtual candidate), so a
 * neutral or explicitly requested row that resolved to another candidate comes
 * back as the substitute on the next load. This store keeps the requested row
 * on the client instead, keyed by profile and item, and the media page and
 * resume hints read it before the server's version-history fields. Nothing on
 * the wire changes, so native and Jellyfin clients keep their existing meaning
 * for `last_file_id`.
 *
 * A stored row is a hint, not a source of truth: when the catalog no longer
 * carries it the page stays in its pick-again state instead of selecting
 * something else. It also records whether the viewer named the row, so a later
 * Play can tell their pick from one the server made for them.
 */
const STORAGE_KEY = "silo-requested-version";
/** Bounded so a long-lived browser cannot grow the map without limit. */
const MAX_ENTRIES = 200;

export interface RequestedVersion {
  fileId: number;
  /**
   * True when the viewer named this row themselves. Progress and history
   * record whichever row served the bytes, so an Auto start writes its
   * effective row here too; only this flag says the row is a choice the server
   * must not substitute. A remembered entry without it (written before the
   * flag existed) is a hint, not a pick.
   */
  explicit: boolean;
}

interface RequestedVersionEntry extends RequestedVersion {
  updatedAt: number;
}

type RequestedVersionMap = Record<string, RequestedVersionEntry>;

/**
 * The storage key for one profile's pick. `profileId` is the *resolved* active
 * profile, never the raw storage marker: on a hard refresh the marker can be
 * absent until the profile load commits, and falling back to a shared
 * `"device"` bucket there would hand one profile's pick to another. A caller
 * with no resolved profile has nothing to key on and must defer the read.
 */
function entryKey(contentId: string, profileId: string | null): string | null {
  if (!profileId) return null;
  return `${profileId}:${contentId}`;
}

function readMap(): RequestedVersionMap {
  let parsed: unknown;
  try {
    parsed = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "{}");
  } catch {
    return {};
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return {};
  }

  const map: RequestedVersionMap = {};
  for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
    if (value === null || typeof value !== "object") continue;
    const { fileId, updatedAt, explicit } = value as {
      fileId?: unknown;
      updatedAt?: unknown;
      explicit?: unknown;
    };
    if (typeof fileId !== "number" || !Number.isFinite(fileId) || fileId <= 0) continue;
    map[key] = {
      fileId,
      explicit: explicit === true,
      updatedAt: typeof updatedAt === "number" && Number.isFinite(updatedAt) ? updatedAt : 0,
    };
  }
  return map;
}

/**
 * The row the viewer last requested for `contentId`, or null when they never
 * picked one (or their profile has not resolved yet). The caller falls back to
 * the server's `last_file_id`.
 */
export function readRequestedVersion(
  contentId: string,
  profileId: string | null,
): RequestedVersion | null {
  if (!contentId) return null;
  const key = entryKey(contentId, profileId);
  if (!key) return null;
  const entry = readMap()[key];
  return entry ? { fileId: entry.fileId, explicit: entry.explicit } : null;
}

/**
 * The row the viewer last requested for `contentId`, ignoring whether they
 * named it themselves. Callers that decide what to play on the server's behalf
 * must use {@link readRequestedVersion}.
 */
export function readRequestedFileId(contentId: string, profileId: string | null): number | null {
  return readRequestedVersion(contentId, profileId)?.fileId ?? null;
}

/**
 * Records a version pick for `contentId`. The caller must only pass a row the
 * viewer actually requested; the effective playback row belongs to the
 * server's progress/history fields, not here.
 *
 * `explicit` says whether the viewer named the row. Omit it to keep the standing
 * the stored row already has: a later write that only refreshes which row
 * played must not turn an Auto choice into a pin, or a pin into an Auto choice.
 */
export function rememberRequestedFileId(
  contentId: string,
  fileId: number | null | undefined,
  profileId: string | null,
  options: { explicit?: boolean } = {},
): void {
  if (!contentId || fileId == null || !Number.isFinite(fileId) || fileId <= 0) return;

  const map = readMap();
  const key = entryKey(contentId, profileId);
  if (!key) return;
  const previous = map[key];
  const explicit = options.explicit ?? previous?.explicit ?? false;
  // Re-insert so the map keeps most-recently-used order before trimming.
  delete map[key];
  map[key] = { fileId, explicit, updatedAt: Date.now() };

  const entries = Object.entries(map);
  if (entries.length > MAX_ENTRIES) {
    entries.sort(([, a], [, b]) => a.updatedAt - b.updatedAt);
    for (const [staleKey] of entries.slice(0, entries.length - MAX_ENTRIES)) {
      delete map[staleKey];
    }
  }

  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(map));
  } catch {
    // Storage full or unavailable: the page falls back to the server's row.
  }
}
