import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { captureProfileRequestContext, StaleApiRequestContextError } from "@/api/client";
import {
  adminDownloadPreparationsKey,
  listAdminDownloadPreparations,
  type AdminDownloadPreparationList,
} from "@/api/v2/adminDownloadPreparations";

// Realtime events keep the list current; the stale time only bounds how long
// a missed event can hide a change.
const ADMIN_DOWNLOAD_PREPARATIONS_STALE_TIME = 30_000;
// Progress events patch rows in place, and not every change to a job's
// requesters (a profile or device purge, a subscription cleanup) announces
// itself. While work is in flight, re-read the list on this cadence so those
// changes still appear.
export const ADMIN_DOWNLOAD_PREPARATIONS_ACTIVE_REFRESH = 60_000;

/** The offline-download preparation queue, kept live by the admin channel. */
export function useAdminDownloadPreparations() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: adminDownloadPreparationsKey(context),
    queryFn: () => {
      if (!context) throw new StaleApiRequestContextError();
      return listAdminDownloadPreparations(context);
    },
    enabled: context !== null,
    staleTime: ADMIN_DOWNLOAD_PREPARATIONS_STALE_TIME,
  });
}

/**
 * How long the server lists a failed job (its PreparationFailedWindow). Failures
 * age out silently, with no realtime event.
 */
export const ADMIN_DOWNLOAD_PREPARATION_FAILED_WINDOW = 24 * 60 * 60 * 1000;

/** When the first listed failure leaves the server's list, or null. */
export function nextFailureExpiry(list: AdminDownloadPreparationList | undefined): number | null {
  let next: number | null = null;
  for (const item of list?.items ?? []) {
    if (item.state !== "failed" || !item.failed_at) continue;
    const failedAt = Date.parse(item.failed_at);
    if (Number.isNaN(failedAt)) continue;
    const expiry = failedAt + ADMIN_DOWNLOAD_PREPARATION_FAILED_WINDOW;
    if (next == null || expiry < next) next = expiry;
  }
  return next;
}

/**
 * Keeps the preparation list current where realtime events cannot: it
 * re-reads on a fixed cadence while jobs are in flight, and once when the
 * oldest listed failure ages out. Mount it once for an acting admin. The
 * cadence is a plain timer rather than the query's refetchInterval because
 * every progress patch updates the query, which would restart that interval
 * and postpone the re-read indefinitely.
 */
export function useAdminDownloadPreparationsRefresh() {
  const { data, dataUpdatedAt, errorUpdatedAt, refetch } = useAdminDownloadPreparations();
  const counts = data?.counts;
  const active = counts ? counts.running + counts.queued + counts.retrying > 0 : false;
  const failureExpiry = nextFailureExpiry(data);
  useEffect(() => {
    if (!active) return;
    const id = window.setInterval(() => {
      void refetch({ cancelRefetch: false });
    }, ADMIN_DOWNLOAD_PREPARATIONS_ACTIVE_REFRESH);
    return () => window.clearInterval(id);
  }, [active, refetch]);
  // Re-armed after every read, successful (dataUpdatedAt) or failed
  // (errorUpdatedAt): if this browser's clock runs ahead of the server's, the
  // first read can still list the failure, and a later read must clear it; a
  // read that fails during an outage must not leave the expired failure cached.
  // A failure already past its expiry is retried on the active cadence, never
  // in a tight loop.
  useEffect(() => {
    if (failureExpiry == null) return;
    const remaining = failureExpiry - Date.now();
    // A second of slack keeps the read from landing just before the server
    // drops the row.
    const delay = remaining > 0 ? remaining + 1_000 : ADMIN_DOWNLOAD_PREPARATIONS_ACTIVE_REFRESH;
    const id = window.setTimeout(() => {
      void refetch({ cancelRefetch: false });
    }, delay);
    return () => window.clearTimeout(id);
  }, [failureExpiry, dataUpdatedAt, errorUpdatedAt, refetch]);
}
