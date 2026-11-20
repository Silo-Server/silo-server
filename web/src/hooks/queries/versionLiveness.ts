import { useQueries, type QueryClient } from "@tanstack/react-query";
import { useMemo } from "react";

import type { VersionLivenessResponse, VersionLivenessResult } from "@/api/types";
import { v2 } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";
import { catalogKeys } from "@/hooks/queries/keys";
import { isVirtualFileVersion } from "@/pages/ItemDetail/components/versionFormatUtils";

type CatalogVersionsCheck = components["schemas"]["CatalogVersionsCheck"];
type CatalogVersionCheckResult = CatalogVersionsCheck["results"][number];

/**
 * The minimal server-shaped fields the liveness check reads from a row. Both
 * the item page's `FileVersion` and the player's `PlayerFileVersion` satisfy it,
 * so one check serves every version list without either growing the other's
 * required columns.
 */
export interface VersionLivenessCandidate {
  file_id: number;
  container?: string;
  file_path?: string;
  available?: boolean;
  /** Durable failed verdict (confirmed unservable). Menus remove rows with
   * this flag instead of offering them; absent means not failed. */
  failed?: boolean;
  /** A recent probe rejected the provider's bytes as a different release than
   *  this row declares. Advisory only; never implies `failed`. */
  source_mismatch?: { served_bytes: number; declared_bytes: number };
}

/** A source-mismatch verdict: bytes served versus bytes declared. */
export interface VersionSourceMismatchVerdict {
  servedBytes: number;
  declaredBytes: number;
}

/**
 * A liveness verdict for one file: whether the version is currently
 * servable, plus whether the server confirmed the row dead (durable failed
 * verdict). A dead row never recovers without a fresh successful resolve;
 * an unavailable row without the flag may be transient. A `sourceMismatch`
 * is neither: the release is alive, only the served bytes were wrong.
 */
export interface VersionLivenessVerdict {
  available: boolean;
  failed?: boolean;
  sourceMismatch?: VersionSourceMismatchVerdict;
}

/** Maximum file_ids per versions/check request. */
export const VERSION_LIVENESS_BATCH_SIZE = 40;

/** How long a liveness result is trusted before it is re-checked. */
export const VERSION_LIVENESS_STALE_MS = 5 * 60 * 1000;

/**
 * A check slower than this is a slow provider resolution, not a hiccup. Its
 * answer is held for longer so a slow server is asked less, not more: the check
 * is advisory menu health, and re-probing a struggling provider only adds load.
 */
export const VERSION_LIVENESS_SLOW_MS = 1_000;

/** Ceiling on the window a slow check earns (10x its own latency). */
export const VERSION_LIVENESS_MAX_WINDOW_MS = 15 * 60 * 1000;

/**
 * Floor between two checks of one row set when a catalog `versions_updated`
 * event asks for one. The event says a row was stamped or rewritten, not which
 * rows, and a refresh announces its burst one row at a time.
 */
export const VERSION_LIVENESS_FORCED_MIN_INTERVAL_MS = 30 * 1000;

/** Coalescing window for a burst of `versions_updated` events. */
export const VERSION_LIVENESS_REFRESH_DELAY_MS = 750;

/** Upper bound on how long coalescing may postpone a refresh. */
export const VERSION_LIVENESS_REFRESH_MAX_WAIT_MS = 3 * 1000;

/**
 * Reads one v2 check result into the numeric file ids the item page and player
 * menus key on. The v2 contract renders ids as opaque strings; a value that is
 * not a positive safe integer names no catalog row, so it is dropped rather
 * than keyed under NaN.
 */
function livenessResultFromV2(
  result: CatalogVersionCheckResult,
): VersionLivenessResult | undefined {
  const fileId = Number(result.file_id);
  if (!Number.isSafeInteger(fileId) || fileId <= 0) {
    return undefined;
  }
  return {
    file_id: fileId,
    available: result.available,
    failed: result.failed,
    source_mismatch: result.source_mismatch
      ? {
          served_bytes: result.source_mismatch.served_bytes,
          declared_bytes: result.source_mismatch.declared_bytes,
        }
      : undefined,
  };
}

