import { useQueries } from "@tanstack/react-query";
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
      queryFn: () => fetchVersionLiveness(fileIds),
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
