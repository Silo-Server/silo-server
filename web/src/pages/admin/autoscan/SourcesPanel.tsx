import { useState } from "react";
import { AlertTriangle, CheckCircle2, Clock } from "lucide-react";
import type { AutoscanSource, AutoscanSourceInput } from "@/api/types";
import { Badge } from "@/components/ui/badge";
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
  useAutoscanSources,
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
}: {
  source: AutoscanSource;
  connectionOptions: Array<{ id: string; name: string }>;
}) {
  const update = useUpdateAutoscanSource();
  const [edit, setEdit] = useState<RowEdit>(() => sourceToRowEdit(source));
  const [intervalError, setIntervalError] = useState(false);

  const isDirty =
    edit.connectionId !== (source.connection_id ?? "") ||
    edit.intervalStr !==
      (source.poll_interval_seconds != null ? String(source.poll_interval_seconds) : "");

  function commitChange(patch: Partial<AutoscanSourceInput>) {
    const intervalVal = edit.intervalStr.trim() === "" ? null : Number(edit.intervalStr);

    const body: AutoscanSourceInput = {
      enabled: source.enabled,
      connection_id: edit.connectionId || undefined,
      poll_interval_seconds: intervalVal,
      ...patch,
    };

    update.mutate({ id: source.id, body });
  }

  function handleToggleEnabled(checked: boolean) {
    if (checked && !edit.connectionId) {
      // Surface a friendly message; backend would 400 anyway.
      // The toast comes from the mutation's onError handler.
      update.mutate(
        {
          id: source.id,
          body: {
            enabled: true,
            connection_id: undefined,
            poll_interval_seconds: edit.intervalStr.trim() === "" ? null : Number(edit.intervalStr),
          },
        },
        // onError is handled globally in useUpdateAutoscanSource; nothing extra needed here.
      );
      return;
    }
    commitChange({ enabled: checked });
  }

  function handleIntervalBlur() {
    const raw = edit.intervalStr.trim();
    if (raw === "") {
      setIntervalError(false);
      if (isDirty) commitChange({});
      return;
    }
    const n = Number(raw);
    if (!Number.isInteger(n) || n < 1) {
      setIntervalError(true);
      return;
    }
    setIntervalError(false);
    if (isDirty) commitChange({});
  }

  function handleConnectionChange(value: string) {
    const next = value === "__none__" ? "" : value;
    setEdit((e) => ({ ...e, connectionId: next }));
    // Auto-save connection change immediately.
    const intervalVal = edit.intervalStr.trim() === "" ? null : Number(edit.intervalStr);
    update.mutate({
      id: source.id,
      body: {
        enabled: source.enabled,
        connection_id: next || undefined,
        poll_interval_seconds: intervalVal,
      },
    });
  }

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
              <SelectTrigger className="w-[200px]">
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
            <SelectTrigger className="w-[200px]">
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
      </TableCell>

      {/* Enable toggle */}
      <TableCell>
        <Switch
          checked={source.enabled}
          onCheckedChange={handleToggleEnabled}
          disabled={update.isPending}
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
    </TableRow>
  );
}

// ---------------------------------------------------------------------------
// Main panel
// ---------------------------------------------------------------------------

export default function SourcesPanel() {
  const sources = useAutoscanSources();
  const connections = useAutoscanConnections();

  const connectionOptions = (connections.data ?? []).map((c) => ({
    id: c.id,
    name: c.name,
  }));

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
    <div className="rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Source</TableHead>
            <TableHead>Connection</TableHead>
            <TableHead>Interval</TableHead>
            <TableHead>Enabled</TableHead>
            <TableHead>Last run</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {list.map((source) => (
            <SourceRow key={source.id} source={source} connectionOptions={connectionOptions} />
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