export async function fetchVersionLiveness(
  fileIds: number[],
  options?: { signal?: AbortSignal },
): Promise<VersionLivenessResponse> {
  const response = await v2("POST /api/v2/catalog/versions/check", {
    body: { file_ids: fileIds.map(String) },
    ...(options?.signal ? { signal: options.signal } : {}),
  });
  return {
    results: response.results.flatMap((result) => {
      const mapped = livenessResultFromV2(result);
      return mapped ? [mapped] : [];
    }),
  };
}

/**
 * The checks one server has already answered, one entry per row set, and the
 * one in flight for it. A row set is the batch a single request names, so the
 * key is the sorted id list: two callers asking about the same rows share an
 * answer, and a caller asking about different rows gets its own.
 */
interface LivenessCheckRecord {
  at: number;
  durationMs: number;
  response: VersionLivenessResponse;
}

const checkedRowSets = new Map<string, LivenessCheckRecord>();
const inFlightRowSets = new Map<string, Promise<VersionLivenessResponse>>();
/** Row sets a catalog event asked to re-check, waiting for the next request. */
const forcedRowSets = new Set<string>();

function rowSetKey(fileIds: number[]): string {
  return [...fileIds].sort((a, b) => a - b).join(",");
}

/** How long this row set's answer stays authoritative. */
function reuseWindowMs(record: LivenessCheckRecord): number {
  if (record.durationMs < VERSION_LIVENESS_SLOW_MS) {
    return VERSION_LIVENESS_STALE_MS;
  }
  return Math.min(
    VERSION_LIVENESS_MAX_WINDOW_MS,
    Math.max(VERSION_LIVENESS_STALE_MS, record.durationMs * 10),
  );
}

/**
 * Checks a row set at most as often as its own cost justifies.
 *
 * A liveness check resolves the provider per row, so a re-check is not free:
 * it is the most expensive read a version list makes. Three rules keep one row
 * set from being asked about more than it can answer:
 *
 * - One request in flight per row set; a second caller waits on the first.
 * - An answer stands for VERSION_LIVENESS_STALE_MS, and longer when the check
 *   was slow (10x its own latency, capped at VERSION_LIVENESS_MAX_WINDOW_MS).
 * - A catalog `versions_updated` event may force an earlier check, but not
 *   more often than VERSION_LIVENESS_FORCED_MIN_INTERVAL_MS. The event names no
 *   rows, so every known row set is marked; the floor is what stops a refresh
 *   that announces hundreds of stamped rows from re-probing them per event.
 *
 * A row set that actually changed is a new key and is checked at once. Nothing
 * here polls: a re-render, a remount, a re-enable, or an unchanged list reuses
 * the answer it already has.
 */
export function checkVersionLiveness(fileIds: number[]): Promise<VersionLivenessResponse> {
  const key = rowSetKey(fileIds);
  const running = inFlightRowSets.get(key);
  if (running) return running;

  const record = checkedRowSets.get(key);
  const now = Date.now();
  if (record) {
    const age = now - record.at;
    const forced = forcedRowSets.has(key);
    const window = forced ? VERSION_LIVENESS_FORCED_MIN_INTERVAL_MS : reuseWindowMs(record);
    if (age < window) {
      forcedRowSets.delete(key);
      return Promise.resolve(record.response);
    }
  }
  forcedRowSets.delete(key);

  const startedAt = now;
  const request = fetchVersionLiveness(fileIds)
    .then((response) => {
      checkedRowSets.set(key, {
        at: Date.now(),
        durationMs: Date.now() - startedAt,
        response,
      });
      return response;
    })
    .catch((error: unknown) => {
      // A failed check teaches nothing about the rows. Drop the record so the
      // next caller retries instead of reusing a stale verdict as if it were
      // an answer.
      checkedRowSets.delete(key);
      throw error;
    })
    .finally(() => {
      inFlightRowSets.delete(key);
    });
  inFlightRowSets.set(key, request);
  return request;
}

