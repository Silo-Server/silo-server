import type { AdminJob } from "@/api/types";

export function isActiveAdminJob(job: Pick<AdminJob, "status">) {
  return job.status === "queued" || job.status === "running";
}

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
