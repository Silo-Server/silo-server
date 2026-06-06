import type { AdminJob } from "@/api/types";

function formatCount(value: number) {
  return value.toLocaleString();
}

export function formatExportProgressLabel(
  current: number,
  total: number,
  status: AdminJob["status"],
) {
  if (total > 0) {
    return `${formatCount(current)} / ${formatCount(total)}`;
  }
  if (status === "queued") {
    return "Queued";
  }
  return "Waiting";
}

export function formatJobProgress(job: AdminJob) {
  if (job.progress_total > 0) {
    return `${formatCount(job.progress_current)} / ${formatCount(job.progress_total)}`;
  }
  if (job.status === "queued") {
    return "Queued";
  }
  if (job.status === "completed") {
    return "Complete";
  }
  if (job.status === "cancelled") {
    return "Cancelled";
  }
  return "Starting";
}
