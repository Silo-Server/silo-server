import { Link } from "react-router";
import { CheckCircle2, CircleSlash, XCircle } from "lucide-react";

import type { StreamNode } from "@/api/types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminNodes } from "@/hooks/queries/admin/nodes";
import { formatRelativeTime } from "@/lib/date";
import { useMeasuredSize } from "../charts/useMeasuredSize";
import { SectionError } from "../feedback";
import { formatMbps } from "../format";

/**
 * The narrowest card that still fits a name, a meter, and its numbers. Below
 * two columns of this the stacked rows read better than squeezed cards.
 */
const NODE_CARD_MIN_WIDTH_PX = 260;

/**
 * Remote transcode/streaming workers from `nodepool`.
 *
 * The fleet totals sit in the header so a glance answers "are the nodes okay
 * and how loaded are they" before any row is read; the body carries the
 * per-node detail. The body follows the widget's width: a narrow slot stacks
 * compact rows, a wide one lays the nodes out as cards in a grid — the admin
 * widening the widget is asking to see more of each node, not longer rows.
 * Nodes are ordered by type, then name, so transcode and proxy fleets read as
 * groups instead of interleaving.
 *
 * A deployment with no stream nodes is the normal single-server shape, not a
 * misconfiguration, so the empty state says where transcodes actually run
 * rather than reading as a missing dependency.
 */