/**
 * Marks every known row set for one re-check: a catalog event said a row was
 * stamped or its versions rewritten, and the event names no rows. The floor in
 * `checkVersionLiveness` turns any number of these into one check per row set.
 */
export function markVersionLivenessStale() {
  for (const key of checkedRowSets.keys()) {
    forcedRowSets.add(key);
  }
}

/** Drops every cached answer. Tests only; production never needs a reset. */
export function resetVersionLivenessCache() {
  checkedRowSets.clear();
  inFlightRowSets.clear();
  forcedRowSets.clear();
}

const pendingRefreshes = new WeakMap<
  QueryClient,
  { timer: ReturnType<typeof setTimeout>; deadline: number }
>();

/**
 * Re-checks the mounted version lists after a catalog `versions_updated` event.
 *
 * A refresh stamping rows emits one event per row, and each used to invalidate
 * every mounted check at once. The invalidation is therefore coalesced: one
 * lands per refresh burst, and the check itself is rate-limited per row set by
 * `markVersionLivenessStale`.
 */
export function scheduleVersionLivenessRefresh(queryClient: QueryClient) {
  const existing = pendingRefreshes.get(queryClient);
  if (existing) clearTimeout(existing.timer);
  const now = Date.now();
  const deadline = existing?.deadline ?? now + VERSION_LIVENESS_REFRESH_MAX_WAIT_MS;
  const timer = setTimeout(
    () => {
      pendingRefreshes.delete(queryClient);
      markVersionLivenessStale();
      void queryClient.invalidateQueries(
        { queryKey: catalogKeys.versionLiveness },
        { cancelRefetch: false },
      );
    },
    Math.max(0, Math.min(VERSION_LIVENESS_REFRESH_DELAY_MS, deadline - now)),
  );
  pendingRefreshes.set(queryClient, { timer, deadline });
}

/**
 * Batches the virtual file_ids of `versions` into chunks of at most
 * VERSION_LIVENESS_BATCH_SIZE, each sorted ascending so the query key is
 * stable regardless of the order the caller passes versions in.
 */
export function chunkVirtualFileIds(versions: VersionLivenessCandidate[]): number[][] {
  const fileIds = versions
    .filter((version) => isVirtualFileVersion(version))
    .map((version) => version.file_id)
    .sort((a, b) => a - b);

  const chunks: number[][] = [];
  for (let i = 0; i < fileIds.length; i += VERSION_LIVENESS_BATCH_SIZE) {
    chunks.push(fileIds.slice(i, i + VERSION_LIVENESS_BATCH_SIZE));
  }
  return chunks;
}

/**
 * Merges the batched liveness results over the item's own metadata. A check
 * result is fresher than the item snapshot (the snapshot was fetched before
 * the check ran), so for any file_id the check reports, the result is
 * authoritative: true recovers a version the metadata marked unavailable (the
 * server clears `failed_at` on a successful check), false marks it
 * unavailable. Item metadata `available === false` only stays authoritative
 * while the check has not yet reported for that file_id. Virtual versions
 * with no result yet are unknown (absent from the map) and remain visible;
 * non-virtual versions are not part of the check batch, so their metadata
 * flag stays authoritative unless the backend reports them too.
 */
export function mergeVersionLiveness(
  versions: VersionLivenessCandidate[],
  results: VersionLivenessResponse | undefined,
): Map<number, VersionLivenessVerdict> {
  const availability = new Map<number, VersionLivenessVerdict>();
  for (const version of versions) {
    if (version.available === false) {
      availability.set(version.file_id, {
        available: false,
        failed: version.failed,
        sourceMismatch: sourceMismatchVerdict(version),
      });
    }
  }
  for (const result of results?.results ?? []) {
    // The check ran after the item snapshot was fetched, so its result wins
    // for the file_ids it reports — including recovering a version the
    // metadata marked unavailable.
    availability.set(result.file_id, {
      available: result.available,
      failed: result.failed,
      sourceMismatch: sourceMismatchVerdict(result),
    });
  }
  return availability;
}

