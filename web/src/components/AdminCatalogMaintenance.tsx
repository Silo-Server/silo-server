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

export default function AdminCatalogMaintenance() {
  const [importDialogOpen, setImportDialogOpen] = useState(false);
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
          <h2 className="text-lg font-semibold">Catalog Import & Export</h2>
          <p className="text-muted-foreground text-sm">
            Queue full catalog exports, import seeds from uploads or S3, and watch background job
            progress in one place.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => exportMutation.mutate({})}
            disabled={exportMutation.isPending}
          >
            <Download
              className={`mr-1 h-4 w-4 ${exportMutation.isPending ? "animate-pulse" : ""}`}
            />
            Start Export
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
                Import Catalog
              </Button>
            </DialogTrigger>
            <DialogContent className="sm:max-w-2xl">
              <DialogHeader>
                <DialogTitle>Import Catalog Seed</DialogTitle>
              </DialogHeader>
              <form onSubmit={handleImportSubmit} className="space-y-4">
                <div className="space-y-2">
                  <Label>Import Source</Label>
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
                      <SelectItem value="local_path">Local File</SelectItem>
                      <SelectItem value="export_job">Local Export Job</SelectItem>
                      <SelectItem value="bucket_artifact">Bucket Artifact</SelectItem>
                      <SelectItem value="remote_url">Remote URL</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                {importSource === "local_path" ? (
                  <div className="space-y-2">
                    <Label>File Path</Label>
                    <Input
                      value={localPath}
                      onChange={(e) => setLocalPath(e.target.value)}
                      placeholder="/catalog-seeds/my-catalog.json.gz"
                    />
                    {localImportSources.length > 0 && (
                      <>
                        <div className="flex items-center justify-between gap-2">
                          <Label className="text-muted-foreground text-xs">Detected Files</Label>
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
                            Refresh
                          </Button>
                        </div>
                        <Select value="" onValueChange={(value) => setLocalPath(value)}>
                          <SelectTrigger>
                            <SelectValue placeholder="Select a detected file" />
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
                      Enter the absolute path to a <span className="font-mono">.json.gz</span>{" "}
                      catalog seed file on the server, or select a detected file from{" "}
                      <span className="font-mono">/catalog-seeds/</span>.
                    </p>
                  </div>
                ) : importSource === "export_job" ? (
                  <div className="space-y-2">
                    <Label>Completed Export</Label>
                    <Select value={selectedExportJobId} onValueChange={setSelectedExportJobId}>
                      <SelectTrigger>
                        <SelectValue placeholder="Choose a completed export job" />
                      </SelectTrigger>
                      <SelectContent>
                        {completedExportJobs.length === 0 ? (
                          <SelectItem value="__none" disabled>
                            No completed exports yet
                          </SelectItem>
                        ) : (
                          completedExportJobs.map((job) => (
                            <SelectItem key={job.id} value={job.id}>
                              {describeExportJob(job)}
                            </SelectItem>
                          ))
                        )}
                      </SelectContent>
                    </Select>
                    <p className="text-muted-foreground text-xs">
                      Silo will load the selected seed directly from the configured operational S3
                      bucket.
                    </p>
                  </div>
                ) : importSource === "bucket_artifact" ? (
                  <div className="space-y-2">
                    <div className="flex items-center justify-between gap-2">
                      <Label>Detected Bucket Artifacts</Label>
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
                        Refresh
                      </Button>
                    </div>
                    <Select value={selectedArtifactKey} onValueChange={setSelectedArtifactKey}>
                      <SelectTrigger>
                        <SelectValue placeholder="Choose a catalog seed from the bucket" />
                      </SelectTrigger>
                      <SelectContent>
                        {bucketImportSources.length === 0 ? (
                          <SelectItem value="__none" disabled>
                            No catalog seed objects found
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
                      This reads any detected `catalog-seeds/*.json.gz` object in the private
                      internal S3 bucket, including exports from other installs.
                    </p>
                  </div>
                ) : (
                  <div className="space-y-2">
                    <Label>Remote URL</Label>
                    <Input
                      value={remoteURL}
                      onChange={(e) => setRemoteURL(e.target.value)}
                      placeholder="https://example.com/catalog-seeds/export.json.gz"
                    />
                    <p className="text-muted-foreground text-xs">
                      Paste a public <span className="font-mono">.json.gz</span> catalog seed URL.
                      Silo will download it server-side before importing.
                    </p>
                  </div>
                )}
                <div className="space-y-2">
                  <Label>Conflict Mode</Label>
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
                      <SelectItem value="skip_existing">Skip Existing</SelectItem>
                      <SelectItem value="overwrite_existing">Overwrite Existing</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-3">
                  <div className="space-y-1">
                    <Label>Path Rewrites</Label>
                    <p className="text-muted-foreground text-xs">
                      Rewrites use prefix matching. Mapping{" "}
                      <span className="font-mono">/srv/media</span> to{" "}
                      <span className="font-mono">/media</span> rewrites every nested library and
                      file path under that root.
                    </p>
                  </div>
                  {pathRewrites.map((rewrite, index) => (
                    <div key={rewrite.id} className="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
                      <Input
                        value={rewrite.from}
                        onChange={(e) => updateRewrite(index, "from", e.target.value)}
                        placeholder="/srv/media"
                      />
                      <Input
                        value={rewrite.to}
                        onChange={(e) => updateRewrite(index, "to", e.target.value)}
                        placeholder="/media"
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        className="shrink-0"
                        aria-label="Remove path rewrite"
                        onClick={() => removeRewrite(index)}
                      >
                        <Trash2 className="h-4 w-4" aria-hidden="true" />
                      </Button>
                    </div>
                  ))}
                  <Button type="button" variant="outline" size="sm" onClick={addRewrite}>
                    <Plus className="mr-1 h-4 w-4" /> Add Rewrite
                  </Button>
                </div>
                <div className="border-border/60 bg-muted/30 text-muted-foreground rounded-md border p-3 text-xs">
                  Import validates the rewritten library roots before writing anything, so missing
                  or incomplete rewrites will fail fast instead of seeding broken paths.
                </div>
                <Button type="submit" className="w-full" disabled={isImportSubmitDisabled}>
                  {importMutation.isPending ? "Importing..." : "Import Catalog"}
                </Button>
              </form>
            </DialogContent>
          </Dialog>
        </div>
      </div>

      <div className="border-border/70 bg-card/60 rounded-lg border">
        <div className="border-border/70 flex items-center justify-between border-b px-4 py-3">
          <div>
            <h3 className="text-sm font-semibold">Recent Catalog Imports</h3>
            <p className="text-muted-foreground text-xs">
              Imports run in the background so progress stays visible while validation and writes
              are in flight.
            </p>
          </div>
          {importJobsQuery.isFetching ? (
            <Badge variant="outline">Refreshing</Badge>
          ) : (
            <Badge variant="secondary">{importJobs.length}</Badge>
          )}
        </div>
        <div className="divide-border/60 divide-y">
          {importJobs.length === 0 ? (
            <div className="text-muted-foreground px-4 py-5 text-sm">
              No catalog import jobs yet.
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
                      <span className="text-sm font-medium">{describeImportJob(job)}</span>
                      <span className="text-muted-foreground text-xs">
                        requested {new Date(job.requested_at).toLocaleString()}
                      </span>
                    </div>
                    <div className="text-muted-foreground text-sm">
                      {job.message || "Catalog import job"}
                    </div>
                    <div className="bg-muted h-2 overflow-hidden rounded-full">
                      <div
                        className="bg-primary h-full rounded-full transition-[width] duration-300"
                        style={{ width: `${progressPercent}%` }}
                      />
                    </div>
                    <div className="text-muted-foreground flex flex-wrap gap-4 text-xs">
                      <span>Progress: {formatJobProgress(job)}</span>
                      {job.completed_at ? (
                        <span>Finished: {new Date(job.completed_at).toLocaleString()}</span>
                      ) : null}
                      {job.status === "completed" ? (
                        <span>
                          Imported {importResult.items_created ?? 0} items and{" "}
                          {importResult.files_created ?? 0} files
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
                      Refresh
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
            <h3 className="text-sm font-semibold">Recent Catalog Exports</h3>
            <p className="text-muted-foreground text-xs">
              Export jobs run in the background and upload finished seeds to the private internal S3
              bucket.
            </p>
          </div>
          {exportJobsQuery.isFetching ? (
            <Badge variant="outline">Refreshing</Badge>
          ) : (
            <Badge variant="secondary">{exportJobs.length}</Badge>
          )}
        </div>
        <div className="divide-border/60 divide-y">
          {exportJobs.length === 0 ? (
            <div className="text-muted-foreground px-4 py-5 text-sm">
              No catalog export jobs yet.
            </div>
          ) : (
            exportJobs.map((job) => {
              const exportRequest = job.request_payload as { library_ids?: number[] };
              const exportResult = job.result_payload as Partial<CatalogSeedExportResult>;
              const scopeLabel =
                exportRequest.library_ids && exportRequest.library_ids.length > 0
                  ? `${exportRequest.library_ids.length} librar${exportRequest.library_ids.length === 1 ? "y" : "ies"}`
                  : "All libraries";
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
                        requested {new Date(job.requested_at).toLocaleString()}
                      </span>
                    </div>
                    <div className="text-muted-foreground text-sm">
                      {job.message || "Catalog export job"}
                    </div>
                    <div className="text-muted-foreground flex flex-wrap gap-4 text-xs">
                      <span>Progress: {progressLabel}</span>
                      {job.completed_at ? (
                        <span>Finished: {new Date(job.completed_at).toLocaleString()}</span>
                      ) : null}
                      {exportResult.items_exported ? (
                        <span>
                          Exported {exportResult.items_exported} items and{" "}
                          {exportResult.files_exported ?? 0} files
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
                      Refresh
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
                        Download
                      </Button>
                    ) : null}
                    {job.status === "completed" && !job.public_url ? (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => publishMutation.mutate(job.id)}
                        disabled={publishMutation.isPending}
                      >
                        Publish
                      </Button>
                    ) : null}
                    {job.public_url ? (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={async () => {
                          await navigator.clipboard.writeText(job.public_url ?? "");
                        }}
                      >
                        Copy URL
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

function describeExportJob(job: AdminJob) {
  const exportRequest = job.request_payload as { library_ids?: number[] };
  const scopeLabel =
    exportRequest.library_ids && exportRequest.library_ids.length > 0
      ? `${exportRequest.library_ids.length} librar${exportRequest.library_ids.length === 1 ? "y" : "ies"}`
      : "All libraries";
  return `${scopeLabel} • ${new Date(job.requested_at).toLocaleString()}`;
}

function describeImportJob(job: AdminJob) {
  const importRequest = job.request_payload as {
    source_label?: string;
    source_key?: string;
  };
  return importRequest.source_label || importRequest.source_key || "Catalog seed";
}

function describeImportSource(source: CatalogSeedImportSource) {
  const label = source.last_modified ? new Date(source.last_modified).toLocaleString() : source.key;
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
