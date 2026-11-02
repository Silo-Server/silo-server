import { useId, useState } from "react";
import { Link } from "react-router";
import type {
  AdminDownloadStorage,
  AdminDownloadStorageLocation,
} from "@/api/v2/adminDownloadStorage";
import type { StreamNode, UpdateNodeRequest } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioCardItem, RadioGroup } from "@/components/ui/radio-group";
import { useAdminNodes, useUpdateNode } from "@/hooks/queries/admin/nodes";
import { formatStorageBytes, preparedBytes } from "./downloadStoragePresentation";

const BYTES_PER_GB = 1e9;

type BudgetMode = "default" | "custom" | "none";

/**
 * Where one location keeps prepared files and how much it may keep. A node's
 * directory and budget are its own overrides; the server's are the cluster
 * settings, edited on the Downloads settings page.
 */
export default function EditLocationDialog({
  location,
  storage,
  onClose,
}: {
  location: AdminDownloadStorageLocation | null;
  storage: AdminDownloadStorage;
  onClose: () => void;
}) {
  return (
    <Dialog
      open={location !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="sm:max-w-lg">
        {location?.kind === "node" ? (
          <NodeLocationForm
            key={location.key}
            location={location}
            storage={storage}
            onClose={onClose}
          />
        ) : location ? (
          <ServerLocationInfo location={location} storage={storage} onClose={onClose} />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function ServerLocationInfo({
  location,
  storage,
  onClose,
}: {
  location: AdminDownloadStorageLocation;
  storage: AdminDownloadStorage;
  onClose: () => void;
}) {
  return (
    <>
      <DialogHeader>
        <DialogTitle>Edit location · Server</DialogTitle>
        <DialogDescription>
          The server's prepared-file directory and the default budget are server settings.
        </DialogDescription>
      </DialogHeader>
      <dl className="space-y-3 text-sm">
        <div>
          <dt className="text-muted-foreground text-xs">Prepared file directory</dt>
          <dd className="font-mono text-xs break-all">{location.dir}</dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">
            Storage budget at every location without its own
          </dt>
          <dd>
            {storage.default_budget_bytes > 0
              ? formatStorageBytes(storage.default_budget_bytes)
              : "No budget"}
          </dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">Keep cached files for</dt>
          <dd>{storage.cache_hours} hours after their last use</dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">Disk ceiling</dt>
          <dd>{storage.disk_ceiling_percent}%</dd>
        </div>
      </dl>
      <DialogFooter>
        <Button variant="ghost" onClick={onClose}>
          Close
        </Button>
        <Button asChild>
          <Link to="/admin/settings/downloads">Open Downloads settings</Link>
        </Button>
      </DialogFooter>
    </>
  );
}

function initialBudgetMode(node: StreamNode | undefined): BudgetMode {
  const override = node?.download_artifact_max_bytes_override;
  if (override == null) return "default";
  return override === 0 ? "none" : "custom";
}

/** The node's budget override: null follows the default, 0 means no budget. */
function budgetOverride(mode: BudgetMode, gb: number): number | null {
  if (mode === "default") return null;
  if (mode === "none") return 0;
  return Math.round(gb * BYTES_PER_GB);
}

function NodeLocationForm({
  location,
  storage,
  onClose,
}: {
  location: AdminDownloadStorageLocation;
  storage: AdminDownloadStorage;
  onClose: () => void;
}) {
  const nodes = useAdminNodes();
  const node = nodes.data?.find((n) => String(n.id) === location.node_id);
  if (!node) {
    return (
      <>
        <DialogHeader>
          <DialogTitle>Edit location · {location.name}</DialogTitle>
          <DialogDescription>
            {nodes.isLoading
              ? "Loading the node…"
              : "This node could not be read. Reload and try again."}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
        </DialogFooter>
      </>
    );
  }
  return (
    <NodeLocationFields
      key={node.config_etag}
      node={node}
      location={location}
      storage={storage}
      onClose={onClose}
    />
  );
}

function NodeLocationFields({
  node,
  location,
  storage,
  onClose,
}: {
  node: StreamNode;
  location: AdminDownloadStorageLocation;
  storage: AdminDownloadStorage;
  onClose: () => void;
}) {
  const update = useUpdateNode();
  const dirId = useId();
  const budgetId = useId();
  const currentDir = node.download_artifact_dir_override ?? "";
  const currentBudget = node.download_artifact_max_bytes_override ?? null;
  const [dir, setDir] = useState(currentDir);
  const [mode, setMode] = useState<BudgetMode>(initialBudgetMode(node));
  // Unrounded, so reopening the dialog shows (and keeps) a 1.5 GB budget.
  const [gb, setGb] = useState(
    currentBudget && currentBudget > 0 ? String(currentBudget / BYTES_PER_GB) : "",
  );

  const trimmed = dir.trim();
  const dirError = trimmed !== "" && !trimmed.startsWith("/") ? "Use an absolute path." : null;
  const gbValue = Number(gb);
  const gbError =
    mode === "custom" && (!gb.trim() || !Number.isFinite(gbValue) || gbValue <= 0)
      ? "Enter a budget in GB."
      : null;
  const dirChanged = trimmed !== currentDir;
  // A blank override inherits download.artifact_dir when it is set, which the
  // server location reports as its own directory.
  const server = storage.locations.find((l) => l.kind === "server");
  const clusterDir = server?.dir_source === "setting" ? server.dir : undefined;
  const indexed = preparedBytes(location);
  const stored = location.usage ? location.usage.bytes : indexed;

  function save() {
    if (dirError || gbError) return;
    // Send only what changed: an omitted override is left as it is.
    const body: UpdateNodeRequest = {};
    if (dirChanged) body.download_artifact_dir_override = trimmed === "" ? null : trimmed;
    const budget = budgetOverride(mode, gbValue);
    if (budget !== currentBudget) body.download_artifact_max_bytes_override = budget;
    if (Object.keys(body).length === 0) {
      onClose();
      return;
    }
    update.mutate({ node, body }, { onSuccess: onClose });
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>Edit location · {location.name}</DialogTitle>
        <DialogDescription>
          Where this node keeps prepared download files, and how much it may keep.
        </DialogDescription>
      </DialogHeader>
      <div className="space-y-5">
        <div className="space-y-2">
          <Label htmlFor={dirId}>Prepared file directory</Label>
          <Input
            id={dirId}
            value={dir}
            onChange={(event) => setDir(event.target.value)}
            placeholder={clusterDir ?? "download-artifacts inside the transcode directory"}
            className="font-mono text-xs"
            aria-invalid={dirError ? true : undefined}
          />
          {dirError ? <p className="text-destructive text-xs">{dirError}</p> : null}
          <p className="text-muted-foreground text-xs">
            {clusterDir ? (
              <>
                Leave blank to use <code className="font-mono">{clusterDir}</code>, the directory in
                Settings → Downloads; it must exist on this node.
              </>
            ) : (
              <>
                Leave blank to use <code className="font-mono">download-artifacts</code> inside this
                node's transcode directory.
              </>
            )}{" "}
            A separate disk keeps downloads from competing with live transcodes.
          </p>
          {location.usage?.shares_scratch && !dirChanged ? (
            <p className="text-warning text-xs">
              The current directory is on the same disk as transcode scratch.
            </p>
          ) : null}
        </div>
        {dirChanged ? (
          <div className="border-warning/30 bg-warning/8 rounded-xl border p-3 text-xs" role="note">
            Changing the directory doesn't move files. Of the {formatStorageBytes(stored)} here,{" "}
            <strong>{formatStorageBytes(location.in_use_bytes)} in use</strong> is prepared again in
            the new directory and{" "}
            <strong>{formatStorageBytes(location.cached_bytes)} cached</strong> is dropped. Old
            files stay in{" "}
            {location.dir ? <code className="font-mono">{location.dir}</code> : "the old directory"}{" "}
            until you delete them. {location.name} picks up the change when it restarts.
          </div>
        ) : null}
        <div className="space-y-2">
          <Label id={budgetId}>Storage budget</Label>
          <RadioGroup
            aria-labelledby={budgetId}
            value={mode}
            onValueChange={(value) => setMode(value as BudgetMode)}
            className="grid-cols-1 gap-2 sm:grid-cols-3"
          >
            <RadioCardItem
              value="default"
              label="Default"
              hint={
                storage.default_budget_bytes > 0
                  ? formatStorageBytes(storage.default_budget_bytes)
                  : "No budget"
              }
            />
            <RadioCardItem value="custom" label="Custom" />
            <RadioCardItem value="none" label="None" hint="Disk ceiling only" />
          </RadioGroup>
          {mode === "custom" ? (
            <div className="flex items-center gap-2">
              <Input
                aria-label="Budget in GB"
                inputMode="decimal"
                value={gb}
                onChange={(event) => setGb(event.target.value)}
                className="w-32"
                aria-invalid={gbError ? true : undefined}
              />
              <span className="text-muted-foreground text-sm">GB</span>
            </div>
          ) : null}
          {gbError ? <p className="text-destructive text-xs">{gbError}</p> : null}
          <p className="text-muted-foreground text-xs">
            {formatStorageBytes(indexed)} stored now. Over budget, Silo deletes cached files early,
            least recently used first. Files a device is still waiting on stay.
          </p>
        </div>
        <p className="text-muted-foreground text-xs">
          Disk ceiling: {storage.disk_ceiling_percent}%, set for all locations in{" "}
          <Link to="/admin/settings/downloads" className="underline underline-offset-3">
            Settings → Downloads
          </Link>
          .
        </p>
      </div>
      <DialogFooter>
        <Button variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button onClick={save} disabled={!!dirError || !!gbError || update.isPending}>
          {update.isPending ? "Saving…" : "Save"}
        </Button>
      </DialogFooter>
    </>
  );
}
