import { useMemo, useState } from "react";
import { HardDrive, RefreshCw, ShieldCheck, TriangleAlert } from "lucide-react";
import type { AdminJob, ArtworkPurgeRequest } from "@/api/types";
import {
  useArtworkStorage,
  useImportArtworkStorage,
  usePurgeArtworkStorage,
  useRebuildArtworkStorage,
  useRefreshArtworkStorage,
} from "@/hooks/queries/admin/artworkStorage";
import { useAdminLibraries, useAllAdminJobs } from "@/hooks/queries/admin/libraries";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

export function formatArtworkBytes(value: number | undefined) {
  if (value === undefined) return "Not reported";
  if (value < 1024) return `${value} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let amount = value;
  let unit = -1;
  do {
    amount /= 1024;
    unit++;
  } while (amount >= 1024 && unit < units.length - 1);
  return `${amount.toFixed(amount >= 10 ? 1 : 2)} ${units[unit]}`;
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-muted/30 rounded-md p-3">
      <div className="text-muted-foreground text-xs">{label}</div>
      <div className="mt-1 text-sm font-semibold">{value}</div>
    </div>
  );
}

function formatSnapshotAge(snapshotAt: string | undefined): string {
  if (!snapshotAt) return "Never refreshed";
  return `${Math.max(0, Math.round((Date.now() - Date.parse(snapshotAt)) / 60_000))} minutes ago`;
}

function purgeResult(job?: AdminJob) {
  if (!job || job.status !== "completed") return null;
  return job.result_payload as Record<string, number>;
}

export default function AdminArtworkStorage() {
  const storage = useArtworkStorage();
  const refresh = useRefreshArtworkStorage();
  const importStore = useImportArtworkStorage();
  const rebuild = useRebuildArtworkStorage();
  const purge = usePurgeArtworkStorage();
  const libraries = useAdminLibraries().data ?? [];
  const jobs = useAllAdminJobs(50).data ?? [];
  const [dialogOpen, setDialogOpen] = useState(false);
  const [rebuildDialogOpen, setRebuildDialogOpen] = useState(false);
  const [scope, setScope] = useState("server");
  const [previewJobID, setPreviewJobID] = useState("");
  const previewJob = jobs.find((job) => job.id === previewJobID);
  const preview = purgeResult(previewJob);
  const request = useMemo<ArtworkPurgeRequest>(() => {
    const libraryID = scope === "server" ? undefined : Number(scope);
    return {
      scope: libraryID ? { library_id: libraryID } : { server: true },
      mode: "safe_materialized",
      dry_run: true,
    };
  }, [scope]);

  if (storage.isLoading)
    return <div className="text-muted-foreground text-sm">Loading artwork storage…</div>;
  if (storage.isError || !storage.data)
    return (
      <div role="alert" className="text-destructive text-sm">
        Artwork storage status is unavailable.
      </div>
    );

  const data = storage.data;
  const storeHealth = data.store_health ?? "unknown";
  const libraryNames = new Map(libraries.map((library) => [library.id, library.name]));
  const snapshotAge = formatSnapshotAge(data.snapshot_at);
  const canRebuild =
    data.backend === "local" && ["unavailable", "wrong_mount"].includes(storeHealth);

  return (
    <section
      className="border-border/70 bg-card/60 space-y-4 rounded-lg border p-4"
      aria-labelledby="artwork-storage-title"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="artwork-storage-title" className="flex items-center gap-2 text-lg font-semibold">
            <HardDrive className="h-5 w-5" /> Artwork Storage
          </h2>
          <p className="text-muted-foreground mt-1 text-sm">
            {data.backend === "local" ? "Local artwork storage" : "Shared S3 artwork storage"}
            {data.resolved_path ? ` · ${data.resolved_path}` : ""}
          </p>
        </div>
        <div className="flex gap-2">
          <Badge variant={storeHealth === "healthy" ? "secondary" : "destructive"}>
            {storeHealth.replace(/_/g, " ")}
          </Badge>
          <Badge variant={data.complete ? "secondary" : "outline"}>
            {data.complete ? "Complete" : "Incomplete"}
          </Badge>
        </div>
      </div>

      {(data.coverage_limited || !data.complete) && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-sm">
          <strong>{data.coverage_limited ? "Coverage limited." : "Inventory incomplete."}</strong>{" "}
          {data.coverage_limit_reason ||
            "Known bytes are exact; unknown or drifting revisions are reported separately."}
        </div>
      )}
      {data.unsupported_topology_warnings?.map((warning) => (
        <div key={warning} className="border-border rounded-md border p-3 text-sm">
          {warning}
        </div>
      ))}

      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        <Metric label="Unique physical" value={formatArtworkBytes(data.total.physical_bytes)} />
        <Metric label="Free space" value={formatArtworkBytes(data.free_space_bytes)} />
        <Metric label="Protected" value={formatArtworkBytes(data.total.protected_bytes)} />
        <Metric label="Pending GC" value={formatArtworkBytes(data.total.pending_gc_bytes)} />
        <Metric label="Missing" value={formatArtworkBytes(data.total.missing_bytes)} />
        <Metric
          label="Repair pending"
          value={formatArtworkBytes(data.total.repair_pending_bytes)}
        />
        <Metric label="Reclaimable" value={formatArtworkBytes(data.total.reclaimable_bytes)} />
        <Metric
          label="Retained unverifiable"
          value={formatArtworkBytes(data.seed.retained_unverifiable_bytes)}
        />
        <Metric label="Revisions" value={data.total.revision_count.toLocaleString()} />
        <Metric label="Objects" value={data.total.object_count.toLocaleString()} />
        <Metric label="Snapshot" value={snapshotAge} />
      </div>

      {(storeHealth !== "healthy" || (data.total.missing_revision_count ?? 0) > 0) && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-sm">
          <strong>Artwork recovery status.</strong>{" "}
          {(data.total.repairing_revision_count ?? 0).toLocaleString()} revision(s) queued or
          repairing; {(data.total.protected_loss_count ?? 0).toLocaleString()} protected
          selection(s) need restoration or replacement.
        </div>
      )}

      <div className="flex flex-wrap gap-2">
        {canRebuild && (
          <Button size="sm" variant="destructive" onClick={() => setRebuildDialogOpen(true)}>
            <TriangleAlert className="mr-2 h-4 w-4" /> Rebuild artwork store
          </Button>
        )}
        <Button
          size="sm"
          variant="outline"
          onClick={() => refresh.mutate({})}
          disabled={refresh.isPending}
        >
          <RefreshCw className={`mr-2 h-4 w-4 ${refresh.isPending ? "animate-spin" : ""}`} />{" "}
          Refresh storage usage
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={() => importStore.mutate({})}
          disabled={importStore.isPending}
        >
          Import copied portable store
        </Button>
        <Button size="sm" onClick={() => setDialogOpen(true)}>
          <ShieldCheck className="mr-2 h-4 w-4" /> Free artwork storage
        </Button>
      </div>

      <Dialog open={rebuildDialogOpen} onOpenChange={setRebuildDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Rebuild the empty artwork store?</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <p className="text-muted-foreground text-sm">
              This creates a new local store generation and queues recovery of reconstructible
              artwork. Continue only when the previous store root is gone or the replacement root is
              empty.
            </p>
            <Button
              variant="destructive"
              onClick={() =>
                rebuild.mutate(undefined, { onSuccess: () => setRebuildDialogOpen(false) })
              }
              disabled={rebuild.isPending}
            >
              Confirm rebuild artwork store
            </Button>
          </div>
        </DialogContent>
      </Dialog>

      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead className="text-muted-foreground text-xs">
            <tr>
              <th className="py-2">Library</th>
              <th>Referenced</th>
              <th>Exclusive</th>
              <th>Shared (non-additive)</th>
              <th>Reclaimable</th>
              <th>Recovery</th>
            </tr>
          </thead>
          <tbody>
            {(data.libraries ?? []).map((library) => (
              <tr key={library.library_id} className="border-border/50 border-t">
                <td className="py-2">
                  {libraryNames.get(library.library_id) ?? `Library #${library.library_id}`}
                </td>
                <td>{formatArtworkBytes(library.referenced_bytes)}</td>
                <td>{formatArtworkBytes(library.exclusive_bytes)}</td>
                <td>{formatArtworkBytes(library.shared_bytes)}</td>
                <td>{formatArtworkBytes(library.reclaimable_bytes)}</td>
                <td>
                  {library.repairing_revisions ?? 0} repairing · {library.protected_losses ?? 0}{" "}
                  protected loss
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <p className="text-muted-foreground mt-2 text-xs">
          Shared bytes overlap other rows and must not be summed. Server-scoped artwork is reported
          separately by the API.
        </p>
      </div>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Free artwork storage safely</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>Scope</Label>
              <Select
                value={scope}
                onValueChange={(value) => {
                  setScope(value);
                  setPreviewJobID("");
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="server">Entire server</SelectItem>
                  {libraries.map((library) => (
                    <SelectItem key={library.id} value={String(library.id)}>
                      {library.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {!preview && (
              <Button
                onClick={() =>
                  purge.mutate(request, { onSuccess: (job) => setPreviewJobID(job.id) })
                }
                disabled={
                  purge.isPending ||
                  previewJob?.status === "queued" ||
                  previewJob?.status === "running"
                }
              >
                Preview safe plan
              </Button>
            )}
            {previewJob && !preview && (
              <p className="text-muted-foreground text-sm">
                Plan job: {previewJob.status}. Progress appears in Job History.
              </p>
            )}
            {preview && (
              <div className="space-y-3">
                <div className="grid grid-cols-2 gap-2">
                  <Metric
                    label="References transitioned"
                    value={String(preview.transitioned ?? 0)}
                  />
                  <Metric
                    label="Protected skipped"
                    value={String(preview.protected_skipped ?? 0)}
                  />
                  <Metric label="Shared retained" value={String(preview.shared_retained ?? 0)} />
                  <Metric
                    label="Reclaimable"
                    value={formatArtworkBytes(preview.reclaimable_bytes ?? 0)}
                  />
                </div>
                <p className="text-muted-foreground text-xs">
                  The preview changed neither catalog references nor stored objects.
                </p>
                <Button
                  variant="destructive"
                  onClick={() =>
                    purge.mutate(
                      { ...request, dry_run: false },
                      { onSuccess: () => setDialogOpen(false) },
                    )
                  }
                  disabled={purge.isPending}
                >
                  Confirm free artwork storage
                </Button>
              </div>
            )}
          </div>
        </DialogContent>
      </Dialog>
    </section>
  );
}
