import type { AdminJob } from "@/api/types";

export function isActiveAdminJob(job: Pick<AdminJob, "status">) {
  return job.status === "queued" || job.status === "running";
}

/**
 * Extracts a library ID from a library refresh admin job payload.
 *
 * @param job - Admin job-like object whose `request_payload` may contain an optional `library_id`.
 * @returns A finite numeric `library_id`, a base-10 `Number.parseInt` result for string
 * values, or `null` for missing, non-numeric, or non-finite values.
 *
 * @example
 * getLibraryRefreshLibraryID({ request_payload: { library_id: "42" } }); // 42
 *
 * @remarks
 * Finite number inputs, including floats, are returned unchanged rather than coerced.
 * String inputs use `Number.parseInt(value, 10)`, so parseInt's leading numeric token
 * rules apply. `NaN`, `Infinity`, and `-Infinity` return `null`.
 */
export function getLibraryRefreshLibraryID(job: Pick<AdminJob, "request_payload">) {
  const value = (job.request_payload as { library_id?: unknown }).library_id;
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  if (typeof value === "string") {
    const parsed = Number.parseInt(value, 10);
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
}

export function getLibraryRefreshLibraryName(job: Pick<AdminJob, "request_payload">) {
  const value = (job.request_payload as { library_name?: unknown }).library_name;
  return typeof value === "string" && value.trim() ? value : null;
}
