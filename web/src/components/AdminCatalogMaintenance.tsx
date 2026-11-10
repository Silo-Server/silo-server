import { JobPageControls } from "@/components/admin/JobPageControls";
import { useState } from "react";
import type { FormEvent } from "react";
import type {
  AdminJob,
  CatalogSeedExportResult,
  CatalogSeedImportSource,
  CatalogPathRewrite,
} from "@/api/types";
import {
  useCatalogExportJobs,
  useCatalogImportJobs,
  useCatalogImportSources,
  useCreateCatalogExportJob,
  useImportCatalogSeed,
  useLocalImportSources,
  usePublishCatalogExportJob,
} from "@/hooks/queries/admin/libraries";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogDescription,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  addEmptyPathRewrite,
  createEmptyPathRewrite,
  removePathRewrite,
  type PathRewriteRow,
  updatePathRewrite,
} from "./adminCatalogMaintenancePathRewrites";
import { formatExportProgressLabel, formatJobProgress } from "./adminCatalogMaintenanceFormatters";
import { Download, Plus, RefreshCw, Trash2, Upload } from "lucide-react";
import { copyTextToClipboard } from "@/lib/clipboard";
import { formatDateTime } from "@/lib/datetime";
import { toast } from "sonner";
import type { ReactNode } from "react";
import { useT, type Translate } from "@/i18n";

// Example paths and URLs shown as placeholders; not translated.
const LOCAL_PATH_EXAMPLE = "/catalog-seeds/my-catalog.json.gz";
const REMOTE_URL_EXAMPLE = "https://example.com/catalog-seeds/export.json.gz";
const REWRITE_FROM_EXAMPLE = "/srv/media";
const REWRITE_TO_EXAMPLE = "/media";

const monospace = (chunks: ReactNode[]) => <span className="font-mono">{chunks}</span>;

