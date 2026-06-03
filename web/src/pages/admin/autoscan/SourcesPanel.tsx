import { useId, useState } from "react";
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Clock,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";
import { Link } from "react-router";
import type {
  AutoscanPathRewrite,
  AutoscanRewriteSuggestions,
  AutoscanSource,
  AutoscanSourceInput,
} from "@/api/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  useAutoscanConnections,
  useAutoscanRewriteSuggestions,
  useAutoscanSettings,
  useAutoscanSources,
  useAvailableScanSources,
  useCreateAutoscanSource,
  useDeleteAutoscanSource,
  useUpdateAutoscanSource,
} from "@/hooks/queries/useAutoscan";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Derive a human-readable label from installation_id + capability_id. */
function sourceLabel(source: AutoscanSource): string {
  return `${source.capability_id} (plugin #${source.installation_id})`;
}

function formatRelativeTime(isoString: string | null): string {
  if (!isoString) return "Never";
  const date = new Date(isoString);
  const diffMs = Date.now() - date.getTime();
  const diffMin = Math.floor(diffMs / 60_000);
  if (diffMin < 1) return "Just now";
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  const diffDays = Math.floor(diffHr / 24);
  return `${diffDays}d ago`;
}

// ---------------------------------------------------------------------------
// Row state for per-row edits (connection + interval + rewrites)
// ---------------------------------------------------------------------------

interface RowEdit {
  connectionId: string; // "" means no connection
  intervalStr: string; // "" means use default
  rewrites: AutoscanPathRewrite[];
}

function sourceToRowEdit(source: AutoscanSource): RowEdit {
  return {
    connectionId: source.connection_id ?? "",
    intervalStr: source.poll_interval_seconds != null ? String(source.poll_interval_seconds) : "",
    rewrites: source.path_rewrites.map((r) => ({ ...r })),
  };
}

// ---------------------------------------------------------------------------
// RewriteEditor — expandable section inside a SourceRow
// ---------------------------------------------------------------------------

