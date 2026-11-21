import type { AdminJob } from "@/api/types";
import { t, type MessageKey, type Translate } from "@/i18n";
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

const JOB_STATUS_LABELS: Record<AdminJob["status"], MessageKey> = {
  queued: "web.admin.common.taskStatus.queued",
  running: "web.admin.common.taskStatus.running",
  completed: "web.admin.common.taskStatus.completed",
  failed: "web.admin.common.taskStatus.failed",
  cancelled: "web.admin.common.taskStatus.cancelled",
};

/** Takes the component's `useT()` translator so the label follows a language change. */
export function jobStatusLabel(status: AdminJob["status"], translate: Translate) {
  const key: MessageKey | undefined = JOB_STATUS_LABELS[status];
  // A status this build does not know yet shows as the server sent it.
  return key ? translate(key) : status;
}
