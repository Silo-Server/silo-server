import { useId, useState } from "react";
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Clock,
  Library as LibraryIcon,
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
  Library,
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
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";

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
  sourceConfig: Record<string, string>;
}

function sourceToRowEdit(source: AutoscanSource): RowEdit {
  return {
    connectionId: source.connection_id ?? "",
    intervalStr: source.poll_interval_seconds != null ? String(source.poll_interval_seconds) : "",
    rewrites: source.path_rewrites.map((r) => ({ ...r })),
    sourceConfig: sourceConfigForEdit(source),
  };
}

const CEPHFS_PLUGIN_ID = "silo.autoscan.cephfs";
const CEPHFS_CAPABILITY_ID = "cephfs";
const DEFAULT_CEPHFS_EXCLUSIONS = [
  "*.partial",
  "*.tmp",
  "@eaDir",
  "#recycle",
  ".downloads",
  ".recyclebin",
  "volumes",
];
const CEPHFS_MOVIE_PATHS_KEY = "movie_flat_paths";
const CEPHFS_TV_PATHS_KEY = "tv_flat_paths";
const CEPHFS_LEGACY_MOVIE_NESTED_KEY = "movie_nested_paths";
const CEPHFS_LEGACY_TV_NESTED_KEY = "tv_nested_paths";

function isCephFSSource(source: AutoscanSource): boolean {
  return source.capability_id === CEPHFS_CAPABILITY_ID;
}

function isCephFSPlugin(plugin: { plugin_id: string; capability_id: string } | undefined): boolean {
  return plugin?.plugin_id === CEPHFS_PLUGIN_ID || plugin?.capability_id === CEPHFS_CAPABILITY_ID;
}

function defaultCephFSConfig(): Record<string, string> {
  return { exclusions: DEFAULT_CEPHFS_EXCLUSIONS.join("\n") };
}

function mergeLineValues(...values: Array<string | undefined>): string {
  const lines = new Set<string>();
  for (const value of values) {
    (value ?? "")
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean)
      .forEach((line) => lines.add(line));
  }
  return Array.from(lines).join("\n");
}

function mergeLineDefaults(value: string | undefined, defaults: string[]): string {
  const lines = new Set(
    (value ?? "")
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean),
  );
  for (const entry of defaults) {
    lines.add(entry);
  }
  return Array.from(lines).join("\n");
}

function sourceConfigForEdit(source: AutoscanSource): Record<string, string> {
  if (!isCephFSSource(source)) {
    return { ...(source.source_config ?? {}) };
  }
  const config = source.source_config ?? {};
  return {
    [CEPHFS_MOVIE_PATHS_KEY]: mergeLineValues(
      config[CEPHFS_MOVIE_PATHS_KEY],
      config[CEPHFS_LEGACY_MOVIE_NESTED_KEY],
    ),
    [CEPHFS_TV_PATHS_KEY]: mergeLineValues(
      config[CEPHFS_TV_PATHS_KEY],
      config[CEPHFS_LEGACY_TV_NESTED_KEY],
    ),
    exclusions: mergeLineDefaults(config.exclusions, DEFAULT_CEPHFS_EXCLUSIONS),
  };
}

function normalizeSourceConfig(config: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(config)) {
    const trimmedKey = key.trim();
    if (!trimmedKey) continue;
    out[trimmedKey] = value.trim();
  }
  return out;
}

function libraryKind(type: string): "movie" | "tv" | "mixed" | null {
  switch (type.trim().toLowerCase()) {
    case "movie":
    case "movies":
      return "movie";
    case "series":
    case "show":
    case "shows":
    case "tv":
    case "tvshows":
      return "tv";
    case "mixed":
      return "mixed";
    default:
      return null;
  }
}

