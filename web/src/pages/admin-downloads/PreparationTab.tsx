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
} from "@/api/v2/adminDownloadPreparations";
import type { OperationalLogEntry } from "@/api/types";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  useAdminDownloadPreparationAction,
  useAdminDownloadPreparations,
} from "@/hooks/queries/admin/downloadPreparations";
import { useOperationalLogs } from "@/hooks/queries/admin/logs";
import { useAppLanguage, useT, type MessageKey, type Translate } from "@/i18n";
import { formatTime } from "@/lib/datetime";
import { formatCodecLabel, formatFileSize } from "@/lib/mediaFormat";
import { cn } from "@/lib/utils";
import { activityMethodMeta } from "@/pages/adminActivityPresentation";
import { CHECKBOX } from "./controls";
import {
  cancelPreparationsPrompt,
  canPausePreparation,
  canResumePreparation,
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
  jobCount,
  PREPARATION_STATES,
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
import { formatLocale } from "@/lib/appLocale";

const K = "web.admin.downloads.preparation";
// Product and stream names, not translated.
const FFMPEG = "FFmpeg";
const STDERR = "stderr";

const ACTION_FAILED: Record<AdminDownloadPreparationAction, MessageKey> = {
  pause: `${K}.actionFailed.pause`,
  resume: `${K}.actionFailed.resume`,
  cancel: `${K}.actionFailed.cancel`,
};

const COLUMNS: readonly MessageKey[] = [
  `${K}.column.title`,
  `${K}.column.output`,
  `${K}.column.worker`,
  `${K}.column.progress`,
  `${K}.column.requestedBy`,
];

const GRID =
  "grid-cols-[16px_minmax(180px,1.6fr)_minmax(190px,1.5fr)_minmax(100px,0.8fr)_minmax(200px,1.5fr)_minmax(150px,1fr)_112px]";

/** Runs one administrator action and reports its outcome in a toast. */
function usePreparationActions() {
  const t = useT();
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
          toast.error(t(ACTION_FAILED[action]));
        },
      },
    );
  };
  const pendingIds = mutation.isPending ? new Set(mutation.variables?.ids) : null;
  return { run, isPending: mutation.isPending, pendingIds };
}

