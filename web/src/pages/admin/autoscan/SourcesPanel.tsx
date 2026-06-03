import { useState } from "react";
import { AlertTriangle, CheckCircle2, Clock, Trash2 } from "lucide-react";
import type { AutoscanSource, AutoscanSourceInput } from "@/api/types";
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
import { Input } from "@/components/ui/input";
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
  useAutoscanSettings,
  useAutoscanSources,
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
// Row state for per-row edits (connection + interval)
// ---------------------------------------------------------------------------

interface RowEdit {
  connectionId: string; // "" means no connection
  intervalStr: string; // "" means use default
}

function sourceToRowEdit(source: AutoscanSource): RowEdit {
  return {
    connectionId: source.connection_id ?? "",
    intervalStr: source.poll_interval_seconds != null ? String(source.poll_interval_seconds) : "",
  };
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

  /** Build the full desired state to send on every mutation. */
  function fullBody(overrides: Partial<AutoscanSourceInput>): AutoscanSourceInput {
    const intervalVal = edit.intervalStr.trim() === "" ? null : Number(edit.intervalStr);
    return {
      connection_id: edit.connectionId === "" ? null : edit.connectionId,
      enabled: source.enabled,
      poll_interval_seconds: intervalVal,
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
    update.mutate({
      id: source.id,
      body: {
        connection_id: next === "" ? null : next,
        enabled: source.enabled,
        poll_interval_seconds: intervalVal,
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
// Main panel
// ---------------------------------------------------------------------------

export default function SourcesPanel() {
  const sources = useAutoscanSources();
  const connections = useAutoscanConnections();
  const settings = useAutoscanSettings();
  const deleteSource = useDeleteAutoscanSource();

  const [deleteTarget, setDeleteTarget] = useState<AutoscanSource | null>(null);

  const connectionOptions = (connections.data ?? []).map((c) => ({
    id: c.id,
    name: c.name,
  }));

  const globalPollInterval = settings.data?.default_poll_interval_seconds ?? null;

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
      <p className="text-muted-foreground py-6 text-sm">
        No scan sources found. Install a <code>scan_source</code> plugin capability to see sources
        here.
      </p>
    );
  }

  return (
    <>
      <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Source</TableHead>
              <TableHead>Connection</TableHead>
              <TableHead>Interval</TableHead>
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
    </>
  );
}
