import type { ReactNode } from "react";
import { useMemo, useState } from "react";
import { Link } from "react-router";
import {
  AlertTriangle,
  ChevronDown,
  ChevronUp,
  Clock,
  Filter,
  FileText,
  HardDriveDownload,
  Loader,
  Pause,
  Play,
  RotateCw,
  Search,
  Terminal,
  X,
} from "lucide-react";
import { toast } from "sonner";
import type {
  AdminDownloadPreparation,
  AdminDownloadPreparationAction,
  AdminDownloadPreparationList,
} from "@/api/v2/adminDownloadPreparations";
import type { OperationalLogEntry } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useAdminDownloadPreparationAction } from "@/hooks/queries/admin/downloadPreparations";
import { useOperationalLogs } from "@/hooks/queries/admin/logs";
import { formatTime } from "@/lib/datetime";
import { formatCodecLabel, formatFileSize } from "@/lib/mediaFormat";
import { cn } from "@/lib/utils";
import { activityMethodMeta } from "@/pages/adminActivityPresentation";
import {
  PREPARATION_STATES,
  canPausePreparation,
  canResumePreparation,
  cancelPreparationsPrompt,
  formatAgo,
  formatClock,
  formatPreparationAudioOutput,
  formatPreparationBitrate,
  formatPreparationOutput,
  formatPreparationSource,
  formatPreparationSourceAudio,
  formatPreparationToneMap,
  formatPreparationVideoOutput,
  formatRemaining,
  formatRetryIn,
  formatSpeed,
  preparationPercent,
  preparationRemainingSeconds,
  preparationRunningLabel,
  preparationSearchText,
  preparationStateMeta,
  preparationSubtitle,
  preparationTitle,
  preparationWorker,
  requesterDevice,
  requesterName,
  secondsSince,
  summarizePreparationAction,
  type PreparationState,
} from "@/pages/adminDownloadPreparationPresentation";

const GRID =
  "grid-cols-[16px_minmax(180px,1.6fr)_minmax(190px,1.5fr)_minmax(100px,0.8fr)_minmax(200px,1.5fr)_minmax(150px,1fr)_112px]";

const CHECKBOX = "accent-primary size-3.5 cursor-pointer disabled:cursor-default";

/** Runs one administrator action and reports its outcome in a toast. */
function usePreparationActions() {
  const mutation = useAdminDownloadPreparationAction();
  const run = (
    action: AdminDownloadPreparationAction,
    targets: readonly AdminDownloadPreparation[],
    onDone?: () => void,
  ) => {
    if (targets.length === 0) return;
    mutation.mutate(
      { action, ids: targets.map((t) => t.id) },
      {
        onSuccess: (results) => {
          const summary = summarizePreparationAction(action, results);
          if (results.some((r) => r.outcome === "applied")) toast.success(summary);
          else toast.info(summary);
          onDone?.();
        },
        onError: () => {
          toast.error(`Couldn't ${action}. Check the list before trying again.`);
        },
      },
    );
  };
  const pendingIds = mutation.isPending ? new Set(mutation.variables?.ids) : null;
  return { run, isPending: mutation.isPending, pendingIds };
}

