import { JobPageControls } from "@/components/admin/JobPageControls";
import type { AdminJob } from "@/api/types";
import { useAllAdminJobs } from "@/hooks/queries/admin/libraries";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { RefreshCw } from "lucide-react";
import { formatJobProgress } from "./adminCatalogMaintenanceFormatters";
import { formatDateTime } from "@/lib/datetime";
import { useT, type MessageKey, type Translate } from "@/i18n";

const JOB_TYPE_LABELS: Record<string, MessageKey> = {
  delete_library: "web.admin.common.jobHistory.jobType.deleteLibrary",
  image_cache_cleanup: "web.admin.common.jobHistory.jobType.imageCacheCleanup",
  catalog_export: "web.admin.common.jobHistory.jobType.catalogExport",
  catalog_import: "web.admin.common.jobHistory.jobType.catalogImport",
  item_refresh: "web.admin.common.jobHistory.jobType.itemRefresh",
  library_refresh: "web.admin.common.jobHistory.jobType.libraryRefresh",
};

function jobTypeLabel(jobType: string, t: Translate) {
  const key = JOB_TYPE_LABELS[jobType];
  return key ? t(key) : jobType;
}

function statusVariant(status: string) {
  switch (status) {
    case "failed":
      return "destructive" as const;
    case "completed":
      return "secondary" as const;
    case "running":
      return "default" as const;
    default:
      return "outline" as const;
  }
}

function countValues(count: number) {
  return { count, count_text: count };
}

function jobDescription(job: AdminJob, t: Translate) {
  const payload = job.request_payload as Record<string, unknown>;
  switch (job.job_type) {
    case "delete_library":
    case "image_cache_cleanup":
      return payload.library_name
        ? t("web.admin.common.jobHistory.target.library", { name: String(payload.library_name) })
        : t("web.admin.common.jobHistory.target.libraryNumber", { id: String(payload.library_id) });
    case "item_refresh":
    case "library_refresh":
      return payload.library_name
        ? t("web.admin.common.jobHistory.target.library", { name: String(payload.library_name) })
        : payload.library_id
          ? t("web.admin.common.jobHistory.target.libraryNumber", {
              id: String(payload.library_id),
            })
          : t("web.admin.common.jobHistory.target.allLibraries");
    case "catalog_export": {
      const ids = payload.library_ids as number[] | undefined;
      if (ids && ids.length > 0)
        return t("web.admin.common.jobHistory.target.libraryCount", countValues(ids.length));
      return t("web.admin.common.jobHistory.target.allLibraries");
    }
    case "catalog_import":
      return (
        (payload.source_label as string) ||
        (payload.source_key as string) ||
        t("web.admin.common.jobHistory.target.catalogSeed")
      );
    default:
      return "";
  }
}

function imageCacheCleanupTotals(result: Record<string, unknown>, t: Translate) {
  const deletedPrefixes = typeof result.deleted_prefixes === "number" ? result.deleted_prefixes : 0;
  const deletedObjects =
    typeof result.deleted_s3_objects === "number" ? result.deleted_s3_objects : 0;
  return {
    objects: t("web.admin.common.jobHistory.result.cachedObjects", countValues(deletedObjects)),
    prefixes: t("web.admin.common.jobHistory.result.prefixes", countValues(deletedPrefixes)),
  };
}