export default function AdminCatalogMaintenance() {
  const t = useT();
  const [importDialogOpen, setImportDialogOpen] = useState(false);
  const [execution, setExecution] = useState<"queued" | "synchronous">("queued");
  const exportJobsQuery = useCatalogExportJobs();
  const importJobsQuery = useCatalogImportJobs();
  const importSourcesQuery = useCatalogImportSources();
  const exportMutation = useCreateCatalogExportJob();
  const publishMutation = usePublishCatalogExportJob();
  const importMutation = useImportCatalogSeed();
  const exportJobs = exportJobsQuery.data ?? [];
  const importJobs = importJobsQuery.data ?? [];
  const completedExportJobs = exportJobs.filter((job) => job.status === "completed");
  const bucketImportSources = importSourcesQuery.data ?? [];
  const localImportSourcesQuery = useLocalImportSources();
  const localImportSources = localImportSourcesQuery.data ?? [];
  const [localPath, setLocalPath] = useState("/catalog-seeds/");
  const [remoteURL, setRemoteURL] = useState("");
  const [importSource, setImportSource] = useState<
    "local_path" | "export_job" | "bucket_artifact" | "remote_url"
  >("local_path");
  const [selectedExportJobId, setSelectedExportJobId] = useState("");
  const [selectedArtifactKey, setSelectedArtifactKey] = useState("");
  const [conflictMode, setConflictMode] = useState<"skip_existing" | "overwrite_existing">(
    "skip_existing",
  );
  const [pathRewrites, setPathRewrites] = useState<PathRewriteRow[]>([createEmptyPathRewrite()]);

  function updateRewrite(index: number, field: keyof CatalogPathRewrite, value: string) {
    setPathRewrites((current) => updatePathRewrite(current, index, field, value));
  }

  function addRewrite() {
    setPathRewrites((current) => addEmptyPathRewrite(current));
  }

  function removeRewrite(index: number) {
    setPathRewrites((current) => removePathRewrite(current, index));
  }

  function resetImportState() {
    setImportSource("local_path");
    setExecution("queued");
    setLocalPath("/catalog-seeds/");
    setSelectedExportJobId("");
    setSelectedArtifactKey("");
    setRemoteURL("");
    setConflictMode("skip_existing");
    setPathRewrites([createEmptyPathRewrite()]);
  }

  function handleImportSubmit(e: FormEvent) {
    e.preventDefault();
    if (importSource === "local_path" && !localPath.trim()) return;
    if (importSource === "export_job" && !selectedExportJobId) return;
    if (importSource === "bucket_artifact" && !selectedArtifactKey) return;
    if (importSource === "remote_url" && !remoteURL.trim()) return;

    const filteredRewrites = pathRewrites.filter(
      (rewrite) => rewrite.from.trim() && rewrite.to.trim(),
    );

    importMutation.mutate(
      {
        source: importSource,
        execution,
        ...(importSource === "local_path"
          ? { local_path: localPath.trim() }
          : importSource === "export_job"
            ? { export_job_id: selectedExportJobId }
            : importSource === "bucket_artifact"
              ? { artifact_key: selectedArtifactKey }
              : { remote_url: remoteURL.trim() }),
        conflict_mode: conflictMode,
        path_rewrites: filteredRewrites,
      },
      {
        onSuccess: () => {
          setImportDialogOpen(false);
          resetImportState();
        },
      },
    );
  }

  const addRewriteLabel = t("web.admin.catalogMaintenance.import.addRewrite");
  const isImportSubmitDisabled =
    importMutation.isPending ||
    (importSource === "local_path"
      ? !localPath.trim()
      : importSource === "export_job"
        ? !selectedExportJobId
        : importSource === "bucket_artifact"
          ? !selectedArtifactKey
          : !remoteURL.trim());

  return (
    <div className="space-y-6">
      <div className="border-border/70 bg-card/60 flex flex-col gap-4 rounded-lg border p-4 lg:flex-row lg:items-center lg:justify-between">
        <div className="space-y-1">
          <h2 className="text-lg font-semibold">
            {t("web.admin.catalogMaintenance.header.title")}
          </h2>
          <p className="text-muted-foreground text-sm">
            {t("web.admin.catalogMaintenance.header.body")}
          </p>
        </div>
        <div className="ml-auto flex shrink-0 flex-wrap justify-end gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => exportMutation.mutate({})}
            disabled={exportMutation.isPending}
          >
            <Download
              className={`mr-1 h-4 w-4 ${exportMutation.isPending ? "animate-pulse" : ""}`}
            />
            {t("web.admin.catalogMaintenance.header.startExport")}
          </Button>
          <Dialog
            open={importDialogOpen}
            onOpenChange={(open) => {
              setImportDialogOpen(open);
              if (!open) {
                resetImportState();
              }
            }}
          >
            <DialogTrigger asChild>
              <Button variant="outline" size="sm">
                <Upload className="mr-1 h-4 w-4" />
                {t("web.admin.catalogMaintenance.header.importCatalog")}
              </Button>
            </DialogTrigger>
            <DialogContent className="sm:max-w-2xl">
              <DialogHeader>
                <DialogTitle>{t("web.admin.catalogMaintenance.import.title")}</DialogTitle>
                <DialogDescription>
                  {t("web.admin.catalogMaintenance.import.description")}
                </DialogDescription>
              </DialogHeader>
              <form onSubmit={handleImportSubmit} className="space-y-4">
                <div className="space-y-2">
                  <Label>{t("web.admin.catalogMaintenance.import.sourceLabel")}</Label>
                  <Select
                    value={importSource}
                    onValueChange={(value) =>
                      setImportSource(value as "local_path" | "export_job" | "bucket_artifact")
                    }
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="local_path">
                        {t("web.admin.catalogMaintenance.import.sourceLocalFile")}
                      </SelectItem>
                      <SelectItem value="export_job">
                        {t("web.admin.catalogMaintenance.import.sourceExportJob")}
                      </SelectItem>
                      <SelectItem value="bucket_artifact">
                        {t("web.admin.catalogMaintenance.import.sourceBucketArtifact")}
                      </SelectItem>
                      <SelectItem value="remote_url">
                        {t("web.admin.catalogMaintenance.import.sourceRemoteUrl")}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label>{t("web.admin.catalogMaintenance.import.executionLabel")}</Label>
                  <Select
                    value={execution}
                    onValueChange={(value: "queued" | "synchronous") => setExecution(value)}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="queued">
                        {t("web.admin.catalogMaintenance.import.executionQueued")}
                      </SelectItem>
                      <SelectItem value="synchronous">
                        {t("web.admin.catalogMaintenance.import.executionSynchronous")}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="text-muted-foreground text-xs">
                    {execution === "queued"
                      ? t("web.admin.catalogMaintenance.import.executionQueuedHelp")
                      : t("web.admin.catalogMaintenance.import.executionSynchronousHelp")}
                  </p>
                </div>
                {importSource === "local_path" ? (
                  <div className="space-y-2">
                    <Label>{t("web.admin.catalogMaintenance.import.filePathLabel")}</Label>
                    <Input
                      value={localPath}
                      onChange={(e) => setLocalPath(e.target.value)}
                      placeholder={LOCAL_PATH_EXAMPLE}
                    />
                    {localImportSources.length > 0 && (
                      <>
                        <div className="flex items-center justify-between gap-2">
                          <Label className="text-muted-foreground text-xs">
                            {t("web.admin.catalogMaintenance.import.detectedFiles")}
                          </Label>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={() => localImportSourcesQuery.refetch()}
                            disabled={localImportSourcesQuery.isFetching}
                          >
                            <RefreshCw
                              className={`mr-1 h-4 w-4 ${localImportSourcesQuery.isFetching ? "animate-spin" : ""}`}
                            />
                            {t("web.admin.catalogMaintenance.refresh")}
                          </Button>
                        </div>
                        <Select value="" onValueChange={(value) => setLocalPath(value)}>
                          <SelectTrigger>
                            <SelectValue
                              placeholder={t(
                                "web.admin.catalogMaintenance.import.selectDetectedFile",
                              )}
                            />
                          </SelectTrigger>
                          <SelectContent>
                            {localImportSources.map((source) => (
                              <SelectItem key={source.key} value={source.key}>
                                {describeImportSource(source)}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </>
                    )}
                    <p className="text-muted-foreground text-xs">
                      {t("web.admin.catalogMaintenance.import.filePathHelp", { code: monospace })}
                    </p>
                    {localImportSourcesQuery.hasNextPage && (
                      <Button
                        type="button"
                        variant="outline"
                        disabled={localImportSourcesQuery.isFetchingNextPage}
                        onClick={() => void localImportSourcesQuery.fetchNextPage()}
                      >
                        {t("web.admin.catalogMaintenance.import.loadMoreLocal")}
                      </Button>
                    )}
                    {localImportSourcesQuery.isError && (
                      <Button
                        type="button"
                        variant="outline"
                        onClick={() => void localImportSourcesQuery.restart()}
                      >
                        {t("web.admin.catalogMaintenance.import.retryLocal")}
                      </Button>
                    )}
                  </div>
                ) : importSource === "export_job" ? (
                  <div className="space-y-2">
                    <Label>{t("web.admin.catalogMaintenance.import.completedExportLabel")}</Label>
                    <Select value={selectedExportJobId} onValueChange={setSelectedExportJobId}>
                      <SelectTrigger>
                        <SelectValue
                          placeholder={t("web.admin.catalogMaintenance.import.chooseExport")}
                        />
                      </SelectTrigger>
                      <SelectContent>
                        {completedExportJobs.length === 0 ? (
                          <SelectItem value="__none" disabled>
                            {t("web.admin.catalogMaintenance.import.noCompletedExports")}
                          </SelectItem>
                        ) : (
                          completedExportJobs.map((job) => (
                            <SelectItem key={job.id} value={job.id}>
                              {describeExportJob(t, job)}
                            </SelectItem>
                          ))
                        )}
                      </SelectContent>
                    </Select>
                    <p className="text-muted-foreground text-xs">
                      {t("web.admin.catalogMaintenance.import.exportJobHelp")}
                    </p>
                  </div>
                ) : importSource === "bucket_artifact" ? (
                  <div className="space-y-2">
                    <div className="flex items-center justify-between gap-2">
                      <Label>{t("web.admin.catalogMaintenance.import.bucketLabel")}</Label>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => importSourcesQuery.refetch()}
                        disabled={importSourcesQuery.isFetching}
                      >
                        <RefreshCw
                          className={`mr-1 h-4 w-4 ${importSourcesQuery.isFetching ? "animate-spin" : ""}`}
                        />
                        {t("web.admin.catalogMaintenance.refresh")}
                      </Button>
                      {importSourcesQuery.hasNextPage && (
                        <Button
                          type="button"
                          variant="outline"
                          disabled={importSourcesQuery.isFetchingNextPage}
                          onClick={() => void importSourcesQuery.fetchNextPage()}
                        >
                          {t("web.admin.catalogMaintenance.import.loadMoreBucket")}
                        </Button>
                      )}
                      {importSourcesQuery.isError && (
                        <Button
                          type="button"
                          variant="outline"
                          onClick={() => void importSourcesQuery.restart()}
                        >
                          {t("web.admin.catalogMaintenance.import.retryBucket")}
                        </Button>
                      )}
                    </div>
                    <Select value={selectedArtifactKey} onValueChange={setSelectedArtifactKey}>
                      <SelectTrigger>
                        <SelectValue
                          placeholder={t("web.admin.catalogMaintenance.import.chooseBucketSeed")}
                        />
                      </SelectTrigger>
                      <SelectContent>
                        {bucketImportSources.length === 0 ? (
                          <SelectItem value="__none" disabled>
                            {t("web.admin.catalogMaintenance.import.noBucketSeeds")}
                          </SelectItem>
                        ) : (
                          bucketImportSources.map((source) => (
                            <SelectItem key={source.key} value={source.key}>
                              {describeImportSource(source)}
                            </SelectItem>
                          ))
                        )}
                      </SelectContent>
                    </Select>
                    <p className="text-muted-foreground text-xs">
                      {t("web.admin.catalogMaintenance.import.bucketHelp")}
                    </p>
                  </div>
                ) : (
                  <div className="space-y-2">
                    <Label>{t("web.admin.catalogMaintenance.import.remoteUrlLabel")}</Label>
                    <Input
                      value={remoteURL}
                      onChange={(e) => setRemoteURL(e.target.value)}
                      placeholder={REMOTE_URL_EXAMPLE}
                    />
                    <p className="text-muted-foreground text-xs">
                      {t("web.admin.catalogMaintenance.import.remoteUrlHelp", { code: monospace })}
                    </p>
                  </div>
                )}
                <div className="space-y-2">
                  <Label>{t("web.admin.catalogMaintenance.import.conflictLabel")}</Label>
                  <Select
                    value={conflictMode}
                    onValueChange={(value) =>
                      setConflictMode(value as "skip_existing" | "overwrite_existing")
                    }
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="skip_existing">
                        {t("web.admin.catalogMaintenance.import.conflictSkip")}
                      </SelectItem>
                      <SelectItem value="overwrite_existing">
                        {t("web.admin.catalogMaintenance.import.conflictOverwrite")}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-3">
                  <div className="space-y-1">
                    <Label>{t("web.admin.catalogMaintenance.import.rewritesLabel")}</Label>
                    <p className="text-muted-foreground text-xs">
                      {t("web.admin.catalogMaintenance.import.rewritesHelp", { code: monospace })}
                    </p>
                  </div>
                  {pathRewrites.map((rewrite, index) => (
                    <div key={rewrite.id} className="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
                      <Input
                        value={rewrite.from}
                        onChange={(e) => updateRewrite(index, "from", e.target.value)}
                        placeholder={REWRITE_FROM_EXAMPLE}
                      />
                      <Input
                        value={rewrite.to}
                        onChange={(e) => updateRewrite(index, "to", e.target.value)}
                        placeholder={REWRITE_TO_EXAMPLE}
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="shrink-0"
                        aria-label={t("web.admin.catalogMaintenance.import.removeRewrite")}
                        onClick={() => removeRewrite(index)}
                      >
                        <Trash2 className="h-4 w-4" aria-hidden="true" />
                      </Button>
                    </div>
                  ))}
                  <Button type="button" variant="outline" size="sm" onClick={addRewrite}>
                    <Plus className="mr-1 h-4 w-4" /> {addRewriteLabel}
                  </Button>
                </div>
                <div className="border-border/60 bg-muted/30 text-muted-foreground rounded-md border p-3 text-xs">
                  {t("web.admin.catalogMaintenance.import.validationNote")}
                </div>
                <Button type="submit" className="w-full" disabled={isImportSubmitDisabled}>
                  {importMutation.isPending
                    ? t("web.admin.catalogMaintenance.import.importing")
                    : t("web.admin.catalogMaintenance.import.submit")}
                </Button>
              </form>
            </DialogContent>
          </Dialog>
        </div>
      </div>

      <div className="border-border/70 bg-card/60 rounded-lg border">
        <div className="border-border/70 flex items-center justify-between border-b px-4 py-3">
          <div>
            <h3 className="text-sm font-semibold">
              {t("web.admin.catalogMaintenance.imports.title")}
            </h3>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.catalogMaintenance.imports.body")}
            </p>
          </div>
          {importJobsQuery.isFetching ? (
            <Badge variant="outline">{t("web.admin.catalogMaintenance.jobs.refreshing")}</Badge>
          ) : (
            <Badge variant="secondary">{importJobs.length}</Badge>
          )}
        </div>
        <JobPageControls
          query={importJobsQuery}
          label={t("web.admin.catalogMaintenance.imports.loadOlder")}
        />
        <div className="divide-border/60 divide-y">
          {importJobs.length === 0 ? (
            <div className="text-muted-foreground px-4 py-5 text-sm">
              {t("web.admin.catalogMaintenance.imports.empty")}
            </div>
          ) : (
            importJobs.map((job) => {
              const importResult = job.result_payload as Record<string, number | undefined>;
              const progressPercent = getJobProgressPercent(job);

              return (
                <div
                  key={job.id}
                  className="flex flex-col gap-3 px-4 py-4 lg:flex-row lg:items-center lg:justify-between"
                >
                  <div className="min-w-0 flex-1 space-y-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge
                        variant={
                          job.status === "failed"
                            ? "destructive"
                            : job.status === "completed"
                              ? "secondary"
                              : "outline"
                        }
                      >
                        {job.status}
                      </Badge>
                      <span className="text-sm font-medium">{describeImportJob(t, job)}</span>
                      <span className="text-muted-foreground text-xs">
                        {t("web.admin.catalogMaintenance.jobs.requested", {
                          date: formatDateTime(job.requested_at),
                        })}
                      </span>
                    </div>
                    <div className="text-muted-foreground text-sm">
                      {job.message || t("web.admin.catalogMaintenance.imports.jobFallback")}
                    </div>
                    <div className="bg-muted h-2 overflow-hidden rounded-full">
                      <div
                        className="bg-primary h-full rounded-full transition-[width] duration-300"
                        style={{ width: `${progressPercent}%` }}
                      />
                    </div>
                    <div className="text-muted-foreground flex flex-wrap gap-4 text-xs">
                      <span>
                        {t("web.admin.catalogMaintenance.jobs.progress", {
                          progress: formatJobProgress(job),
                        })}
                      </span>
                      {job.completed_at ? (
                        <span>
                          {t("web.admin.catalogMaintenance.jobs.finished", {
                            date: formatDateTime(job.completed_at),
                          })}
                        </span>
                      ) : null}
                      {job.status === "completed" ? (
                        <span>
                          {t("web.admin.catalogMaintenance.imports.result", {
                            items: itemCount(t, importResult.items_created ?? 0),
                            files: fileCount(t, importResult.files_created ?? 0),
                          })}
                        </span>
                      ) : null}
                    </div>
                    {job.error_message ? (
                      <div className="text-destructive text-xs">{job.error_message}</div>
                    ) : null}
                  </div>
                  <div className="flex items-center gap-2">
                    <Button variant="outline" size="sm" onClick={() => importJobsQuery.refetch()}>
                      <RefreshCw className="mr-1 h-4 w-4" />
                      {t("web.admin.catalogMaintenance.refresh")}
                    </Button>
                  </div>
                </div>
              );
            })
          )}
        </div>
      </div>

      <div className="border-border/70 bg-card/60 rounded-lg border">
        <div className="border-border/70 flex items-center justify-between border-b px-4 py-3">
          <div>
            <h3 className="text-sm font-semibold">
              {t("web.admin.catalogMaintenance.exports.title")}
            </h3>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.catalogMaintenance.exports.body")}
            </p>
          </div>
          {exportJobsQuery.isFetching ? (
            <Badge variant="outline">{t("web.admin.catalogMaintenance.jobs.refreshing")}</Badge>
          ) : (
            <Badge variant="secondary">{exportJobs.length}</Badge>
          )}
        </div>
        <JobPageControls
          query={exportJobsQuery}
          label={t("web.admin.catalogMaintenance.exports.loadOlder")}
        />
        <div className="divide-border/60 divide-y">
          {exportJobs.length === 0 ? (
            <div className="text-muted-foreground px-4 py-5 text-sm">
              {t("web.admin.catalogMaintenance.exports.empty")}
            </div>
          ) : (
            exportJobs.map((job) => {
              const exportResult = job.result_payload as Partial<CatalogSeedExportResult>;
              const scopeLabel = exportScopeLabel(t, job);
              const progressLabel = formatExportProgressLabel(
                job.progress_current,
                job.progress_total,
                job.status,
              );

              return (
                <div
                  key={job.id}
                  className="flex flex-col gap-3 px-4 py-4 lg:flex-row lg:items-center lg:justify-between"
                >
                  <div className="space-y-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge
                        variant={
                          job.status === "failed"
                            ? "destructive"
                            : job.status === "completed"
                              ? "secondary"
                              : "outline"
                        }
                      >
                        {job.status}
                      </Badge>
                      <span className="text-sm font-medium">{scopeLabel}</span>
                      <span className="text-muted-foreground text-xs">
                        {t("web.admin.catalogMaintenance.jobs.requested", {
                          date: formatDateTime(job.requested_at),
                        })}
                      </span>
                    </div>
                    <div className="text-muted-foreground text-sm">
                      {job.message || t("web.admin.catalogMaintenance.exports.jobFallback")}
                    </div>
                    <div className="text-muted-foreground flex flex-wrap gap-4 text-xs">
                      <span>
                        {t("web.admin.catalogMaintenance.jobs.progress", {
                          progress: progressLabel,
                        })}
                      </span>
                      {job.completed_at ? (
                        <span>
                          {t("web.admin.catalogMaintenance.jobs.finished", {
                            date: formatDateTime(job.completed_at),
                          })}
                        </span>
                      ) : null}
                      {exportResult.items_exported ? (
                        <span>
                          {t("web.admin.catalogMaintenance.exports.result", {
                            items: itemCount(t, exportResult.items_exported),
                            files: fileCount(t, exportResult.files_exported ?? 0),
                          })}
                        </span>
                      ) : null}
                    </div>
                    {job.error_message ? (
                      <div className="text-destructive text-xs">{job.error_message}</div>
                    ) : null}
                  </div>
                  <div className="flex items-center gap-2">
                    <Button variant="outline" size="sm" onClick={() => exportJobsQuery.refetch()}>
                      <RefreshCw className="mr-1 h-4 w-4" />
                      {t("web.admin.catalogMaintenance.refresh")}
                    </Button>
                    {job.download_url ? (
                      <Button
                        variant="default"
                        size="sm"
                        onClick={() =>
                          window.open(job.download_url, "_blank", "noopener,noreferrer")
                        }
                      >
                        <Download className="mr-1 h-4 w-4" />
                        {t("web.admin.catalogMaintenance.exports.download")}
                      </Button>
                    ) : null}
                    {job.status === "completed" &&
                    !job.public_url &&
                    job.public_link_supported !== false ? (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => publishMutation.mutate(job.id)}
                        disabled={publishMutation.isPending}
                      >
                        {t("web.admin.catalogMaintenance.exports.createLink")}
                      </Button>
                    ) : null}
                    {job.public_url ? (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={async () => {
                          try {
                            await copyTextToClipboard(job.public_url ?? "");
                            toast.success(t("web.admin.catalogMaintenance.exports.copied"));
                          } catch {
                            toast.error(t("web.admin.catalogMaintenance.exports.copyFailed"));
                          }
                        }}
                      >
                        {t("web.admin.catalogMaintenance.exports.copyUrl")}
                      </Button>
                    ) : null}
                  </div>
                </div>
              );
            })
          )}
        </div>
      </div>
    </div>
  );
}

/** The libraries an export covers: how many, or all of them. */
function exportScopeLabel(t: Translate, job: AdminJob) {
  const exportRequest = job.request_payload as { library_ids?: number[] };
  const count = exportRequest.library_ids?.length ?? 0;
  return count > 0
    ? t("web.admin.catalogMaintenance.exports.libraryCount", { count, count_text: String(count) })
    : t("web.admin.catalogMaintenance.exports.allLibraries");
}

function itemCount(t: Translate, count: number) {
  return t("web.admin.catalogMaintenance.count.items", { count, count_text: String(count) });
}

function fileCount(t: Translate, count: number) {
  return t("web.admin.catalogMaintenance.count.files", { count, count_text: String(count) });
}

function describeExportJob(t: Translate, job: AdminJob) {
  return `${exportScopeLabel(t, job)} • ${formatDateTime(job.requested_at)}`;
}

function describeImportJob(t: Translate, job: AdminJob) {
  const importRequest = job.request_payload as {
    source_label?: string;
    source_key?: string;
  };
  return (
    importRequest.source_label ||
    importRequest.source_key ||
    t("web.admin.catalogMaintenance.imports.seedFallback")
  );
}

function describeImportSource(source: CatalogSeedImportSource) {
  const label = source.last_modified ? formatDateTime(source.last_modified) : source.key;
  return `${label} • ${source.key}`;
}

function getJobProgressPercent(job: AdminJob) {
  if (job.progress_total > 0) {
    return Math.min(100, Math.max(0, (job.progress_current / job.progress_total) * 100));
  }
  if (job.status === "completed") {
    return 100;
  }
  if (job.status === "running") {
    return 12;
  }
  return 4;
}