function sourceMismatchVerdict(row: {
  source_mismatch?: { served_bytes: number; declared_bytes: number };
}): VersionSourceMismatchVerdict | undefined {
  const mismatch = row.source_mismatch;
  if (!mismatch) {
    return undefined;
  }
  return { servedBytes: mismatch.served_bytes, declaredBytes: mismatch.declared_bytes };
}

/**
 * Returns a Map<file_id, available> for the given versions. The check only
 * covers virtual versions (local files are always available) and only fires
 * while `enabled` is true and there is at least one virtual version without a
 * fresh result. Results are cached for VERSION_LIVENESS_STALE_MS, keyed on the
 * sorted file_ids, so opening the version list repeatedly does not re-fetch.
 */
export function useVersionLiveness(
  versions: VersionLivenessCandidate[],
  enabled: boolean,
): Map<number, VersionLivenessVerdict> {
  const chunks = useMemo(() => chunkVirtualFileIds(versions), [versions]);

  const queryResults = useQueries({
    queries: chunks.map((fileIds) => ({
      queryKey: [...catalogKeys.versionLiveness, fileIds],
      // Through the gate, not straight to the network: re-renders, remounts and
      // event-driven invalidations of an unchanged row set reuse its answer
      // instead of resolving the provider again.
      queryFn: () => checkVersionLiveness(fileIds),
      enabled: enabled && fileIds.length > 0,
      staleTime: VERSION_LIVENESS_STALE_MS,
    })),
  });

  // Depend on the individual query data values (referentially stable while a
  // chunk's result is unchanged) rather than the queryResults array, so the
  // returned Map keeps its identity across unrelated re-renders.
  return useMemo(
    () =>
      mergeVersionLiveness(versions, {
        results: queryResults.flatMap((query) => query.data?.results ?? []),
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- depend on each query's data, not the result objects.
    [versions, ...queryResults.map((query) => query.data)],
  );
}

/**
 * Stamps the checked availability onto the rows themselves.
 *
 * The menu reads a row's health from `available`, so the verdict has to live on
 * the row rather than in a side map the menu cannot see. Only a `file_id` the
 * check actually reported is touched: an unreported row keeps its metadata
 * flag and an unchanged row keeps its identity, so downstream memoization and
 * active-source resolution are not invalidated by the liveness read.
 */
export function applyVersionAvailability<T extends VersionLivenessCandidate>(
  versions: T[],
  availability: Map<number, VersionLivenessVerdict>,
): T[] {
  let changed = false;
  const next = versions.map((version) => {
    const verdict = availability.get(version.file_id);
    if (verdict === undefined) {
      return version;
    }
    // A failed flag is only ever written when there is something to say: an
    // explicit flag from the check, or clearing a stale one after a
    // successful resolve. Rows that never carried the field keep their
    // identity so downstream memoization does not churn.
    let failed = verdict.failed;
    if (failed === undefined) {
      failed = verdict.available && version.failed === true ? false : version.failed;
    }
    // The source mismatch is a transient probe verdict, so it is rebuilt from
    // the check's answer every time: present replaces, absent clears. It never
    // carries a failed meaning of its own.
    const sourceMismatch = verdict.sourceMismatch
      ? {
          served_bytes: verdict.sourceMismatch.servedBytes,
          declared_bytes: verdict.sourceMismatch.declaredBytes,
        }
      : undefined;
    if (
      version.available === verdict.available &&
      version.failed === failed &&
      sameSourceMismatch(version.source_mismatch, sourceMismatch)
    ) {
      return version;
    }
    changed = true;
    return { ...version, available: verdict.available, failed, source_mismatch: sourceMismatch };
  });
  return changed ? next : versions;
}

function sameSourceMismatch(
  current: { served_bytes: number; declared_bytes: number } | undefined,
  next: { served_bytes: number; declared_bytes: number } | undefined,
): boolean {
  if (current === undefined || next === undefined) {
    return current === next;
  }
  return (
    current.served_bytes === next.served_bytes && current.declared_bytes === next.declared_bytes
  );
}