function jobResult(job: AdminJob, t: Translate) {
  const result = job.result_payload as Record<string, unknown>;
  // A canceled image cache cleanup keeps what it deleted before the cancel.
  // The objects include any from the prefix the cancel interrupted, which
  // does not count as deleted, so the two totals are reported separately.
  if (job.status === "cancelled" && job.job_type === "image_cache_cleanup") {
    return t(
      "web.admin.common.jobHistory.result.cleanupCanceled",
      imageCacheCleanupTotals(result, t),
    );
  }
  if (job.status !== "completed") return null;
  switch (job.job_type) {
    case "library_refresh": {
      const total = result.total_items ?? 0;
      const withIDs = result.items_with_ids ?? 0;
      const withoutIDs = result.items_without_ids ?? 0;
      const refreshedOK = result.refreshed_ok ?? 0;
      const refreshedFailed = result.refreshed_failed ?? 0;
      const pipelineOK = result.pipeline_ok ?? 0;
      const pipelineFailed = result.pipeline_failed ?? 0;
      if (total === 0) {
        return t("web.admin.common.jobHistory.result.nothingToRefresh");
      }
      return t("web.admin.common.jobHistory.result.refreshSummary", {
        total: Number(total),
        with_ids: Number(withIDs),
        without_ids: Number(withoutIDs),
        refreshed_ok: Number(refreshedOK),
        refreshed_failed: Number(refreshedFailed),
        pipeline_ok: Number(pipelineOK),
        pipeline_failed: Number(pipelineFailed),
      });
    }
    case "delete_library": {
      const files = typeof result.deleted_media_files === "number" ? result.deleted_media_files : 0;
      const items =
        typeof result.deleted_orphaned_items === "number" ? result.deleted_orphaned_items : 0;
      const cleanupQueued = result.image_cleanup_queued === true;
      const cleanupDirs =
        typeof result.image_cleanup_dirs === "number" ? result.image_cleanup_dirs : 0;
      const parts = [];
      if (files > 0) parts.push(t("web.admin.common.jobHistory.result.files", countValues(files)));
      if (items > 0) parts.push(t("web.admin.common.jobHistory.result.items", countValues(items)));
      if (cleanupQueued && cleanupDirs > 0) {
        parts.push(t("web.admin.common.jobHistory.result.cleanupQueued", countValues(cleanupDirs)));
      }
      return parts.length > 0
        ? t("web.admin.common.jobHistory.result.deleted", { parts: parts.join(", ") })
        : t("web.admin.common.jobHistory.result.deletedEmpty");
    }
    case "image_cache_cleanup":
      return t(
        "web.admin.common.jobHistory.result.cleanupDone",
        imageCacheCleanupTotals(result, t),
      );
    case "catalog_export":
      return typeof result.items_exported === "number"
        ? t("web.admin.common.jobHistory.result.exported", {
            items: t(
              "web.admin.common.jobHistory.result.items",
              countValues(result.items_exported),
            ),
            files: t(
              "web.admin.common.jobHistory.result.files",
              countValues(typeof result.files_exported === "number" ? result.files_exported : 0),
            ),
          })
        : null;
    case "catalog_import":
      return typeof result.items_created === "number"
        ? t("web.admin.common.jobHistory.result.imported", {
            items: t("web.admin.common.jobHistory.result.items", countValues(result.items_created)),
            files: t(
              "web.admin.common.jobHistory.result.files",
              countValues(typeof result.files_created === "number" ? result.files_created : 0),
            ),
          })
        : null;
    default:
      return null;
  }
}

export default function AdminJobHistory() {
  const t = useT();
  const jobsQuery = useAllAdminJobs();
  const jobs = jobsQuery.data ?? [];

  return (
    <div className="border-border/70 bg-card/60 rounded-lg border">
      <div className="border-border/70 flex items-center justify-between border-b px-4 py-3">
        <div>
          <h3 className="text-sm font-semibold">{t("web.admin.common.jobHistory.title")}</h3>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.common.jobHistory.subtitle")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {jobsQuery.isFetching ? (
            <Badge variant="outline">{t("web.admin.common.jobHistory.refreshing")}</Badge>
          ) : (
            <Badge variant="secondary">{jobs.length}</Badge>
          )}
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("web.admin.common.jobHistory.refresh")}
            onClick={() => jobsQuery.refetch()}
          >
            <RefreshCw
              className={`h-4 w-4 ${jobsQuery.isFetching ? "animate-spin" : ""}`}
              aria-hidden="true"
            />
          </Button>
        </div>
      </div>
      <div className="divide-border/60 divide-y">
        {jobs.length === 0 ? (
          <div className="text-muted-foreground px-4 py-5 text-sm">
            {t("web.admin.common.jobHistory.empty")}
          </div>
        ) : (
          jobs.map((job) => {
            const desc = jobDescription(job, t);
            const result = jobResult(job, t);
            const progress = formatJobProgress(job);

            return (
              <div key={job.id} className="flex flex-col gap-1 px-4 py-3">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant={statusVariant(job.status)}>{job.status}</Badge>
                  <Badge variant="outline">{jobTypeLabel(job.job_type, t)}</Badge>
                  {desc && <span className="text-sm font-medium">{desc}</span>}
                  <span className="text-muted-foreground text-xs">
                    {formatDateTime(job.requested_at)}
                  </span>
                </div>
                <div className="text-muted-foreground flex flex-wrap gap-3 text-xs">
                  {job.message && <span>{job.message}</span>}
                  {(job.status === "running" || job.status === "queued") && (
                    <span>{t("web.admin.common.jobHistory.progress", { progress })}</span>
                  )}
                  {result && <span>{result}</span>}
                  {job.completed_at && (
                    <span>
                      {t("web.admin.common.jobHistory.finished", {
                        date: formatDateTime(job.completed_at),
                      })}
                    </span>
                  )}
                </div>
                {job.error_message && (
                  <div className="text-destructive text-xs">{job.error_message}</div>
                )}
              </div>
            );
          })
        )}
      </div>
      <JobPageControls query={jobsQuery} />
    </div>
  );
}
