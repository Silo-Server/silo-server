import type { AdminJob } from "@/api/types";
import { t } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

function formatCount(value: number) {
  return value.toLocaleString(formatLocale());
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
    return t("web.admin.catalogMaintenance.progress.queued");
  }
  return t("web.admin.catalogMaintenance.progress.waiting");
}

export function formatJobProgress(job: AdminJob) {
  if (job.progress_total > 0) {
    return `${formatCount(job.progress_current)} / ${formatCount(job.progress_total)}`;
  }
  if (job.status === "queued") {
    return t("web.admin.catalogMaintenance.progress.queued");
  }
  if (job.status === "completed") {
    return t("web.admin.catalogMaintenance.progress.complete");
  }
  if (job.status === "cancelled") {
    return t("web.admin.catalogMaintenance.progress.cancelled");
  }
  return t("web.admin.catalogMaintenance.progress.starting");
}