export default function AdminDownloadPreparationsPanel({
  list,
  isLoading,
  isError,
}: {
  list: AdminDownloadPreparationList | undefined;
  isLoading: boolean;
  isError: boolean;
}) {
  const [search, setSearch] = useState("");
  const [stateFilter, setStateFilter] = useState<PreparationState | null>(null);
  const [workerFilter, setWorkerFilter] = useState<string | null>(null);
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set());
  const [cancelTargets, setCancelTargets] = useState<AdminDownloadPreparation[] | null>(null);
  const actions = usePreparationActions();
  const items = useMemo(() => list?.items ?? [], [list]);

  const workers = useMemo(() => {
    const counts = new Map<string, { key: string; label: string; name: string; count: number }>();
    for (const item of items) {
      if (item.state !== "running") continue;
      const worker = preparationWorker(item);
      if (!worker) continue;
      const current = counts.get(worker.key);
      counts.set(worker.key, { ...worker, count: (current?.count ?? 0) + 1 });
    }
    return [...counts.values()].sort((a, b) => b.count - a.count);
  }, [items]);

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return items.filter(
      (item) =>
        (!stateFilter || item.state === stateFilter) &&
        (!workerFilter || preparationWorker(item)?.key === workerFilter) &&
        (!q || preparationSearchText(item).includes(q)),
    );
  }, [items, search, stateFilter, workerFilter]);

  if (isLoading && !list) {
    return <div className="text-muted-foreground py-8 text-sm">Loading download preparation…</div>;
  }
  if (!list) {
    return (
      <EmptyState icon={<AlertTriangle className="mb-3 h-8 w-8 opacity-20" />}>
        {isError
          ? "Download preparation is not available on this server."
          : "No downloads being prepared"}
      </EmptyState>
    );
  }

  const counts: Record<PreparationState, number> = {
    running: list.counts.running,
    queued: list.counts.queued,
    retrying: list.counts.retrying,
    paused: list.counts.paused,
    failed: list.counts.failed_recent,
  };
  const total = PREPARATION_STATES.reduce((sum, state) => sum + counts[state], 0);
  const activeFilters = [stateFilter, workerFilter].filter(Boolean).length;
  // Actions apply only to selected jobs the filters show; jobs that left the
  // list drop out of the selection on their own.
  const selectedItems = filtered.filter((item) => selected.has(item.id));
  const allVisibleSelected = filtered.length > 0 && selectedItems.length === filtered.length;
  const toggleSelected = (id: string) =>
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const selectVisible = (checked: boolean) =>
    setSelected((current) => {
      const next = new Set(current);
      for (const item of filtered) {
        if (checked) next.add(item.id);
        else next.delete(item.id);
      }
      return next;
    });
  const clearSelection = () => setSelected(new Set());
  const pausable = selectedItems.filter(canPausePreparation);
  const resumable = selectedItems.filter(canResumePreparation);
  const cancelPrompt = cancelTargets ? cancelPreparationsPrompt(cancelTargets) : null;

  return (
    <div className="space-y-5">
      {total > 0 && (
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground mb-2 text-[10px] font-semibold tracking-wider uppercase">
            State
          </div>
          <div
            role="img"
            aria-label="Download preparation states"
            className="flex h-1.5 overflow-hidden rounded-full"
          >
            {PREPARATION_STATES.filter((state) => counts[state] > 0).map((state) => (
              <div
                key={state}
                className={`transition-all duration-500 ${preparationStateMeta(state).swatchClass}`}
                style={{ width: `${(counts[state] / total) * 100}%` }}
              />
            ))}
          </div>
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1">
            {PREPARATION_STATES.map((state) => (
              <button
                key={state}
                type="button"
                aria-pressed={stateFilter === state}
                onClick={() => setStateFilter(stateFilter === state ? null : state)}
                className={cn(
                  "flex items-center gap-1.5 text-[11px] transition-opacity",
                  stateFilter && stateFilter !== state && "opacity-30",
                )}
              >
                <span
                  className={`inline-block h-2 w-2 rounded-full ${preparationStateMeta(state).swatchClass}`}
                />
                <span className="font-medium">{preparationStateMeta(state).label}</span>
                <span className="text-muted-foreground tabular-nums">
                  {counts[state].toLocaleString()}
                </span>
              </button>
            ))}
          </div>
          {workers.length > 0 && (
            <div className="border-border mt-3 border-t pt-3">
              <div className="text-muted-foreground mb-2 text-[10px] font-semibold tracking-wider uppercase">
                By Worker
              </div>
              <div className="flex flex-wrap gap-1.5">
                {workers.map((worker) => (
                  <button
                    key={worker.key}
                    type="button"
                    aria-pressed={workerFilter === worker.key}
                    onClick={() => setWorkerFilter(workerFilter === worker.key ? null : worker.key)}
                    className={cn(
                      "bg-surface border-border hover:border-primary/20 rounded-md border px-2.5 py-1 text-[11px] font-medium transition-all",
                      workerFilter === worker.key
                        ? "border-primary/40 bg-primary/10 text-primary"
                        : workerFilter && "opacity-30",
                    )}
                  >
                    <span className="text-muted-foreground mr-1">{worker.label}</span>
                    {worker.name}
                    <span className="text-muted-foreground ml-1.5 tabular-nums">
                      {worker.count}
                    </span>
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <div className="relative max-w-md min-w-[200px] flex-1">
          <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 h-3.5 w-3.5 -translate-y-1/2" />
          <Input
            placeholder="Filter by title, user, device, or worker..."
            aria-label="Filter download preparation"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-8 pl-9 text-[13px]"
          />
          {search && (
            <button
              type="button"
              aria-label="Clear filter"
              onClick={() => setSearch("")}
              className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2.5 -translate-y-1/2"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
        {activeFilters > 0 && (
          <button
            type="button"
            onClick={() => {
              setStateFilter(null);
              setWorkerFilter(null);
            }}
            className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-[11px]"
          >
            <X className="h-3 w-3" />
            Clear filters
          </button>
        )}
      </div>

      {selectedItems.length > 0 && (
        <div
          role="toolbar"
          aria-label="Selected preparation jobs"
          className="bg-surface/60 border-border flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2"
        >
          <span className="text-[12px] font-medium tabular-nums">
            {selectedItems.length.toLocaleString()} selected
          </span>
          <div className="ml-auto flex flex-wrap gap-1.5">
            <Button
              size="sm"
              variant="outline"
              className="h-7 gap-1.5 px-2 text-[11px]"
              disabled={actions.isPending || pausable.length === 0}
              onClick={() => actions.run("pause", pausable)}
            >
              <Pause className="h-3.5 w-3.5" />
              Pause{pausable.length > 0 && ` ${pausable.length.toLocaleString()}`}
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="h-7 gap-1.5 px-2 text-[11px]"
              disabled={actions.isPending || resumable.length === 0}
              onClick={() => actions.run("resume", resumable)}
            >
              <Play className="h-3.5 w-3.5" />
              Resume{resumable.length > 0 && ` ${resumable.length.toLocaleString()}`}
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="text-destructive hover:text-destructive h-7 gap-1.5 px-2 text-[11px]"
              disabled={actions.isPending}
              onClick={() => setCancelTargets(selectedItems)}
            >
              <X className="h-3.5 w-3.5" />
              Cancel {selectedItems.length.toLocaleString()}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-[11px]"
              disabled={actions.isPending}
              onClick={clearSelection}
            >
              Clear selection
            </Button>
          </div>
        </div>
      )}

      {(search || activeFilters > 0) && (
        <div className="text-muted-foreground text-[11px]">
          Showing {filtered.length} of {items.length} jobs
        </div>
      )}
      {items.length < total && (
        <div className="text-muted-foreground text-[11px]">
          Listing the first {items.length.toLocaleString()} of {total.toLocaleString()} jobs.
          Running jobs are always listed first.
        </div>
      )}

      {filtered.length === 0 ? (
        items.length > 0 ? (
          <EmptyState icon={<Filter className="mb-3 h-8 w-8 opacity-20" />}>
            No jobs match your filters
          </EmptyState>
        ) : (
          <EmptyState icon={<HardDriveDownload className="mb-3 h-8 w-8 opacity-20" />}>
            No downloads being prepared
          </EmptyState>
        )
      ) : (
        <div className="bg-card border-border overflow-hidden rounded-lg border">
          <div
            className={`border-border bg-surface/50 hidden ${GRID} items-center gap-3 border-b px-3 py-2.5 sm:grid`}
          >
            <input
              type="checkbox"
              className={CHECKBOX}
              aria-label={allVisibleSelected ? "Clear selection" : "Select all shown jobs"}
              checked={allVisibleSelected}
              ref={(element) => {
                if (element)
                  element.indeterminate = selectedItems.length > 0 && !allVisibleSelected;
              }}
              disabled={actions.isPending}
              onChange={(event) => selectVisible(event.target.checked)}
            />
            {["Title", "Output", "Worker", "Progress", "Requested by"].map((heading) => (
              <div
                key={heading}
                className="text-muted-foreground text-[10px] font-semibold tracking-wider uppercase"
              >
                {heading}
              </div>
            ))}
            <div />
          </div>
          <div className="max-h-[calc(100vh-420px)] min-h-[12rem] overflow-y-auto">
            {filtered.map((prep, i) => (
              <PreparationRow
                key={prep.id}
                prep={prep}
                even={i % 2 === 0}
                selected={selected.has(prep.id)}
                onToggleSelected={() => toggleSelected(prep.id)}
                actionsDisabled={actions.isPending}
                actionPending={actions.pendingIds?.has(prep.id) ?? false}
                onPause={() => actions.run("pause", [prep])}
                onResume={() => actions.run("resume", [prep])}
                onCancel={() => setCancelTargets([prep])}
              />
            ))}
          </div>
        </div>
      )}

      <ConfirmDialog
        open={cancelPrompt !== null}
        onOpenChange={(open) => {
          if (!open && !actions.isPending) setCancelTargets(null);
        }}
        title={cancelPrompt?.title ?? ""}
        description={cancelPrompt?.description ?? ""}
        confirmLabel={cancelPrompt?.confirmLabel}
        cancelLabel="Keep"
        variant="destructive"
        isPending={actions.isPending}
        onConfirm={() => {
          if (!cancelTargets) return;
          const ids = new Set(cancelTargets.map((t) => t.id));
          actions.run("cancel", cancelTargets, () => {
            setCancelTargets(null);
            setSelected((current) => new Set([...current].filter((id) => !ids.has(id))));
          });
        }}
      />
    </div>
  );
}

interface PreparationRowProps {
  prep: AdminDownloadPreparation;
  even: boolean;
  selected: boolean;
  onToggleSelected: () => void;
  actionsDisabled: boolean;
  actionPending: boolean;
  onPause: () => void;
  onResume: () => void;
  onCancel: () => void;
}

function PreparationRow({
  prep,
  even,
  selected,
  onToggleSelected,
  actionsDisabled,
  actionPending,
  onPause,
  onResume,
  onCancel,
}: PreparationRowProps) {
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [ffmpegOpen, setFFmpegOpen] = useState(false);
  const title = preparationTitle(prep);
  const subtitle = preparationSubtitle(prep);
  const itemHref = prep.content_id ? `/item/${prep.content_id}` : "";
  const expanded = detailsOpen || ffmpegOpen;
  const toggle = () =>
    setDetailsOpen((open) => {
      if (open) setFFmpegOpen(false);
      return !open;
    });

  const titleBlock = (
    <>
      <div className="truncate text-[13px] font-medium">{title}</div>
      <div className="text-muted-foreground truncate text-[10px]">{subtitle}</div>
      <div className="text-muted-foreground truncate text-[10px]">
        {formatPreparationSource(prep)}
      </div>
    </>
  );
  const detailsButton = (
    <button
      type="button"
      onClick={toggle}
      aria-expanded={expanded}
      aria-label={`Details for ${title}`}
      className="text-muted-foreground hover:text-primary inline-flex items-center gap-0.5 text-[10px] font-medium transition-colors"
    >
      Details
      {expanded ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
    </button>
  );

  const checkbox = (
    <input
      type="checkbox"
      className={CHECKBOX}
      aria-label={`Select ${title}`}
      checked={selected}
      disabled={actionsDisabled}
      onChange={onToggleSelected}
    />
  );
  const rowActions = (
    <RowActions
      prep={prep}
      title={title}
      disabled={actionsDisabled}
      pending={actionPending}
      onPause={onPause}
      onResume={onResume}
      onCancel={onCancel}
    />
  );

  return (
    <div
      className={cn(
        "border-border/30 hover:bg-surface/60 border-b transition-colors duration-100",
        !even && "bg-surface/20",
        selected && "bg-primary/5",
      )}
    >
      {/* Desktop row */}
      <div className={`hidden ${GRID} items-center gap-3 px-3 py-2.5 sm:grid`}>
        {checkbox}
        <div className="min-w-0">
          {itemHref ? (
            <Link to={itemHref} className="hover:text-primary block min-w-0 transition-colors">
              {titleBlock}
            </Link>
          ) : (
            titleBlock
          )}
        </div>
        <div className="flex min-w-0 items-center gap-1.5">
          <FormatBadge format={prep.format} />
          <span className="line-clamp-2 text-[11px] font-medium">
            {formatPreparationOutput(prep)}
          </span>
        </div>
        <div className="min-w-0">
          <WorkerBadge prep={prep} />
        </div>
        <ProgressCell prep={prep} />
        <Requesters prep={prep} />
        <div className="flex items-center justify-end gap-1">
          {rowActions}
          {detailsButton}
        </div>
      </div>

      {/* Mobile card */}
      <div className="space-y-2 px-4 py-3 sm:hidden">
        <div className="flex items-start justify-between gap-2">
          <div className="mt-0.5 shrink-0">{checkbox}</div>
          <div className="min-w-0 flex-1">
            <div className="truncate text-[13px] font-semibold">{title}</div>
            <div className="text-muted-foreground truncate text-[11px]">{subtitle}</div>
          </div>
          <FormatBadge format={prep.format} />
        </div>
        <ProgressCell prep={prep} />
        <div className="text-muted-foreground flex min-w-0 flex-wrap items-center gap-1.5 text-[10px]">
          <WorkerBadge prep={prep} />
          <span className="truncate">{formatPreparationOutput(prep)}</span>
        </div>
        <div className="text-muted-foreground flex items-center justify-between gap-2 text-[10px]">
          <span className="truncate">{requesterSummary(prep)}</span>
          <div className="flex shrink-0 items-center gap-1">
            {rowActions}
            {detailsButton}
          </div>
        </div>
      </div>

      {expanded && (
        <PreparationDetails
          prep={prep}
          showFFmpeg={ffmpegOpen}
          onToggleFFmpeg={() => setFFmpegOpen((open) => !open)}
        />
      )}
    </div>
  );
}

function RowActions({
  prep,
  title,
  disabled,
  pending,
  onPause,
  onResume,
  onCancel,
}: {
  prep: AdminDownloadPreparation;
  title: string;
  disabled: boolean;
  pending: boolean;
  onPause: () => void;
  onResume: () => void;
  onCancel: () => void;
}) {
  const iconButton =
    "text-muted-foreground inline-flex h-6 w-6 items-center justify-center rounded transition-colors disabled:pointer-events-none disabled:opacity-40";
  if (pending) {
    return (
      <Loader className="text-muted-foreground h-3.5 w-3.5 animate-spin" aria-label="Working" />
    );
  }
  return (
    <>
      {canPausePreparation(prep) && (
        <button
          type="button"
          className={cn(iconButton, "hover:bg-surface hover:text-foreground")}
          aria-label={`Pause ${title}`}
          title={
            prep.state === "running"
              ? "Pause: stops the encode, which starts over when resumed"
              : "Pause: keeps its place in the queue without starting"
          }
          disabled={disabled}
          onClick={onPause}
        >
          <Pause className="h-3.5 w-3.5" />
        </button>
      )}
      {canResumePreparation(prep) && (
        <button
          type="button"
          className={cn(iconButton, "hover:bg-surface hover:text-foreground")}
          aria-label={`Resume ${title}`}
          title="Resume"
          disabled={disabled}
          onClick={onResume}
        >
          <Play className="h-3.5 w-3.5" />
        </button>
      )}
      <button
        type="button"
        className={cn(iconButton, "hover:bg-destructive/10 hover:text-destructive")}
        aria-label={prep.state === "failed" ? `Remove ${title}` : `Cancel ${title}`}
        title={prep.state === "failed" ? "Remove this failed job" : "Cancel"}
        disabled={disabled}
        onClick={onCancel}
      >
        <X className="h-3.5 w-3.5" />
      </button>
    </>
  );
}

function FormatBadge({ format }: { format: AdminDownloadPreparation["format"] }) {
  // Match the streams table: remux shares the Remux swatch, transcode the Transcode one.
  const meta = activityMethodMeta(format);
  return (
    <span
      className={`inline-flex shrink-0 rounded border px-1.5 py-0.5 text-[9px] font-semibold ${meta.badgeClass}`}
    >
      {meta.label}
    </span>
  );
}

function WorkerBadge({ prep }: { prep: AdminDownloadPreparation }) {
  const worker = preparationWorker(prep);
  if (!worker) {
    return <span className="text-muted-foreground text-[11px]">Not assigned</span>;
  }
  return (
    <span className="border-primary/20 bg-primary/10 text-primary inline-flex max-w-full items-center gap-1 rounded border px-1.5 py-0.5 text-[9px] font-semibold">
      <span className="opacity-70">{worker.label}</span>
      <span className="truncate">{worker.name}</span>
    </span>
  );
}

function ProgressCell({ prep }: { prep: AdminDownloadPreparation }) {
  switch (prep.state) {
    case "running": {
      const percent = preparationPercent(prep);
      if (percent == null) {
        const elapsed = secondsSince(prep.started_at);
        return (
          <div className="space-y-0.5">
            <div className="text-primary flex items-center gap-1 text-[11px] font-semibold">
              <Loader className="h-3 w-3 animate-spin" aria-hidden="true" />
              {preparationRunningLabel(prep)}
              {elapsed != null && ` · ${formatClock(elapsed)}`}
            </div>
            <div className="text-muted-foreground text-[10px]">
              {prep.progress_unavailable
                ? "This worker doesn't report progress"
                : "Waiting for the first progress report"}
            </div>
          </div>
        );
      }
      const detail = [
        formatSpeed(prep.progress?.speed),
        formatRemaining(preparationRemainingSeconds(prep)),
      ]
        .filter(Boolean)
        .join(" · ");
      return (
        <div className="space-y-1">
          <div className="flex items-center justify-between gap-2 text-[11px]">
            <span className="font-semibold tabular-nums">{Math.floor(percent)}%</span>
            <span className="text-muted-foreground truncate tabular-nums">{detail}</span>
          </div>
          <div
            className="bg-muted h-1.5 overflow-hidden rounded-full"
            role="progressbar"
            aria-label={`${preparationTitle(prep)} progress`}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.floor(percent)}
          >
            <div
              className="bg-primary h-full rounded-full transition-[width] duration-300 ease-out"
              style={{ width: `${percent}%` }}
            />
          </div>
        </div>
      );
    }
    case "queued":
      return (
        <div className="text-muted-foreground flex items-center gap-1.5 text-[11px]">
          <Clock className="h-3 w-3" aria-hidden="true" />
          Queued ·{" "}
          {prep.queue_position === 1
            ? "next in line"
            : prep.queue_position
              ? `#${prep.queue_position.toLocaleString()} in line`
              : "waiting"}
        </div>
      );
    case "retrying":
      return (
        <div className="min-w-0 space-y-0.5">
          <div className="text-warning flex items-center gap-1.5 text-[11px] font-semibold">
            <RotateCw className="h-3 w-3" aria-hidden="true" />
            Waiting to retry
          </div>
          <div className="text-muted-foreground line-clamp-2 text-[10px]">
            {[
              `Attempt ${prep.attempts + 1} of ${prep.max_attempts} ${formatRetryIn(prep.next_retry_at)}`,
              prep.error,
            ]
              .filter(Boolean)
              .join(" · ")}
          </div>
        </div>
      );
    case "paused":
      return (
        <div className="min-w-0 space-y-0.5">
          <div className="text-muted-foreground flex items-center gap-1.5 text-[11px] font-semibold">
            <Pause className="h-3 w-3" aria-hidden="true" />
            Paused
          </div>
          <div className="text-muted-foreground text-[10px]">
            {[formatAgo(prep.paused_at), "Won't start until resumed"].filter(Boolean).join(" · ")}
          </div>
        </div>
      );
    case "failed":
      return (
        <div className="min-w-0 space-y-0.5">
          <div className="text-destructive flex items-center gap-1.5 text-[11px] font-semibold">
            <AlertTriangle className="h-3 w-3" aria-hidden="true" />
            Failed
          </div>
          <div className="text-muted-foreground line-clamp-2 text-[10px]" title={prep.error}>
            {[
              `After ${prep.attempts} ${prep.attempts === 1 ? "attempt" : "attempts"}`,
              formatAgo(prep.failed_at),
              prep.error,
            ]
              .filter(Boolean)
              .join(" · ")}
          </div>
        </div>
      );
  }
}

function requesterSummary(prep: AdminDownloadPreparation): string {
  const [first, ...rest] = prep.requesters;
  if (!first) return "No waiting downloads";
  return [requesterName(first), requesterDevice(first), rest.length > 0 ? `+${rest.length}` : ""]
    .filter(Boolean)
    .join(" · ");
}

function Requesters({ prep }: { prep: AdminDownloadPreparation }) {
  const [first, ...rest] = prep.requesters;
  if (!first) {
    return <span className="text-muted-foreground text-[11px]">No waiting downloads</span>;
  }
  return (
    <div className="min-w-0 text-[12px]">
      <div className="flex min-w-0 items-center gap-1.5">
        <Link
          to={`/admin/users/${first.user_id}`}
          className="hover:text-primary truncate font-medium transition-colors"
        >
          {requesterName(first)}
        </Link>
        {first.profile_name && (
          <span className="border-primary/30 bg-primary/15 text-primary max-w-[6rem] truncate rounded border px-1.5 py-0.5 text-[10px] leading-none">
            {first.profile_name}
          </span>
        )}
      </div>
      <div className="text-muted-foreground truncate text-[10px]">
        {requesterDevice(first)}
        {rest.length > 0 && ` · +${rest.length} more`}
      </div>
    </div>
  );
}

function PreparationDetails({
  prep,
  showFFmpeg,
  onToggleFFmpeg,
}: {
  prep: AdminDownloadPreparation;
  showFFmpeg: boolean;
  onToggleFFmpeg: () => void;
}) {
  const logsHref = `/admin/logs?playback_session_id=${encodeURIComponent(prep.log_session_id)}&component=ffmpeg`;
  const ffmpegLogs = useOperationalLogs(
    { playback_session_id: prep.log_session_id, component: "ffmpeg", limit: 12 },
    showFFmpeg,
  );
  const rows = ffmpegLogs.data?.entries ?? [];
  const worker = preparationWorker(prep);
  const toneMap = formatPreparationToneMap(prep);
  const percent = preparationPercent(prep);
  const elapsed = secondsSince(prep.started_at);
  const startedAt = prep.started_at ? formatTimeOnly(prep.started_at) : "";

  return (
    <div className="terminal-surface border-border/50 bg-card border-t px-4 py-3">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <div className="rounded-full border border-[var(--terminal-border)] bg-[var(--terminal-bg)] px-2 py-0.5 text-[10px] font-semibold tracking-[0.2em] text-[var(--terminal-fg)] uppercase">
              Preparation
            </div>
            <div className="text-foreground/85 text-[11px] font-medium">
              Job details{showFFmpeg ? " and FFmpeg output" : ""}
            </div>
          </div>
          <div className="text-muted-foreground mt-1 font-mono text-[10px]">{prep.id}</div>
        </div>
        <div className="flex gap-1.5">
          <Button
            variant="outline"
            size="sm"
            className="h-7 gap-1.5 px-2 text-[11px]"
            onClick={onToggleFFmpeg}
            aria-pressed={showFFmpeg}
          >
            <Terminal className="h-3.5 w-3.5" />
            {showFFmpeg ? "Hide FFmpeg" : "FFmpeg"}
          </Button>
          <Button variant="outline" size="sm" className="h-7 gap-1.5 px-2 text-[11px]" asChild>
            <Link to={logsHref}>
              <FileText className="h-3.5 w-3.5" />
              View logs
            </Link>
          </Button>
        </div>
      </div>

      <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
        <DetailCard label="Source">
          <DetailLine label="Container" value={prep.source.container?.toUpperCase() || "—"} />
          <DetailLine
            label="Video"
            value={
              [
                prep.source.resolution,
                formatCodecLabel(prep.source.video_codec, ""),
                prep.source.hdr ? "HDR" : "",
                formatPreparationBitrate(prep.source.bitrate_kbps),
              ]
                .filter(Boolean)
                .join(" · ") || "—"
            }
          />
          <DetailLine label="Audio" value={formatPreparationSourceAudio(prep)} />
          <DetailLine
            label="Size"
            value={
              [
                formatFileSize(prep.source.file_size),
                prep.source.duration_seconds > 0 ? formatClock(prep.source.duration_seconds) : "",
              ]
                .filter(Boolean)
                .join(" · ") || "—"
            }
            muted
          />
        </DetailCard>
        <DetailCard label="Output" badge={<FormatBadge format={prep.format} />}>
          <DetailLine label="Container" value={`${prep.output.container.toUpperCase()}`} />
          <DetailLine label="Video" value={formatPreparationVideoOutput(prep)} />
          {toneMap && <DetailLine label="Tone map" value={toneMap} />}
          <DetailLine label="Audio" value={formatPreparationAudioOutput(prep)} />
        </DetailCard>
        <DetailCard label="Job">
          <DetailLine label="Status" value={jobStatus(prep)} />
          {prep.state === "running" && prep.progress && (
            <DetailLine
              label="Encoded"
              value={
                prep.progress.duration_seconds > 0
                  ? `${formatClock(prep.progress.encoded_seconds)} of ${formatClock(prep.progress.duration_seconds)}${percent != null ? ` (${Math.floor(percent)}%)` : ""}`
                  : formatClock(prep.progress.encoded_seconds)
              }
            />
          )}
          {prep.state === "running" && prep.progress && prep.progress.speed > 0 && (
            <DetailLine
              label="Speed"
              value={[
                `${formatSpeed(prep.progress.speed)} realtime`,
                formatRemaining(preparationRemainingSeconds(prep)),
              ]
                .filter(Boolean)
                .join(" · ")}
            />
          )}
          {startedAt && (
            <DetailLine
              label={prep.state === "running" ? "Started" : "Last try"}
              value={
                prep.state === "running" && elapsed != null
                  ? `${startedAt} · ${formatClock(elapsed)} ago`
                  : startedAt
              }
            />
          )}
          <DetailLine
            label="Worker"
            value={worker ? `${worker.label} ${worker.name}` : "Not assigned yet"}
            muted
          />
          {prep.error && <DetailLine label="Error" value={prep.error} muted />}
        </DetailCard>
        <DetailCard label="Requested by">
          {prep.requesters.length === 0 ? (
            <span className="text-[var(--terminal-muted)]">No waiting downloads</span>
          ) : (
            prep.requesters.map((r, i) => (
              <div
                key={`${r.user_id}-${r.device_id ?? "web"}-${i}`}
                className="grid min-w-0 grid-cols-[1fr_auto] gap-2"
              >
                <span className="min-w-0 truncate font-medium text-[var(--terminal-fg)]">
                  {requesterName(r)}
                  {r.profile_name ? ` · ${r.profile_name}` : ""}
                  <span className="block truncate text-[10px] font-normal text-[var(--terminal-muted)]">
                    {requesterDevice(r)}
                  </span>
                </span>
                <span className="text-[10px] text-[var(--terminal-muted)] capitalize">
                  {r.status === "preparing" ? "Waiting" : r.status}
                </span>
              </div>
            ))
          )}
        </DetailCard>
      </div>

      {showFFmpeg && (
        <FFmpegConsole
          rows={rows}
          isLoading={ffmpegLogs.isLoading}
          isFetching={ffmpegLogs.isFetching}
        />
      )}
    </div>
  );
}

function jobStatus(prep: AdminDownloadPreparation): string {
  const attempt = `attempt ${Math.max(prep.attempts, 1)} of ${prep.max_attempts}`;
  switch (prep.state) {
    case "running":
      return `${preparationRunningLabel(prep)} · ${attempt}`;
    case "queued":
      return prep.queue_position ? `Queued · #${prep.queue_position} in line` : "Queued";
    case "retrying":
      return `Retrying ${formatRetryIn(prep.next_retry_at)} · attempt ${prep.attempts + 1} of ${prep.max_attempts}`;
    case "paused":
      return `Paused ${formatAgo(prep.paused_at)}`.trim();
    case "failed":
      return `Failed ${formatAgo(prep.failed_at)}`.trim();
  }
}

function FFmpegConsole({
  rows,
  isLoading,
  isFetching,
}: {
  rows: OperationalLogEntry[];
  isLoading: boolean;
  isFetching: boolean;
}) {
  return (
    <div className="mt-3 space-y-2">
      {isFetching && !isLoading && (
        <div className="text-muted-foreground text-right text-[10px]">Refreshing…</div>
      )}
      <div className="overflow-hidden rounded-xl border border-[var(--terminal-border)] bg-[var(--terminal-bg)] shadow-[0_18px_60px_rgba(0,0,0,0.35)]">
        {isLoading ? (
          <div className="px-4 py-6 font-mono text-[11px] text-[var(--terminal-muted)]">
            Loading ffmpeg output…
          </div>
        ) : rows.length === 0 ? (
          <div className="px-4 py-6 font-mono text-[11px] text-[var(--terminal-muted)]">
            No FFmpeg output for this job yet. Output appears once an attempt starts; workers
            running an older version do not send it.
          </div>
        ) : (
          <div className="max-h-64 overflow-y-auto">
            {rows.map((row) => (
              <div
                key={row.id}
                className="grid grid-cols-[120px_1fr] gap-3 border-b border-[var(--terminal-border)]/30 px-4 py-2.5 last:border-b-0"
              >
                <div className="space-y-1">
                  <div className="font-mono text-[10px] text-[var(--terminal-muted)]">
                    {formatTimeOnly(row.timestamp)}
                  </div>
                  <div className="text-[10px] tracking-[0.18em] text-[var(--terminal-muted)]/60 uppercase">
                    {row.message.includes("stderr") ? "stderr" : "event"}
                  </div>
                </div>
                <div className="min-w-0">
                  <div className="font-mono text-[11px] leading-5 break-words text-[var(--terminal-fg)]">
                    {ffmpegRowText(row)}
                  </div>
                  {row.node_id && (
                    <div className="mt-1 text-[10px] text-[var(--terminal-muted)]/60">
                      {row.node_id}
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function DetailCard({
  label,
  badge,
  children,
}: {
  label: string;
  badge?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="rounded-lg border border-[var(--terminal-border)]/60 bg-[var(--terminal-bg)]/60 px-3 py-2">
      <div className="mb-2 flex items-center gap-2">
        <span className="text-[10px] font-semibold tracking-[0.18em] text-[var(--terminal-muted)] uppercase">
          {label}
        </span>
        {badge}
      </div>
      <div className="grid gap-1 text-[11px]">{children}</div>
    </div>
  );
}

function DetailLine({
  label,
  value,
  muted = false,
}: {
  label: string;
  value: string;
  muted?: boolean;
}) {
  return (
    <div className="grid min-w-0 grid-cols-[4.25rem_1fr] gap-2">
      <span className="text-[10px] text-[var(--terminal-muted)]">{label}</span>
      <span
        className={cn(
          "min-w-0 font-medium break-words",
          muted ? "text-[var(--terminal-muted)]" : "text-[var(--terminal-fg)]",
        )}
      >
        {value}
      </span>
    </div>
  );
}

function EmptyState({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <div className="text-muted-foreground flex flex-col items-center justify-center py-20 text-sm">
      {icon}
      <span>{children}</span>
    </div>
  );
}

function ffmpegRowText(entry: OperationalLogEntry) {
  const line = entry.attrs?.ffmpeg_line;
  if (typeof line === "string" && line) return line;
  const event = entry.attrs?.ffmpeg_event;
  if (typeof event === "string" && event) return event;
  return entry.message;
}

function formatTimeOnly(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return formatTime(date, { second: "2-digit" });
}