export function TranscodeNodesWidget() {
  const nodesQuery = useAdminNodes();
  const nodes = sortNodes(nodesQuery.data ?? []);
  const { ref, size } = useMeasuredSize<HTMLDivElement>();
  const columns = size ? Math.max(1, Math.floor(size.width / NODE_CARD_MIN_WIDTH_PX)) : 1;
  const asCards = columns >= 2;

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between space-y-0 pb-3">
        <div className="flex min-w-0 items-baseline gap-2.5">
          <CardTitle className="text-sm font-bold">Nodes</CardTitle>
          {nodes.length > 0 ? <FleetSummary nodes={nodes} /> : null}
        </div>
        <Link
          to="/admin/nodes"
          className="text-muted-foreground hover:text-primary text-[11px] transition-colors"
        >
          Manage ›
        </Link>
      </CardHeader>
      <CardContent ref={ref} className="min-h-0 flex-1 overflow-y-auto">
        {nodesQuery.isLoading ? (
          <div className="space-y-1.5">
            {Array.from({ length: 2 }).map((_, i) => (
              <Skeleton key={i} className="h-[58px] rounded-md" />
            ))}
          </div>
        ) : nodesQuery.error ? (
          <SectionError message="Failed to load stream nodes." />
        ) : nodes.length === 0 ? (
          <div className="text-muted-foreground py-4 text-center text-sm">
            No stream nodes — transcodes run on this server
          </div>
        ) : asCards ? (
          <div
            className="grid gap-2"
            style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}
          >
            {nodes.map((node) => (
              <NodeCard key={node.id} node={node} />
            ))}
          </div>
        ) : (
          <div className="space-y-1.5">
            {nodes.map((node) => (
              <NodeRow key={node.id} node={node} />
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** Type first so each fleet reads as a block, then name for a stable order. */
function sortNodes(nodes: StreamNode[]): StreamNode[] {
  return [...nodes].sort(
    (a, b) => a.type.localeCompare(b.type) || a.name.localeCompare(b.name) || a.id - b.id,
  );
}

/**
 * Healthy count, fleet job load, and summed egress. Disabled nodes are left
 * out of the healthy ratio for the same reason the health strip leaves them
 * out: an operator turned them off on purpose.
 */
function FleetSummary({ nodes }: { nodes: StreamNode[] }) {
  const enabled = nodes.filter((node) => node.enabled);
  const healthy = enabled.filter((node) => node.healthy).length;
  const jobs = nodes.reduce((sum, node) => sum + node.active_jobs, 0);
  const egressKbps = nodes.reduce((sum, node) => sum + node.egress_kbps, 0);

  return (
    <span className="text-muted-foreground truncate text-[11px] tabular-nums">
      {enabled.length > 0 ? `${healthy}/${enabled.length} healthy · ` : ""}
      {jobs.toLocaleString()} {jobs === 1 ? "job" : "jobs"} · {formatMbps(egressKbps / 1_000)}
    </span>
  );
}

/** Compact stacked row for narrow slots. */
function NodeRow({ node }: { node: StreamNode }) {
  const status = nodeStatus(node);
  const StatusIcon = status.icon;
  const jobs = jobLoad(node);

  return (
    <div className="bg-surface border-border rounded-md border px-3 py-2">
      <div className="flex items-center gap-2">
        {/* Status is an icon plus the word: the tint alone would be the only
            signal for anyone who cannot separate the hues. */}
        <span
          className={`flex flex-shrink-0 items-center gap-1 text-[11px] font-medium ${status.className}`}
          title={status.label}
        >
          <StatusIcon className="h-3.5 w-3.5" aria-hidden="true" />
          <span className="sr-only sm:not-sr-only">{status.label}</span>
        </span>
        <div className="min-w-0 flex-1 truncate text-sm font-bold">{node.name}</div>
        <div className="text-muted-foreground flex-shrink-0 text-[11px] tabular-nums">
          {formatMbps(node.egress_kbps / 1_000)}
        </div>
      </div>
      <div className="mt-1.5 flex items-center gap-3">
        <JobsMeter node={node} jobs={jobs} />
        <div className="text-muted-foreground flex flex-shrink-0 items-center gap-2 text-[11px] tabular-nums">
          <span>{jobs.label}</span>
          <span className="text-border/70">·</span>
          <span>
            {node.type}
            {node.group ? ` · ${node.group}` : ""} · checked {lastCheckLabel(node)}
          </span>
        </div>
      </div>
    </div>
  );
}

/**
 * One node as a card for wide slots: identity on top, the load meter with its
 * own room, and the vitals as labelled pairs instead of a squeezed one-liner.
 */
function NodeCard({ node }: { node: StreamNode }) {
  const status = nodeStatus(node);
  const StatusIcon = status.icon;
  const jobs = jobLoad(node);

  return (
    <div className="bg-surface border-border flex flex-col gap-2 rounded-md border p-3">
      <div className="flex items-center gap-2">
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-bold">{node.name}</div>
          <div className="text-muted-foreground truncate text-[11px]">
            {node.type}
            {node.group ? ` · ${node.group}` : ""}
          </div>
        </div>
        <span
          className={`flex flex-shrink-0 items-center gap-1 text-[11px] font-medium ${status.className}`}
        >
          <StatusIcon className="h-3.5 w-3.5" aria-hidden="true" />
          {status.label}
        </span>
      </div>
      <JobsMeter node={node} jobs={jobs} />
      <div className="text-muted-foreground flex items-center justify-between gap-2 text-[11px] tabular-nums">
        <span>{jobs.label}</span>
        <span>{formatMbps(node.egress_kbps / 1_000)}</span>
        <span>checked {lastCheckLabel(node)}</span>
      </div>
    </div>
  );
}

function JobsMeter({ node, jobs }: { node: StreamNode; jobs: ReturnType<typeof jobLoad> }) {
  return (
    <div className="min-w-0 flex-1">
      <div className="bg-muted/60 h-1.5 w-full overflow-hidden rounded-full">
        <div
          className="h-full rounded-full"
          style={{
            width: `${jobs.percent}%`,
            background: "var(--chart-1)",
          }}
          role="meter"
          aria-label={`${node.name} transcode jobs`}
          aria-valuenow={node.active_jobs}
          aria-valuemin={0}
          aria-valuemax={jobs.max ?? undefined}
        />
      </div>
    </div>
  );
}

function lastCheckLabel(node: StreamNode): string {
  return (
    formatRelativeTime(node.last_health_check, {
      rounding: "floor",
      justNowLabel: "Just now",
    }) ?? "never checked"
  );
}

function nodeStatus(node: StreamNode) {
  if (!node.enabled) {
    return { label: "Disabled", icon: CircleSlash, className: "text-muted-foreground" };
  }
  if (!node.healthy) {
    return { label: "Unhealthy", icon: XCircle, className: "text-destructive" };
  }
  return { label: "Healthy", icon: CheckCircle2, className: "text-emerald-500" };
}

/**
 * A node with no `max_jobs` is uncapped, so there is no fraction to draw — the
 * meter then reflects nothing and stays empty rather than inventing a ceiling.
 */
function jobLoad(node: StreamNode): { percent: number; label: string; max: number | null } {
  const max = node.max_jobs && node.max_jobs > 0 ? node.max_jobs : null;
  if (max === null) {
    return {
      percent: 0,
      label: `${node.active_jobs.toLocaleString()} jobs`,
      max: null,
    };
  }
  const percent = Math.max(0, Math.min(100, Math.round((node.active_jobs / max) * 100)));
  return {
    percent,
    label: `${node.active_jobs.toLocaleString()}/${max.toLocaleString()} jobs`,
    max,
  };
}
