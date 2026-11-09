import { useId, useState, type ReactNode } from "react";
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
import { useT } from "@/i18n";
import { countValues, formatStorageBytes, preparedBytes } from "./downloadStoragePresentation";

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
  const t = useT();
  return (
    <>
      <DialogHeader>
        <DialogTitle>{t("web.admin.downloads.location.serverTitle")}</DialogTitle>
        <DialogDescription>{t("web.admin.downloads.location.serverDescription")}</DialogDescription>
      </DialogHeader>
      <dl className="space-y-3 text-sm">
        <div>
          <dt className="text-muted-foreground text-xs">
            {t("web.admin.downloads.location.directory")}
          </dt>
          <dd className="font-mono text-xs break-all">{location.dir}</dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">
            {t("web.admin.downloads.location.defaultBudget")}
          </dt>
          <dd>
            {storage.default_budget_bytes > 0
              ? formatStorageBytes(storage.default_budget_bytes)
              : t("web.admin.downloads.location.noBudget")}
          </dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">
            {t("web.admin.downloads.location.keepCached")}
          </dt>
          <dd>
            {t("web.admin.downloads.location.keepCachedValue", countValues(storage.cache_hours))}
          </dd>
        </div>
        <div>
          <dt className="text-muted-foreground text-xs">
            {t("web.admin.downloads.location.diskCeiling")}
          </dt>
          <dd>
            {t("web.admin.downloads.location.percent", {
              percent: String(storage.disk_ceiling_percent),
            })}
          </dd>
        </div>
      </dl>
      <DialogFooter>
        <Button variant="ghost" onClick={onClose}>
          {t("web.admin.downloads.location.close")}
        </Button>
        <Button asChild>
          <Link to="/admin/settings/downloads">
            {t("web.admin.downloads.location.openSettings")}
          </Link>
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
  const t = useT();
  const nodes = useAdminNodes();
  const node = nodes.data?.find((n) => String(n.id) === location.node_id);
  if (!node) {
    return (
      <>
        <DialogHeader>
          <DialogTitle>
            {t("web.admin.downloads.location.nodeTitle", { name: location.name })}
          </DialogTitle>
          <DialogDescription>
            {nodes.isLoading
              ? t("web.admin.downloads.location.loadingNode")
              : t("web.admin.downloads.location.nodeError")}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t("web.admin.downloads.location.close")}
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
  const t = useT();
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
  const dirError =
    trimmed !== "" && !trimmed.startsWith("/") ? t("web.admin.downloads.location.dirError") : null;
  const gbValue = Number(gb);
  const gbError =
    mode === "custom" && (!gb.trim() || !Number.isFinite(gbValue) || gbValue <= 0)
      ? t("web.admin.downloads.location.gbError")
      : null;
  const dirChanged = trimmed !== currentDir;
  // A blank override inherits download.artifact_dir when it is set, which the
  // server location reports as its own directory.
  const server = storage.locations.find((l) => l.kind === "server");
  const clusterDir = server?.dir_source === "setting" ? server.dir : undefined;
  const indexed = preparedBytes(location);
  const stored = location.usage ? location.usage.bytes : indexed;
  const code = (chunks: ReactNode[]) => <code className="font-mono">{chunks}</code>;
  const dirChange = {
    stored: formatStorageBytes(stored),
    in_use: formatStorageBytes(location.in_use_bytes),
    cached: formatStorageBytes(location.cached_bytes),
    name: location.name,
    b: (chunks: ReactNode[]) => <strong>{chunks}</strong>,
  };

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
        <DialogTitle>
          {t("web.admin.downloads.location.nodeTitle", { name: location.name })}
        </DialogTitle>
        <DialogDescription>{t("web.admin.downloads.location.nodeDescription")}</DialogDescription>
      </DialogHeader>
      <div className="space-y-5">
        <div className="space-y-2">
          <Label htmlFor={dirId}>{t("web.admin.downloads.location.directory")}</Label>
          <Input
            id={dirId}
            value={dir}
            onChange={(event) => setDir(event.target.value)}
            placeholder={clusterDir ?? t("web.admin.downloads.location.dirPlaceholder")}
            className="font-mono text-xs"
            aria-invalid={dirError ? true : undefined}
          />
          {dirError ? <p className="text-destructive text-xs">{dirError}</p> : null}
          <p className="text-muted-foreground text-xs">
            {clusterDir
              ? t("web.admin.downloads.location.dirHelpCluster", { dir: clusterDir, code })
              : t("web.admin.downloads.location.dirHelpDefault", { code })}
          </p>
          {location.usage?.shares_scratch && !dirChanged ? (
            <p className="text-warning text-xs">
              {t("web.admin.downloads.location.sharesScratch")}
            </p>
          ) : null}
        </div>
        {dirChanged ? (
          <div className="border-warning/30 bg-warning/8 rounded-xl border p-3 text-xs" role="note">
            {location.dir
              ? t("web.admin.downloads.location.dirChangeWithDir", {
                  ...dirChange,
                  dir: location.dir,
                  code,
                })
              : t("web.admin.downloads.location.dirChangeNoDir", dirChange)}
          </div>
        ) : null}
        <div className="space-y-2">
          <Label id={budgetId}>{t("web.admin.downloads.location.budget")}</Label>
          <RadioGroup
            aria-labelledby={budgetId}
            value={mode}
            onValueChange={(value) => setMode(value as BudgetMode)}
            className="grid-cols-1 gap-2 sm:grid-cols-3"
          >
            <RadioCardItem
              value="default"
              label={t("web.admin.downloads.location.budgetDefault")}
              hint={
                storage.default_budget_bytes > 0
                  ? formatStorageBytes(storage.default_budget_bytes)
                  : t("web.admin.downloads.location.noBudget")
              }
            />
            <RadioCardItem value="custom" label={t("web.admin.downloads.location.budgetCustom")} />
            <RadioCardItem
              value="none"
              label={t("web.admin.downloads.location.budgetNone")}
              hint={t("web.admin.downloads.location.budgetNoneHint")}
            />
          </RadioGroup>
          {mode === "custom" ? (
            <div className="flex items-center gap-2">
              <Input
                aria-label={t("web.admin.downloads.location.budgetInput")}
                inputMode="decimal"
                value={gb}
                onChange={(event) => setGb(event.target.value)}
                className="w-32"
                aria-invalid={gbError ? true : undefined}
              />
              <span className="text-muted-foreground text-sm">
                {t("web.admin.downloads.location.gb")}
              </span>
            </div>
          ) : null}
          {gbError ? <p className="text-destructive text-xs">{gbError}</p> : null}
          <p className="text-muted-foreground text-xs">
            {t("web.admin.downloads.location.budgetHelp", { size: formatStorageBytes(indexed) })}
          </p>
        </div>
        <p className="text-muted-foreground text-xs">
          {t("web.admin.downloads.location.ceilingNote", {
            percent: String(storage.disk_ceiling_percent),
            link: (chunks) => (
              <Link to="/admin/settings/downloads" className="underline underline-offset-3">
                {chunks}
              </Link>
            ),
          })}
        </p>
      </div>
      <DialogFooter>
        <Button variant="ghost" onClick={onClose}>
          {t("common.cancel")}
        </Button>
        <Button onClick={save} disabled={!!dirError || !!gbError || update.isPending}>
          {update.isPending ? t("web.admin.downloads.location.saving") : t("common.save")}
        </Button>
      </DialogFooter>
    </>
  );
}