function configFromLibraries(
  libraries: Library[],
  current: Record<string, string>,
): Record<string, string> {
  const moviePaths = new Set<string>();
  const tvPaths = new Set<string>();

  for (const library of libraries) {
    if (!library.enabled) {
      continue;
    }
    const kind = libraryKind(library.type);
    if (!kind) {
      continue;
    }
    for (const path of library.paths ?? []) {
      const trimmed = path.trim();
      if (!trimmed) {
        continue;
      }
      if (kind === "movie" || kind === "mixed") {
        moviePaths.add(trimmed);
      }
      if (kind === "tv" || kind === "mixed") {
        tvPaths.add(trimmed);
      }
    }
  }

  return {
    [CEPHFS_MOVIE_PATHS_KEY]: Array.from(moviePaths).join("\n"),
    [CEPHFS_TV_PATHS_KEY]: Array.from(tvPaths).join("\n"),
    exclusions: mergeLineDefaults(current.exclusions, DEFAULT_CEPHFS_EXCLUSIONS),
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
                <div key={index} className="flex flex-col gap-2 sm:flex-row sm:items-end">
                  <div className="min-w-0 flex-1 space-y-1">
                    <Label className="text-muted-foreground text-xs">From</Label>
                    <Input
                      value={rewrite.from}
                      onChange={(e) => updateRewrite(index, { from: e.target.value })}
                      placeholder="/remote/media"
                      className="h-8 text-sm"
                      aria-label={`Rewrite ${index + 1} from path`}
                    />
                  </div>
                  <div className="min-w-0 flex-1 space-y-1">
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
                    className="shrink-0 self-end sm:mb-0.5"
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
                  ? "Fetch root-folder mappings from the connected server"
                  : "Bind a connection first"
              }
            >
              <RefreshCw className={`size-3.5 ${suggest.isPending ? "animate-spin" : ""}`} />
              {suggest.isPending ? "Syncing…" : "Sync from server"}
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

function CephFSConfigEditor({
  config,
  onChange,
  onSave,
  isSaving,
  showSave = true,
}: {
  config: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
  onSave: (next?: Record<string, string>) => void;
  isSaving: boolean;
  showSave?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const libraries = useAdminLibraries();

  function patch(key: string, value: string) {
    onChange({ ...config, [key]: value });
  }

  function useConfiguredLibraries() {
    const next = configFromLibraries(libraries.data ?? [], config);
    onChange(next);
    if (showSave) {
      onSave(next);
    }
  }

  const libraryPathCount =
    libraries.data
      ?.filter((library) => library.enabled)
      .reduce((count, library) => count + (library.paths?.length ?? 0), 0) ?? 0;
  const canUseLibraries = !libraries.isLoading && libraryPathCount > 0 && !isSaving;

  const fields = [
    {
      key: CEPHFS_MOVIE_PATHS_KEY,
      label: "Movie library roots",
      description: "One configured movie library path per line.",
      placeholder: "/mnt/media/movies",
    },
    {
      key: CEPHFS_TV_PATHS_KEY,
      label: "TV library roots",
      description: "One configured TV library path per line.",
      placeholder: "/mnt/media/television",
    },
    {
      key: "exclusions",
      label: "Ignored paths",
      description: "One glob, directory name, or path prefix per line.",
      placeholder: DEFAULT_CEPHFS_EXCLUSIONS.join("\n"),
    },
  ];

  return (
    <div className="border-border mt-3 rounded-md border">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <button
          type="button"
          className="flex min-w-0 flex-1 items-center gap-2 px-3 pt-3 text-left sm:py-2"
          onClick={() => setOpen((o) => !o)}
          aria-expanded={open}
          aria-controls={panelId}
        >
          {open ? (
            <ChevronDown className="text-muted-foreground size-3.5 shrink-0" />
          ) : (
            <ChevronRight className="text-muted-foreground size-3.5 shrink-0" />
          )}
          <span className="truncate text-sm font-medium">CephFS paths &amp; ignores</span>
        </button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="mx-3 mb-3 justify-center whitespace-normal sm:mr-2 sm:mb-0 sm:ml-0 sm:shrink-0 sm:whitespace-nowrap"
          disabled={!canUseLibraries}
          onClick={useConfiguredLibraries}
          title={
            libraries.isLoading
              ? "Loading libraries"
              : libraryPathCount === 0
                ? "No enabled library paths"
                : "Replace CephFS roots with enabled Silo library paths"
          }
        >
          <LibraryIcon className="size-3.5" />
          Use configured libraries
        </Button>
      </div>

      {open && (
        <div id={panelId} className="space-y-3 px-3 pb-3">
          <div className="grid gap-3 md:grid-cols-2">
            {fields.map((field) => (
              <div key={field.key} className="space-y-1">
                <Label className="text-muted-foreground text-xs">{field.label}</Label>
                <textarea
                  value={config[field.key] ?? ""}
                  onChange={(event) => patch(field.key, event.target.value)}
                  placeholder={field.placeholder}
                  rows={field.key === "exclusions" ? 6 : 3}
                  className="border-input bg-background ring-offset-background placeholder:text-muted-foreground focus-visible:ring-ring min-h-20 w-full rounded-md border px-3 py-2 font-mono text-xs focus-visible:ring-2 focus-visible:ring-offset-2 focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50"
                  aria-label={field.label}
                />
                <p className="text-muted-foreground text-xs">{field.description}</p>
              </div>
            ))}
          </div>
          {showSave && (
            <div className="flex justify-end">
              <Button type="button" size="sm" disabled={isSaving} onClick={() => onSave()}>
                Save CephFS settings
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// SourceRow helpers
// ---------------------------------------------------------------------------

/**
 * Safely parse an interval string for inclusion in a source PUT body.
 *
 * - Empty/blank → null (intentional "use the global default").
 * - Valid positive integer → that integer.
 * - Anything else (NaN, non-integer, < 1, mid-edit garbage) → fall back to
 *   the source's currently-persisted value so an unrelated save (enable toggle,
 *   connection change) never corrupts the interval.
 */
function parseInterval(intervalStr: string, current: number | null): number | null {
  const t = intervalStr.trim();
  if (t === "") return null;
  const n = Number(t);
  if (!Number.isInteger(n) || n < 1) return current;
  return n;
}

// ---------------------------------------------------------------------------
// SourceRow
// ---------------------------------------------------------------------------

function SourceRow({
  source,
  connectionOptions,
  globalPollInterval,
  onDelete,
  layout = "table",
}: {
  source: AutoscanSource;
  connectionOptions: Array<{ id: string; name: string }>;
  globalPollInterval: number | null;
  onDelete: (source: AutoscanSource) => void;
  layout?: "table" | "card";
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
    const intervalVal = parseInterval(edit.intervalStr, source.poll_interval_seconds);
    // Trim and drop empty rewrite rows before sending.
    const path_rewrites = edit.rewrites
      .map((r) => ({ from: r.from.trim(), to: r.to.trim() }))
      .filter((r) => r.from.length > 0 && r.to.length > 0);
    return {
      connection_id: edit.connectionId === "" ? null : edit.connectionId,
      enabled: source.enabled,
      poll_interval_seconds: intervalVal,
      path_rewrites,
      source_config: normalizeSourceConfig(edit.sourceConfig),
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
    // Auto-save connection change immediately; always send full state via fullBody
    // so the interval is computed safely (never corrupted by mid-edit garbage).
    update.mutate({
      id: source.id,
      body: fullBody({ connection_id: next === "" ? null : next }),
    });
  }

  function handleRewriteSave(rewrites?: AutoscanPathRewrite[]) {
    // When called from "Apply selected", the merged rewrites are passed in
    // directly to avoid a stale-state read (setEdit hasn't flushed yet).
    const path_rewrites = (rewrites ?? edit.rewrites)
      .map((r) => ({ from: r.from.trim(), to: r.to.trim() }))
      .filter((r) => r.from.length > 0 && r.to.length > 0);
    update.mutate({
      id: source.id,
      body: fullBody({ path_rewrites }),
    });
  }

  function handleSourceConfigSave(nextConfig?: Record<string, string>) {
    const sourceConfig = nextConfig ?? edit.sourceConfig;
    update.mutate({
      id: source.id,
      body: fullBody({ source_config: normalizeSourceConfig(sourceConfig) }),
    });
  }

  // Whether this source has a bound connection (server-side or pending edit).
  // Used to gate the Sync-from-server button, which needs a server to query.
  const hasEffectiveConnection = Boolean(source.connection_id) || Boolean(edit.connectionId);

  // Status column
  const hasError = Boolean(source.last_error);
  const hasRun = Boolean(source.last_run_at);
  const connectionSelectClass = layout === "card" ? "!w-full min-w-0 max-w-full" : "w-[200px]";
  const statusMessageClass =
    layout === "card"
      ? "text-muted-foreground min-w-0 max-w-full whitespace-normal break-words text-xs [overflow-wrap:anywhere]"
      : "text-muted-foreground min-w-0 max-w-full truncate text-xs";
  const intervalHelp =
    globalPollInterval != null
      ? `Floor only - values below the global default (${globalPollInterval}s) have no effect.`
      : "Floor only - values below the global default poll interval have no effect.";

  const sourceIdentity = (
    <div className="min-w-0 space-y-0.5">
      <p className="truncate leading-none font-medium">{source.capability_id}</p>
      <p className="text-muted-foreground text-xs">Plugin #{source.installation_id}</p>
    </div>
  );

  const statusNode = hasError ? (
    <div className="flex max-w-full min-w-0 items-start gap-1.5 overflow-hidden text-sm">
      <AlertTriangle className="text-destructive size-4 shrink-0" />
      <div className="min-w-0 space-y-0.5">
        <p className="text-destructive leading-none font-medium">Error</p>
        <p className={statusMessageClass} title={source.last_error ?? ""}>
          {source.last_error}
        </p>
      </div>
    </div>
  ) : hasRun ? (
    <div className="flex max-w-full min-w-0 items-center gap-1.5 overflow-hidden text-sm">
      <CheckCircle2 className="size-4 shrink-0 text-green-500" />
      <div className="min-w-0 space-y-0.5">
        <p className="leading-none font-medium">OK</p>
        <p className="text-muted-foreground flex items-center gap-1 text-xs">
          <Clock className="size-3" />
          {formatRelativeTime(source.last_run_at)}
        </p>
      </div>
    </div>
  ) : (
    <span className="text-muted-foreground text-sm">Not run yet</span>
  );

  const connectionControl =
    source.connection_id === null && !edit.connectionId ? (
      <div className="flex max-w-full min-w-0 flex-col gap-2 sm:flex-row sm:items-center">
        <Select value="__none__" onValueChange={handleConnectionChange}>
          <SelectTrigger
            className={connectionSelectClass}
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
        <Badge variant="outline" className="text-muted-foreground w-fit shrink-0">
          No connection
        </Badge>
      </div>
    ) : (
      <Select value={edit.connectionId || "__none__"} onValueChange={handleConnectionChange}>
        <SelectTrigger
          className={connectionSelectClass}
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
    );

  const intervalSettings = (
    <>
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
      <p className="text-muted-foreground mt-1 text-xs">{intervalHelp}</p>
      <RewriteEditor
        sourceId={source.id}
        hasConnection={hasEffectiveConnection}
        rewrites={edit.rewrites}
        onChange={(next) => setEdit((ed) => ({ ...ed, rewrites: next }))}
        onSave={handleRewriteSave}
        isSaving={update.isPending}
      />
      {isCephFSSource(source) && (
        <CephFSConfigEditor
          config={edit.sourceConfig}
          onChange={(next) => setEdit((ed) => ({ ...ed, sourceConfig: next }))}
          onSave={handleSourceConfigSave}
          isSaving={update.isPending}
        />
      )}
    </>
  );

  if (layout === "card") {
    return (
      <section className="bg-card min-w-0 overflow-hidden rounded-lg border p-3 shadow-sm">
        <div className="flex min-w-0 items-start justify-between gap-3">
          {sourceIdentity}
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={`Delete source ${sourceLabel(source)}`}
            onClick={() => onDelete(source)}
            className="shrink-0"
          >
            <Trash2 className="text-destructive" />
          </Button>
        </div>

        <div className="mt-4 grid min-w-0 gap-4">
          <div className="grid min-w-0 gap-1.5">
            <Label className="text-muted-foreground text-xs">Connection</Label>
            {connectionControl}
          </div>

          <div className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-3 rounded-md border px-3 py-2">
            <div className="min-w-0">
              <p className="text-sm font-medium">Enabled</p>
              <p className="text-muted-foreground text-xs break-words">
                Poll this source for changes
              </p>
            </div>
            <Switch
              checked={source.enabled}
              onCheckedChange={handleToggleEnabled}
              disabled={update.isPending}
              aria-label={`${sourceLabel(source)} enabled`}
            />
          </div>

          <div className="grid min-w-0 gap-1.5">
            <Label className="text-muted-foreground text-xs">Last run</Label>
            <div className="min-w-0 overflow-hidden rounded-md border px-3 py-2">{statusNode}</div>
          </div>

          <div className="grid min-w-0 gap-1.5">
            <Label className="text-muted-foreground text-xs">Interval &amp; settings</Label>
            {intervalSettings}
          </div>
        </div>
      </section>
    );
  }

  return (
    <TableRow>
      {/* Plugin / capability */}
      <TableCell>{sourceIdentity}</TableCell>

      {/* Connection binding */}
      <TableCell>{connectionControl}</TableCell>

      {/* Poll interval */}
      <TableCell>{intervalSettings}</TableCell>

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
      <TableCell>{statusNode}</TableCell>

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
  sourceConfig: Record<string, string>;
}

const BLANK_ADD_SOURCE: AddSourceForm = {
  pluginKey: "",
  connectionId: "",
  intervalStr: "",
  sourceConfig: {},
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
  const selectedIsCephFS = isCephFSPlugin(selectedPlugin);

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
        source_config: selectedIsCephFS ? normalizeSourceConfig(form.sourceConfig) : {},
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
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Add scan source</DialogTitle>
          <DialogDescription>
            Create a scan source from an installed plugin. Bind a connection if the source needs to
            reach a server (Sonarr/Radarr); a source that reads locally needs none. Add one source
            per thing you want to watch.
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
                onValueChange={(v) => {
                  const plugin = plugins.find(
                    (p) => pluginKey(p.installation_id, p.capability_id) === v,
                  );
                  setForm((f) => ({
                    ...f,
                    pluginKey: v,
                    sourceConfig:
                      isCephFSPlugin(plugin) && Object.keys(f.sourceConfig).length === 0
                        ? defaultCephFSConfig()
                        : f.sourceConfig,
                  }));
                }}
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
                Optional — bind one if the source needs to reach a server (e.g. Sonarr/Radarr).
                Sources that read locally need no connection.
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

            {selectedIsCephFS && (
              <CephFSConfigEditor
                config={form.sourceConfig}
                onChange={(sourceConfig) => setForm((f) => ({ ...f, sourceConfig }))}
                onSave={() => undefined}
                isSaving={createSource.isPending}
                showSave={false}
              />
            )}
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
    <div className="flex flex-col items-start gap-3 sm:flex-row sm:items-center sm:justify-between">
      <p className="text-muted-foreground text-xs">
        Scan-source plugins are installed from the{" "}
        <Link to="/admin/plugins" className="text-primary underline-offset-4 hover:underline">
          Plugins page
        </Link>
        . Add a source for each thing you want to watch.
      </p>
      <Button
        variant="outline"
        size="sm"
        className="w-full justify-center sm:w-auto sm:shrink-0"
        onClick={() => setAddOpen(true)}
      >
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

      <div className="space-y-3 lg:hidden">
        {list.map((source) => (
          <SourceRow
            key={source.id}
            source={source}
            connectionOptions={connectionOptions}
            globalPollInterval={globalPollInterval}
            onDelete={setDeleteTarget}
            layout="card"
          />
        ))}
      </div>

      <div className="hidden rounded-lg border lg:block">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Source</TableHead>
              <TableHead>Connection</TableHead>
              <TableHead>Interval &amp; settings</TableHead>
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
                layout="table"
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