/** The preparation queue: running, queued, retrying and paused jobs, and recent failures. */
export default function PreparationTab() {
  const t = useT();
  // Rows show text from the presentation helpers; re-render them on a language change.
  useAppLanguage();
  const { data: list, isLoading, isError } = useAdminDownloadPreparations();
  const [search, setSearch] = useState("");
  const [stateFilter, setStateFilter] = useState<PreparationState | null>(null);
  const [workerFilter, setWorkerFilter] = useState<string | null>(null);
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set());
  const [cancelTargets, setCancelTargets] = useState<AdminDownloadPreparation[] | null>(null);
  const actions = usePreparationActions();
  const items = useMemo(() => list?.items ?? [], [list]);

  // Not memoized: the labels and search text are translated, so they follow the language.
  const workers = (() => {
    const counts = new Map<string, { key: string; label: string; name: string; count: number }>();
    for (const item of items) {
      if (item.state !== "running") continue;
      const worker = preparationWorker(item);
      if (!worker) continue;
      const current = counts.get(worker.key);
      counts.set(worker.key, { ...worker, count: (current?.count ?? 0) + 1 });
    }
    return [...counts.values()].sort((a, b) => b.count - a.count);
  })();

  const q = search.trim().toLowerCase();
  const filtered = items.filter(
    (item) =>
      (!stateFilter || item.state === stateFilter) &&
      (!workerFilter || preparationWorker(item)?.key === workerFilter) &&
      (!q || preparationSearchText(item).includes(q)),
  );

  if (isLoading && !list) {
    return <div className="text-muted-foreground py-8 text-sm">{t(`${K}.loading`)}</div>;
  }
  if (!list) {
    return (
      <EmptyState icon={<AlertTriangle className="mb-3 h-8 w-8 opacity-20" />}>
        {isError ? t(`${K}.unavailable`) : t(`${K}.empty`)}
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
            {t(`${K}.stateHeading`)}
          </div>
          <div
            role="img"
            aria-label={t(`${K}.stateBarLabel`)}
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
                  {counts[state].toLocaleString(formatLocale())}
                </span>
              </button>
            ))}
          </div>
          {workers.length > 0 && (
            <div className="border-border mt-3 border-t pt-3">
              <div className="text-muted-foreground mb-2 text-[10px] font-semibold tracking-wider uppercase">
                {t(`${K}.byWorker`)}
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
                    <span className="text-muted-foreground me-1">{worker.label}</span>
                    {worker.name}
                    <span className="text-muted-foreground ms-1.5 tabular-nums">
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
          <Search className="text-muted-foreground pointer-events-none absolute start-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2" />
          <Input
            placeholder={t(`${K}.filter.placeholder`)}
            aria-label={t(`${K}.filter.label`)}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-8 ps-9 text-[13px]"
          />
          {search && (
            <button
              type="button"
              aria-label={t(`${K}.filter.clear`)}
              onClick={() => setSearch("")}
              className="text-muted-foreground hover:text-foreground absolute end-2.5 top-1/2 -translate-y-1/2"
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
            {t(`${K}.filter.clearAll`)}
          </button>
        )}
      </div>

      {selectedItems.length > 0 && (
        <div
          role="toolbar"
          aria-label={t(`${K}.selection.toolbarLabel`)}
          className="bg-surface/60 border-border flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2"
        >
          <span className="text-[12px] font-medium tabular-nums">
            {t(`${K}.selection.count`, jobCount(selectedItems.length))}
          </span>
          <div className="ms-auto flex flex-wrap gap-1.5">
            <Button
              size="sm"
              variant="outline"
              className="h-7 gap-1.5 px-2 text-[11px]"
              disabled={actions.isPending || pausable.length === 0}
              onClick={() => actions.run("pause", pausable)}
            >
              <Pause className="h-3.5 w-3.5" />
              {pausable.length > 0
                ? t(`${K}.selection.pauseCount`, jobCount(pausable.length))
                : t(`${K}.selection.pause`)}
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="h-7 gap-1.5 px-2 text-[11px]"
              disabled={actions.isPending || resumable.length === 0}
              onClick={() => actions.run("resume", resumable)}
            >
              <Play className="h-3.5 w-3.5" />
              {resumable.length > 0
                ? t(`${K}.selection.resumeCount`, jobCount(resumable.length))
                : t(`${K}.selection.resume`)}
            </Button>
            <Button
              size="sm"
              variant="outline"
              className="text-destructive hover:text-destructive h-7 gap-1.5 px-2 text-[11px]"
              disabled={actions.isPending}
              onClick={() => setCancelTargets(selectedItems)}
            >
              <X className="h-3.5 w-3.5" />
              {t(`${K}.selection.cancelCount`, jobCount(selectedItems.length))}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-[11px]"
              disabled={actions.isPending}
              onClick={clearSelection}
            >
              {t(`${K}.selection.clear`)}
            </Button>
          </div>
        </div>
      )}

      {(search || activeFilters > 0) && (
        <div className="text-muted-foreground text-[11px]">
          {t(`${K}.showing`, {
            total: items.length,
            shown: String(filtered.length),
            total_text: String(items.length),
          })}
        </div>
      )}
      {items.length < total && (
        <div className="text-muted-foreground text-[11px]">
          {t(`${K}.listingFirst`, {
            total,
            shown: items.length.toLocaleString(formatLocale()),
            total_text: total.toLocaleString(formatLocale()),
          })}
        </div>
      )}

      {filtered.length === 0 ? (
        items.length > 0 ? (
          <EmptyState icon={<Filter className="mb-3 h-8 w-8 opacity-20" />}>
            {t(`${K}.noMatches`)}
          </EmptyState>
        ) : (
          <EmptyState icon={<HardDriveDownload className="mb-3 h-8 w-8 opacity-20" />}>
            {t(`${K}.empty`)}
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
              aria-label={
                allVisibleSelected ? t(`${K}.table.clearSelection`) : t(`${K}.table.selectAll`)
              }
              checked={allVisibleSelected}
              ref={(element) => {
                if (element)
                  element.indeterminate = selectedItems.length > 0 && !allVisibleSelected;
              }}
              disabled={actions.isPending}
              onChange={(event) => selectVisible(event.target.checked)}
            />
            {COLUMNS.map((heading) => (
              <div
                key={heading}
                className="text-muted-foreground text-[10px] font-semibold tracking-wider uppercase"
              >
                {t(heading)}
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
        cancelLabel={t(`${K}.cancelDialog.keep`)}
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
  const t = useT();
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
      aria-label={t(`${K}.row.detailsLabel`, { title })}
      className="text-muted-foreground hover:text-primary inline-flex items-center gap-0.5 text-[10px] font-medium transition-colors"
    >
      {t(`${K}.row.details`)}
      {expanded ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
    </button>
  );

  const checkbox = (
    <input
      type="checkbox"
      className={CHECKBOX}
      aria-label={t(`${K}.row.select`, { title })}
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
          <span className="truncate">{requesterSummary(t, prep)}</span>
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
  const t = useT();
  const iconButton =
    "text-muted-foreground inline-flex h-6 w-6 items-center justify-center rounded transition-colors disabled:pointer-events-none disabled:opacity-40";
  if (pending) {
    return (
      <Loader
        className="text-muted-foreground h-3.5 w-3.5 animate-spin"
        aria-label={t(`${K}.row.working`)}
      />
    );
  }
  return (
    <>
      {canPausePreparation(prep) && (
        <button
          type="button"
          className={cn(iconButton, "hover:bg-surface hover:text-foreground")}
          aria-label={t(`${K}.row.pauseLabel`, { title })}
          title={
            prep.state === "running"
              ? t(`${K}.row.pauseRunningTitle`)
              : t(`${K}.row.pauseWaitingTitle`)
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
          aria-label={t(`${K}.row.resumeLabel`, { title })}
          title={t(`${K}.row.resumeTitle`)}
          disabled={disabled}
          onClick={onResume}
        >
          <Play className="h-3.5 w-3.5" />
        </button>
      )}
      <button
        type="button"
        className={cn(iconButton, "hover:bg-destructive/10 hover:text-destructive")}
        aria-label={
          prep.state === "failed"
            ? t(`${K}.row.removeLabel`, { title })
            : t(`${K}.row.cancelLabel`, { title })
        }
        title={prep.state === "failed" ? t(`${K}.row.removeTitle`) : t(`${K}.row.cancelTitle`)}
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
  const t = useT();
  const worker = preparationWorker(prep);
  if (!worker) {
    return <span className="text-muted-foreground text-[11px]">{t(`${K}.notAssigned`)}</span>;
  }
  return (
    <span className="border-primary/20 bg-primary/10 text-primary inline-flex max-w-full items-center gap-1 rounded border px-1.5 py-0.5 text-[9px] font-semibold">
      <span className="opacity-70">{worker.label}</span>
      <span className="truncate">{worker.name}</span>
    </span>
  );
}

function ProgressCell({ prep }: { prep: AdminDownloadPreparation }) {
  const t = useT();
  switch (prep.state) {
    case "running": {
      const percent = preparationPercent(prep);
      if (percent == null) {
        const elapsed = secondsSince(prep.started_at);
        return (
          <div className="space-y-0.5">
            <div className="text-primary flex items-center gap-1 text-[11px] font-semibold">
              <Loader className="h-3 w-3 animate-spin" aria-hidden="true" />
              {elapsed != null
                ? t(`${K}.progress.runningElapsed`, {
                    label: preparationRunningLabel(prep),
                    elapsed: formatClock(elapsed),
                  })
                : preparationRunningLabel(prep)}
            </div>
            <div className="text-muted-foreground text-[10px]">
              {prep.progress_unavailable
                ? t(`${K}.progress.noReports`)
                : t(`${K}.progress.waitingFirstReport`)}
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
            <span className="font-semibold tabular-nums">
              {t(`${K}.progress.percent`, { percent: String(Math.floor(percent)) })}
            </span>
            <span className="text-muted-foreground truncate tabular-nums">{detail}</span>
          </div>
          <div
            className="bg-muted h-1.5 overflow-hidden rounded-full"
            role="progressbar"
            aria-label={t(`${K}.progress.label`, { title: preparationTitle(prep) })}
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
          {prep.queue_position === 1
            ? t(`${K}.progress.queuedNext`)
            : prep.queue_position
              ? t(`${K}.progress.queuedPosition`, {
                  position: prep.queue_position.toLocaleString(formatLocale()),
                })
              : t(`${K}.progress.queuedWaiting`)}
        </div>
      );
    case "retrying":
      return (
        <div className="min-w-0 space-y-0.5">
          <div className="text-warning flex items-center gap-1.5 text-[11px] font-semibold">
            <RotateCw className="h-3 w-3" aria-hidden="true" />
            {t(`${K}.progress.waitingToRetry`)}
          </div>
          <div className="text-muted-foreground line-clamp-2 text-[10px]">
            {[
              t(`${K}.progress.attempt`, {
                attempt: String(prep.attempts + 1),
                max: String(prep.max_attempts),
                retry_in: formatRetryIn(prep.next_retry_at),
              }),
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
            {t(`${K}.progress.paused`)}
          </div>
          <div className="text-muted-foreground text-[10px]">
            {[formatAgo(prep.paused_at), t(`${K}.progress.wontStart`)].filter(Boolean).join(" · ")}
          </div>
        </div>
      );
    case "failed":
      return (
        <div className="min-w-0 space-y-0.5">
          <div className="text-destructive flex items-center gap-1.5 text-[11px] font-semibold">
            <AlertTriangle className="h-3 w-3" aria-hidden="true" />
            {t(`${K}.progress.failed`)}
          </div>
          <div className="text-muted-foreground line-clamp-2 text-[10px]" title={prep.error}>
            {[
              t(`${K}.progress.afterAttempts`, { count: prep.attempts }),
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

function requesterSummary(t: Translate, prep: AdminDownloadPreparation): string {
  const [first, ...rest] = prep.requesters;
  if (!first) return t(`${K}.requesters.none`);
  return [requesterName(first), requesterDevice(first), rest.length > 0 ? `+${rest.length}` : ""]
    .filter(Boolean)
    .join(" · ");
}

function Requesters({ prep }: { prep: AdminDownloadPreparation }) {
  const t = useT();
  const [first, ...rest] = prep.requesters;
  if (!first) {
    return <span className="text-muted-foreground text-[11px]">{t(`${K}.requesters.none`)}</span>;
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
        {rest.length > 0
          ? t(`${K}.requesters.deviceMore`, {
              count: rest.length,
              count_text: String(rest.length),
              device: requesterDevice(first),
            })
          : requesterDevice(first)}
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
  const t = useT();
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
              {t(`${K}.details.chip`)}
            </div>
            <div className="text-foreground/85 text-[11px] font-medium">
              {showFFmpeg ? t(`${K}.details.headingWithFfmpeg`) : t(`${K}.details.heading`)}
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
            {showFFmpeg ? t(`${K}.details.hideFfmpeg`) : FFMPEG}
          </Button>
          <Button variant="outline" size="sm" className="h-7 gap-1.5 px-2 text-[11px]" asChild>
            <Link to={logsHref}>
              <FileText className="h-3.5 w-3.5" />
              {t(`${K}.details.viewLogs`)}
            </Link>
          </Button>
        </div>
      </div>

      <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
        <DetailCard label={t(`${K}.details.source`)}>
          <DetailLine
            label={t(`${K}.details.container`)}
            value={prep.source.container?.toUpperCase() || "—"}
          />
          <DetailLine
            label={t(`${K}.details.video`)}
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
          <DetailLine label={t(`${K}.details.audio`)} value={formatPreparationSourceAudio(prep)} />
          <DetailLine
            label={t(`${K}.details.size`)}
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
        <DetailCard label={t(`${K}.details.output`)} badge={<FormatBadge format={prep.format} />}>
          <DetailLine
            label={t(`${K}.details.container`)}
            value={`${prep.output.container.toUpperCase()}`}
          />
          <DetailLine label={t(`${K}.details.video`)} value={formatPreparationVideoOutput(prep)} />
          {toneMap && <DetailLine label={t(`${K}.details.toneMap`)} value={toneMap} />}
          <DetailLine label={t(`${K}.details.audio`)} value={formatPreparationAudioOutput(prep)} />
        </DetailCard>
        <DetailCard label={t(`${K}.details.job`)}>
          <DetailLine label={t(`${K}.details.status`)} value={jobStatus(t, prep)} />
          {prep.state === "running" && prep.progress && (
            <DetailLine
              label={t(`${K}.details.encoded`)}
              value={
                prep.progress.duration_seconds > 0
                  ? percent != null
                    ? t(`${K}.details.encodedOfPercent`, {
                        encoded: formatClock(prep.progress.encoded_seconds),
                        duration: formatClock(prep.progress.duration_seconds),
                        percent: String(Math.floor(percent)),
                      })
                    : t(`${K}.details.encodedOf`, {
                        encoded: formatClock(prep.progress.encoded_seconds),
                        duration: formatClock(prep.progress.duration_seconds),
                      })
                  : formatClock(prep.progress.encoded_seconds)
              }
            />
          )}
          {prep.state === "running" && prep.progress && prep.progress.speed > 0 && (
            <DetailLine
              label={t(`${K}.details.speed`)}
              value={[
                t(`${K}.details.speedRealtime`, { speed: formatSpeed(prep.progress.speed) }),
                formatRemaining(preparationRemainingSeconds(prep)),
              ]
                .filter(Boolean)
                .join(" · ")}
            />
          )}
          {startedAt && (
            <DetailLine
              label={
                prep.state === "running" ? t(`${K}.details.started`) : t(`${K}.details.lastTry`)
              }
              value={
                prep.state === "running" && elapsed != null
                  ? t(`${K}.details.startedAgo`, { time: startedAt, elapsed: formatClock(elapsed) })
                  : startedAt
              }
            />
          )}
          <DetailLine
            label={t(`${K}.details.worker`)}
            value={
              worker
                ? t(`${K}.details.workerValue`, { kind: worker.label, name: worker.name })
                : t(`${K}.details.notAssignedYet`)
            }
            muted
          />
          {prep.error && <DetailLine label={t(`${K}.details.error`)} value={prep.error} muted />}
        </DetailCard>
        <DetailCard label={t(`${K}.details.requestedBy`)}>
          {prep.requesters.length === 0 ? (
            <span className="text-[var(--terminal-muted)]">{t(`${K}.details.noWaiting`)}</span>
          ) : (
            prep.requesters.map((r, i) => (
              <div
                key={`${r.user_id}-${r.device_id ?? "web"}-${i}`}
                className="grid min-w-0 grid-cols-[1fr_auto] gap-2"
              >
                <span className="min-w-0 truncate font-medium text-[var(--terminal-fg)]">
                  {r.profile_name
                    ? t(`${K}.details.requesterProfile`, {
                        name: requesterName(r),
                        profile: r.profile_name,
                      })
                    : requesterName(r)}
                  <span className="block truncate text-[10px] font-normal text-[var(--terminal-muted)]">
                    {requesterDevice(r)}
                  </span>
                </span>
                <span className="text-[10px] text-[var(--terminal-muted)] capitalize">
                  {r.status === "preparing" ? t(`${K}.details.requesterWaiting`) : r.status}
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

function jobStatus(t: Translate, prep: AdminDownloadPreparation): string {
  switch (prep.state) {
    case "running":
      return t(`${K}.status.running`, {
        label: preparationRunningLabel(prep),
        attempt: String(Math.max(prep.attempts, 1)),
        max: String(prep.max_attempts),
      });
    case "queued":
      return prep.queue_position
        ? t(`${K}.status.queuedPosition`, { position: String(prep.queue_position) })
        : t(`${K}.status.queued`);
    case "retrying":
      return t(`${K}.status.retrying`, {
        retry_in: formatRetryIn(prep.next_retry_at),
        attempt: String(prep.attempts + 1),
        max: String(prep.max_attempts),
      });
    case "paused": {
      const ago = formatAgo(prep.paused_at);
      return ago ? t(`${K}.status.pausedAgo`, { ago }) : t(`${K}.status.paused`);
    }
    case "failed": {
      const ago = formatAgo(prep.failed_at);
      return ago ? t(`${K}.status.failedAgo`, { ago }) : t(`${K}.status.failed`);
    }
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
  const t = useT();
  return (
    <div className="mt-3 space-y-2">
      {isFetching && !isLoading && (
        <div className="text-muted-foreground text-end text-[10px]">
          {t(`${K}.ffmpeg.refreshing`)}
        </div>
      )}
      <div className="overflow-hidden rounded-xl border border-[var(--terminal-border)] bg-[var(--terminal-bg)] shadow-[0_18px_60px_rgba(0,0,0,0.35)]">
        {isLoading ? (
          <div className="px-4 py-6 font-mono text-[11px] text-[var(--terminal-muted)]">
            {t(`${K}.ffmpeg.loading`)}
          </div>
        ) : rows.length === 0 ? (
          <div className="px-4 py-6 font-mono text-[11px] text-[var(--terminal-muted)]">
            {t(`${K}.ffmpeg.empty`)}
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
                    {row.message.includes(STDERR) ? STDERR : t(`${K}.ffmpeg.event`)}
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