function RewriteEditor({
  sourceId,
  hasConnection,
  rewrites,
  onChange,
  onSave,
  isSaving,
}: {
  sourceId: string;
  hasConnection: boolean;
  rewrites: AutoscanPathRewrite[];
  onChange: (next: AutoscanPathRewrite[]) => void;
  onSave: (rewrites?: AutoscanPathRewrite[]) => void;
  isSaving: boolean;
}) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const [rewriteError, setRewriteError] = useState<string | null>(null);

  const suggest = useAutoscanRewriteSuggestions();
  const [preview, setPreview] = useState<AutoscanRewriteSuggestions | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  function updateRewrite(index: number, patch: Partial<AutoscanPathRewrite>) {
    setRewriteError(null);
    onChange(rewrites.map((r, i) => (i === index ? { ...r, ...patch } : r)));
  }

  function addRewrite() {
    onChange([...rewrites, { from: "", to: "" }]);
    setOpen(true);
  }

  function removeRewrite(index: number) {
    setRewriteError(null);
    onChange(rewrites.filter((_, i) => i !== index));
  }

  async function handleSync() {
    const s = await suggest.mutateAsync(sourceId);
    setPreview(s);
    setSelected(new Set((s.proposed ?? []).map((p) => p.from)));
    setOpen(true);
  }

  function toggleSelected(from: string) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(from)) {
        next.delete(from);
      } else {
        next.add(from);
      }
      return next;
    });
  }

  /** Merge the checked proposed rewrites into the list (dedupe by `from`) and save. */
  function applySelected() {
    if (!preview) return;
    const existingFroms = new Set(rewrites.map((r) => r.from));
    const additions = preview.proposed
      .filter((p) => selected.has(p.from) && !existingFroms.has(p.from))
      .map((p) => ({ from: p.from, to: p.to }));
    const merged = [...rewrites, ...additions];
    onChange(merged);
    setPreview(null);
    setOpen(true);
    // Persist immediately via the normal full-state source PUT.
    onSave(merged);
  }

  function handleSave() {
    // Validate: any non-empty row must have both from and to filled in.
    const hasIncomplete = rewrites.some(
      (r) =>
        (r.from.trim().length > 0 && r.to.trim().length === 0) ||
        (r.from.trim().length === 0 && r.to.trim().length > 0),
    );
    if (hasIncomplete) {
      setRewriteError("Each rewrite must have both a 'from' and a 'to' path.");
      return;
    }
    setRewriteError(null);
    onSave();
  }

  const syncDisabled = !hasConnection || suggest.isPending;

  return (
    <div className="border-border mt-3 rounded-md border">
      <button
        type="button"
        className="flex w-full items-center gap-2 px-3 py-2 text-left"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-controls={panelId}
      >
        {open ? (
          <ChevronDown className="text-muted-foreground size-3.5 shrink-0" />
        ) : (
          <ChevronRight className="text-muted-foreground size-3.5 shrink-0" />
        )}
        <span className="text-sm font-medium">Path rewrites</span>
        {rewrites.length > 0 && (
          <Badge variant="secondary" className="text-xs">
            {rewrites.length}
          </Badge>
        )}
      </button>

      {open && (
        <div id={panelId} className="space-y-3 px-3 pb-3" role="region" aria-label="Path rewrites">
          {rewrites.length === 0 ? (
            <p className="text-muted-foreground text-xs">
              No path rewrites. Map remote paths (from the scan source) to local library paths.
            </p>
          ) : (
            <div className="space-y-2">
              {rewrites.map((rewrite, index) => (
                <div key={index} className="flex items-end gap-2">
                  <div className="flex-1 space-y-1">
                    <Label className="text-muted-foreground text-xs">From</Label>
                    <Input
                      value={rewrite.from}
                      onChange={(e) => updateRewrite(index, { from: e.target.value })}
                      placeholder="/remote/media"
                      className="h-8 text-sm"
                      aria-label={`Rewrite ${index + 1} from path`}
                    />
                  </div>
                  <div className="flex-1 space-y-1">
                    <Label className="text-muted-foreground text-xs">To</Label>
                    <Input
                      value={rewrite.to}
                      onChange={(e) => updateRewrite(index, { to: e.target.value })}
                      placeholder="/media"
                      className="h-8 text-sm"
                      aria-label={`Rewrite ${index + 1} to path`}
                    />
                  </div>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    onClick={() => removeRewrite(index)}
                    aria-label={`Remove rewrite ${index + 1}`}
                    className="mb-0.5 shrink-0"
                  >
                    <X className="size-3.5" />
                  </Button>
                </div>
              ))}
            </div>
          )}

          {rewriteError && <p className="text-destructive text-xs">{rewriteError}</p>}

          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" variant="outline" size="sm" onClick={addRewrite}>
              <Plus className="size-3.5" />
              Add rewrite
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={syncDisabled}
              onClick={handleSync}
              title={
                hasConnection
                  ? "Fetch root-folder mappings from the arr instance"
                  : "Bind a connection first"
              }
            >
              <RefreshCw className={`size-3.5 ${suggest.isPending ? "animate-spin" : ""}`} />
              {suggest.isPending ? "Syncing…" : "Sync from arr"}
            </Button>
            <Button type="button" size="sm" disabled={isSaving} onClick={handleSave}>
              Save rewrites
            </Button>
          </div>

          {/* Sync-from-arr preview */}
          {preview && (
            <div
              className="border-border space-y-4 rounded-md border p-3"
              role="region"
              aria-label="Rewrite suggestions"
            >
              <div className="space-y-2">
                <p className="text-sm font-medium">Proposed</p>
                {preview.proposed.length === 0 ? (
                  <p className="text-muted-foreground text-xs">No proposed rewrites.</p>
                ) : (
                  preview.proposed.map((proposal) => (
                    <label key={proposal.from} className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={selected.has(proposal.from)}
                        onChange={() => toggleSelected(proposal.from)}
                      />
                      <span className="font-mono text-xs">
                        {proposal.from} → {proposal.to}
                      </span>
                      {proposal.match_depth >= 2 ? (
                        <Badge variant="secondary" className="text-xs">
                          {`${proposal.match_depth} segments`}
                        </Badge>
                      ) : (
                        <Badge variant="destructive" className="text-xs">
                          1 segment — weak
                        </Badge>
                      )}
                    </label>
                  ))
                )}
              </div>

              {preview.unmatched.length > 0 && (
                <CollapsibleList
                  title={`No Silo match (${preview.unmatched.length})`}
                  items={preview.unmatched}
                />
              )}

              {preview.ambiguous.length > 0 && (
                <CollapsibleList
                  title={`Ambiguous (${preview.ambiguous.length})`}
                  items={preview.ambiguous.map((a) => `${a.root} → ${a.candidates.join(", ")}`)}
                />
              )}

              {preview.covered.length > 0 && (
                <CollapsibleList
                  title={`Already mapped (${preview.covered.length})`}
                  items={preview.covered}
                />
              )}

              <div className="flex flex-wrap items-center gap-2">
                <Button type="button" size="sm" disabled={isSaving} onClick={applySelected}>
                  Apply selected
                </Button>
                <Button type="button" variant="outline" size="sm" onClick={() => setPreview(null)}>
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/** Collapsed list section used inside the sync-from-arr preview. */
function CollapsibleList({ title, items }: { title: string; items: string[] }) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  return (
    <div className="space-y-1">
      <button
        type="button"
        className="flex items-center gap-1.5 text-left"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-controls={panelId}
      >
        {open ? (
          <ChevronDown className="text-muted-foreground size-3.5 shrink-0" />
        ) : (
          <ChevronRight className="text-muted-foreground size-3.5 shrink-0" />
        )}
        <span className="text-sm font-medium">{title}</span>
      </button>
      {open && (
        <ul id={panelId} className="text-muted-foreground space-y-0.5 pl-5 text-xs">
          {items.map((item) => (
            <li key={item} className="font-mono break-all">
              {item}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// SourceRow
// ---------------------------------------------------------------------------

function SourceRow({
  source,
  connectionOptions,
  globalPollInterval,
  onDelete,
}: {
  source: AutoscanSource;
  connectionOptions: Array<{ id: string; name: string }>;
  globalPollInterval: number | null;
  onDelete: (source: AutoscanSource) => void;
}) {
  const update = useUpdateAutoscanSource();
  const [edit, setEdit] = useState<RowEdit>(() => sourceToRowEdit(source));
  const [intervalError, setIntervalError] = useState(false);

  const isDirty =
    edit.connectionId !== (source.connection_id ?? "") ||
    edit.intervalStr !==
      (source.poll_interval_seconds != null ? String(source.poll_interval_seconds) : "");

  /** Build the full desired state to send on every mutation — always includes path_rewrites. */
  function fullBody(overrides: Partial<AutoscanSourceInput>): AutoscanSourceInput {
    const intervalVal = edit.intervalStr.trim() === "" ? null : Number(edit.intervalStr);
    // Trim and drop empty rewrite rows before sending.
    const path_rewrites = edit.rewrites
      .map((r) => ({ from: r.from.trim(), to: r.to.trim() }))
      .filter((r) => r.from.length > 0 && r.to.length > 0);
    return {
      connection_id: edit.connectionId === "" ? null : edit.connectionId,
      enabled: source.enabled,
      poll_interval_seconds: intervalVal,
      path_rewrites,
      ...overrides,
    };
  }

  function handleToggleEnabled(checked: boolean) {
    update.mutate({
      id: source.id,
      body: fullBody({ enabled: checked }),
    });
  }

  function handleIntervalBlur() {
    const raw = edit.intervalStr.trim();
    if (raw === "") {
      setIntervalError(false);
      if (isDirty) update.mutate({ id: source.id, body: fullBody({}) });
      return;
    }
    const n = Number(raw);
    if (!Number.isInteger(n) || n < 1) {
      setIntervalError(true);
      return;
    }
    setIntervalError(false);
    if (isDirty) update.mutate({ id: source.id, body: fullBody({}) });
  }

  function handleConnectionChange(value: string) {
    const next = value === "__none__" ? "" : value;
    setEdit((e) => ({ ...e, connectionId: next }));
    // Auto-save connection change immediately; always send full state.
    const intervalVal = edit.intervalStr.trim() === "" ? null : Number(edit.intervalStr);
    const path_rewrites = edit.rewrites
      .map((r) => ({ from: r.from.trim(), to: r.to.trim() }))
      .filter((r) => r.from.length > 0 && r.to.length > 0);
    update.mutate({
      id: source.id,
      body: {
        connection_id: next === "" ? null : next,
        enabled: source.enabled,
        poll_interval_seconds: intervalVal,
        path_rewrites,
      },
    });
  }

  function handleRewriteSave(rewrites?: AutoscanPathRewrite[]) {
    // When called from "Apply selected", the merged rewrites are passed in
    // directly to avoid a stale-state read (setEdit hasn't flushed yet).
    const path_rewrites = (rewrites ?? edit.rewrites)
      .map((r) => ({ from: r.from.trim(), to: r.to.trim() }))
      .filter((r) => r.from.length > 0 && r.to.length > 0);
    const intervalVal = edit.intervalStr.trim() === "" ? null : Number(edit.intervalStr);
    update.mutate({
      id: source.id,
      body: {
        connection_id: edit.connectionId === "" ? null : edit.connectionId,
        enabled: source.enabled,
        poll_interval_seconds: intervalVal,
        path_rewrites,
      },
    });
  }

  // A source can only be meaningfully enabled when it has a bound connection.
  // "Effective" means either the server-side binding is set OR the operator has
  // selected one in the pending edit but hasn't saved yet.
  const hasEffectiveConnection = Boolean(source.connection_id) || Boolean(edit.connectionId);

  // Status column
  const hasError = Boolean(source.last_error);
  const hasRun = Boolean(source.last_run_at);

  return (
    <TableRow>
      {/* Plugin / capability */}
      <TableCell>
        <div className="space-y-0.5">
          <p className="leading-none font-medium">{source.capability_id}</p>
          <p className="text-muted-foreground text-xs">Plugin #{source.installation_id}</p>
        </div>
      </TableCell>

      {/* Connection binding */}
      <TableCell>
        {source.connection_id === null && !edit.connectionId ? (
          <div className="flex items-center gap-2">
            <Select value="__none__" onValueChange={handleConnectionChange}>
              <SelectTrigger
                className="w-[200px]"
                aria-label={`Connection for ${sourceLabel(source)}`}
              >
                <SelectValue placeholder="No connection" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__none__">— No connection —</SelectItem>
                {connectionOptions.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Badge variant="outline" className="text-muted-foreground shrink-0">
              Needs connection
            </Badge>
          </div>
        ) : (
          <Select value={edit.connectionId || "__none__"} onValueChange={handleConnectionChange}>
            <SelectTrigger
              className="w-[200px]"
              aria-label={`Connection for ${sourceLabel(source)}`}
            >
              <SelectValue placeholder="No connection" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__none__">— No connection —</SelectItem>
              {connectionOptions.map((c) => (
                <SelectItem key={c.id} value={c.id}>
                  {c.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </TableCell>

      {/* Poll interval */}
      <TableCell>
        <div className="flex items-center gap-2">
          <Input
            className="w-24"
            placeholder="Default"
            value={edit.intervalStr}
            aria-invalid={intervalError}
            aria-label={`Poll interval seconds for ${sourceLabel(source)}`}
            onChange={(e) => {
              setIntervalError(false);
              setEdit((ed) => ({ ...ed, intervalStr: e.target.value }));
            }}
            onBlur={handleIntervalBlur}
          />
          <span className="text-muted-foreground text-xs">sec</span>
        </div>
        {intervalError && (
          <p className="text-destructive mt-1 text-xs">Must be a positive integer.</p>
        )}
        <p className="text-muted-foreground mt-1 text-xs">
          {globalPollInterval != null
            ? `Floor only — values below the global default (${globalPollInterval}s) have no effect.`
            : "Floor only — values below the global default poll interval have no effect."}
        </p>
        {/* Path rewrites editor — nested under the interval cell to avoid adding a new column */}
        <RewriteEditor
          sourceId={source.id}
          hasConnection={hasEffectiveConnection}
          rewrites={edit.rewrites}
          onChange={(next) => setEdit((ed) => ({ ...ed, rewrites: next }))}
          onSave={handleRewriteSave}
          isSaving={update.isPending}
        />
      </TableCell>

      {/* Enable toggle */}
      <TableCell>
        <Switch
          checked={source.enabled}
          onCheckedChange={handleToggleEnabled}
          disabled={update.isPending || !hasEffectiveConnection}
          aria-label={`${sourceLabel(source)} enabled`}
        />
      </TableCell>

      {/* Status */}
      <TableCell>
        {hasError ? (
          <div className="flex items-center gap-1.5 text-sm">
            <AlertTriangle className="text-destructive size-4 shrink-0" />
            <div className="space-y-0.5">
              <p className="text-destructive leading-none font-medium">Error</p>
              <p
                className="text-muted-foreground max-w-[180px] truncate text-xs"
                title={source.last_error ?? ""}
              >
                {source.last_error}
              </p>
            </div>
          </div>
        ) : hasRun ? (
          <div className="flex items-center gap-1.5 text-sm">
            <CheckCircle2 className="size-4 shrink-0 text-green-500" />
            <div className="space-y-0.5">
              <p className="leading-none font-medium">OK</p>
              <p className="text-muted-foreground flex items-center gap-1 text-xs">
                <Clock className="size-3" />
                {formatRelativeTime(source.last_run_at)}
              </p>
            </div>
          </div>
        ) : (
          <span className="text-muted-foreground text-sm">Not run yet</span>
        )}
      </TableCell>

      {/* Actions */}
      <TableCell>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={`Delete source ${sourceLabel(source)}`}
          onClick={() => onDelete(source)}
        >
          <Trash2 className="text-destructive" />
        </Button>
      </TableCell>
    </TableRow>
  );
}

// ---------------------------------------------------------------------------
// Add source dialog
// ---------------------------------------------------------------------------

interface AddSourceForm {
  /** "installation_id:capability_id" composite key of the chosen plugin. */
  pluginKey: string;
  connectionId: string; // "" / "__none__" means no connection
  intervalStr: string;
}

const BLANK_ADD_SOURCE: AddSourceForm = {
  pluginKey: "",
  connectionId: "",
  intervalStr: "",
};

function pluginKey(installationId: number, capabilityId: string): string {
  return `${installationId}:${capabilityId}`;
}

function AddSourceDialog({
  open,
  onOpenChange,
  connectionOptions,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  connectionOptions: Array<{ id: string; name: string }>;
}) {
  const available = useAvailableScanSources();
  const createSource = useCreateAutoscanSource();
  const [form, setForm] = useState<AddSourceForm>(BLANK_ADD_SOURCE);

  const plugins = available.data ?? [];
  const selectedPlugin = plugins.find(
    (p) => pluginKey(p.installation_id, p.capability_id) === form.pluginKey,
  );

  function close() {
    setForm(BLANK_ADD_SOURCE);
    onOpenChange(false);
  }

  function handleSubmit() {
    if (!selectedPlugin) return;
    const connectionId =
      form.connectionId && form.connectionId !== "__none__" ? form.connectionId : null;
    const raw = form.intervalStr.trim();
    const pollInterval = raw === "" ? null : Number(raw);
    createSource.mutate(
      {
        installation_id: selectedPlugin.installation_id,
        capability_id: selectedPlugin.capability_id,
        connection_id: connectionId,
        enabled: false,
        poll_interval_seconds: pollInterval,
        path_rewrites: [],
      },
      { onSuccess: close },
    );
  }

  const intervalInvalid =
    form.intervalStr.trim() !== "" &&
    (!Number.isInteger(Number(form.intervalStr)) || Number(form.intervalStr) < 1);
  const canSubmit = Boolean(selectedPlugin) && !intervalInvalid && !createSource.isPending;

  return (
    <Dialog open={open} onOpenChange={(o) => (o ? onOpenChange(true) : close())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add scan source</DialogTitle>
          <DialogDescription>
            Create a scan source from an installed plugin and bind it to an arr connection. Add one
            source per connection to watch multiple arr instances.
          </DialogDescription>
        </DialogHeader>

        {available.isLoading ? (
          <p className="text-muted-foreground py-4 text-sm">Loading available plugins…</p>
        ) : plugins.length === 0 ? (
          <p className="text-muted-foreground py-4 text-sm">
            No scan-source plugins installed. Install one from the{" "}
            <Link to="/admin/plugins" className="text-primary underline-offset-4 hover:underline">
              Plugins page
            </Link>{" "}
            to add sources here.
          </p>
        ) : (
          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label>Plugin</Label>
              <Select
                value={form.pluginKey}
                onValueChange={(v) => setForm((f) => ({ ...f, pluginKey: v }))}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="Select a scan-source plugin…" />
                </SelectTrigger>
                <SelectContent>
                  {plugins.map((p) => (
                    <SelectItem
                      key={pluginKey(p.installation_id, p.capability_id)}
                      value={pluginKey(p.installation_id, p.capability_id)}
                    >
                      {p.display_name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1.5">
              <Label>Connection</Label>
              <Select
                value={form.connectionId || "__none__"}
                onValueChange={(v) => setForm((f) => ({ ...f, connectionId: v }))}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="No connection" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__none__">— No connection —</SelectItem>
                  {connectionOptions.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-xs">
                Optional — you can bind a connection later. A source must have a connection before
                it can be enabled.
              </p>
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="add-source-interval">Poll interval (seconds)</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="add-source-interval"
                  className="w-32"
                  placeholder="Default"
                  value={form.intervalStr}
                  aria-invalid={intervalInvalid}
                  onChange={(e) => setForm((f) => ({ ...f, intervalStr: e.target.value }))}
                />
                <span className="text-muted-foreground text-sm">sec</span>
              </div>
              {intervalInvalid && (
                <p className="text-destructive text-xs">Must be a positive integer.</p>
              )}
              <p className="text-muted-foreground text-xs">
                Optional — leave blank to use the global default poll interval.
              </p>
            </div>
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={close} disabled={createSource.isPending}>
            Cancel
          </Button>
          {plugins.length > 0 && (
            <Button onClick={handleSubmit} disabled={!canSubmit}>
              {createSource.isPending ? "Adding…" : "Add source"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Main panel
// ---------------------------------------------------------------------------

export default function SourcesPanel() {
  const sources = useAutoscanSources();
  const connections = useAutoscanConnections();
  const settings = useAutoscanSettings();
  const deleteSource = useDeleteAutoscanSource();

  const [deleteTarget, setDeleteTarget] = useState<AutoscanSource | null>(null);
  const [addOpen, setAddOpen] = useState(false);

  const connectionOptions = (connections.data ?? []).map((c) => ({
    id: c.id,
    name: c.name,
  }));

  const globalPollInterval = settings.data?.default_poll_interval_seconds ?? null;

  const header = (
    <div className="flex items-center justify-between gap-3">
      <p className="text-muted-foreground text-xs">
        Scan-source plugins are installed from the{" "}
        <Link to="/admin/plugins" className="text-primary underline-offset-4 hover:underline">
          Plugins page
        </Link>
        . Add a source for each arr connection you want to watch.
      </p>
      <Button variant="outline" size="sm" className="shrink-0" onClick={() => setAddOpen(true)}>
        <Plus />
        Add source
      </Button>
    </div>
  );

  const addDialog = (
    <AddSourceDialog
      open={addOpen}
      onOpenChange={setAddOpen}
      connectionOptions={connectionOptions}
    />
  );

  if (sources.isLoading) {
    return <p className="text-muted-foreground py-4 text-sm">Loading sources…</p>;
  }

  if (sources.isError) {
    return (
      <p className="text-destructive py-4 text-sm">
        Failed to load scan sources. Please reload the page.
      </p>
    );
  }

  const list = sources.data ?? [];

  if (list.length === 0) {
    return (
      <div className="space-y-4">
        {header}
        <div className="rounded-lg border border-dashed p-8 text-center">
          <p className="text-muted-foreground text-sm">
            No scan sources yet. Click <span className="font-medium">Add source</span> to create one
            from an installed scan-source plugin.
          </p>
        </div>
        {addDialog}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {header}

      <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Source</TableHead>
              <TableHead>Connection</TableHead>
              <TableHead>Interval &amp; path rewrites</TableHead>
              <TableHead>Enabled</TableHead>
              <TableHead>Last run</TableHead>
              <TableHead className="w-0" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.map((source) => (
              <SourceRow
                key={source.id}
                source={source}
                connectionOptions={connectionOptions}
                globalPollInterval={globalPollInterval}
                onDelete={setDeleteTarget}
              />
            ))}
          </TableBody>
        </Table>
      </div>

      {/* Delete confirmation */}
      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete source?</AlertDialogTitle>
            <AlertDialogDescription>
              &ldquo;{deleteTarget ? sourceLabel(deleteTarget) : ""}&rdquo; will be permanently
              removed. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (deleteTarget) {
                  deleteSource.mutate(deleteTarget.id);
                  setDeleteTarget(null);
                }
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {addDialog}
    </div>
  );
}
