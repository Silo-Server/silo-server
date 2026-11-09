import type { ReactNode } from "react";
import { Link } from "react-router";
import {
  AlertTriangle,
  CheckCircle2,
  CircleSlash,
  HardDrive,
  PauseCircle,
  XCircle,
  type LucideIcon,
} from "lucide-react";

import type { NodeBuildInfo, StreamNode } from "@/api/types";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminNodes } from "@/hooks/queries/admin/nodes";
import { useBuildInfo } from "@/hooks/queries/admin/system";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import {
  describeNodeDrain,
  describeNodeEgress,
  describeNodeJobs,
  describeNodeGPUBusy,
  describeNodeSystem,
  NODE_STATE_LABEL,
  nodeGroupOf,
  nodeReportsAcceleration,
  nodeState,
  positiveCap,
  UNGROUPED_LABEL,
  type NodeState,
  type ResourceMetric,
} from "@/pages/adminNodesPresentation";
import { SectionError } from "../feedback";
import { formatMbps, formatMbpsValue } from "../format";

/**
 * Remote transcode/streaming workers from `nodepool`, as a grouped table.
 *
 * The fleet totals sit in the header so a glance answers "are the nodes okay
 * and how loaded are they" before any row is read. Nodes sit under their
 * assigned group, because a group is how a proxy and its transcode nodes are
 * deployed and routed together. Groups that need attention sort first and take
 * their worst node's status, so a problem stays visible in a long list. A
 * deployment that assigns no groups gets a plain list without headings.
 *
 * Columns follow the widget's width through container queries: widening the
 * widget shows more readings per node, and a narrow slot keeps the name, the
 * active count, and egress. The build only draws attention when it differs from
 * the server's, so a half-finished rollout is visible at any width.
 *
 * A deployment with no stream nodes is the normal single-server shape, not a
 * misconfiguration, so the empty state says where transcodes actually run
 * rather than reading as a missing dependency.
 */
export function TranscodeNodesWidget() {
  const nodesQuery = useAdminNodes();
  const buildQuery = useBuildInfo();
  const serverRevision = buildQuery.data?.revision || null;
  const nodes = nodesQuery.data ?? [];
  const groups = nodeGroups(nodes, serverRevision);
  const showHeadings = groups.some((group) => group.name !== "");
  const showsGPU = nodes.some(nodeReportsAcceleration);

  return (
    // Tighter than the Card default: in a three-row widget that space is a table row.
    <Card className="h-full gap-0 pb-3">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between space-y-0 pb-3">
        <div className="flex min-w-0 items-baseline gap-2.5">
          <CardTitle className="text-sm font-bold">Nodes</CardTitle>
          {nodes.length > 0 ? <FleetSummary nodes={nodes} /> : null}
        </div>
        <Link
          to="/admin/nodes"
          className="text-muted-foreground hover:text-primary shrink-0 text-[11px] whitespace-nowrap transition-colors"
        >
          Manage ›
        </Link>
      </CardHeader>
      <CardContent className="@container/nodes min-h-0 flex-1 overflow-y-auto">
        {nodesQuery.isLoading ? (
          <div className="space-y-1.5">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-6 rounded-md" />
            ))}
          </div>
        ) : nodesQuery.error ? (
          <SectionError message="Failed to load stream nodes." />
        ) : nodes.length === 0 ? (
          <div className="text-muted-foreground py-4 text-center text-sm">
            No stream nodes — transcodes run on this server
          </div>
        ) : (
          <table className="w-full table-fixed border-separate border-spacing-0 text-xs tabular-nums">
            <ColumnHeadings showsGPU={showsGPU} />
            {groups.map((group) => (
              <GroupRows
                key={group.name}
                group={group}
                showHeading={showHeadings}
                showsGPU={showsGPU}
                serverRevision={serverRevision}
              />
            ))}
          </table>
        )}
      </CardContent>
    </Card>
  );
}

interface NodeGroup {
  /** The group's real name, or "" for the nodes that have none. */
  name: string;
  label: string;
  /** Its worst node's state and attention rank. */
  state: NodeState;
  rank: number;
  nodes: StreamNode[];
}

const compareNames = new Intl.Collator(undefined, { numeric: true }).compare;

/**
 * Nodes by their assigned group, worst group first. Within a group, proxies come
 * before transcode nodes so every group reads in the same order.
 */
function nodeGroups(nodes: StreamNode[], serverRevision: string | null): NodeGroup[] {
  // Ranked once up front: a rank builds several node descriptions, and the sorts
  // below would otherwise rebuild them on every comparison.
  const ranks = new Map(nodes.map((node) => [node, attentionRank(node, serverRevision)]));
  const rankOf = (node: StreamNode) => ranks.get(node) ?? 0;

  const byName = new Map<string, StreamNode[]>();
  for (const node of nodes) {
    const name = nodeGroupOf(node);
    const members = byName.get(name);
    if (members) {
      members.push(node);
    } else {
      byName.set(name, [node]);
    }
  }
  const labels = groupLabels([...byName.keys()].filter(Boolean));

  return [...byName.entries()]
    .map(([name, members]) => {
      const worst = members.reduce((a, b) => (rankOf(b) < rankOf(a) ? b : a));
      return {
        name,
        label: labels.get(name) ?? UNGROUPED_LABEL,
        state: nodeState(worst),
        rank: rankOf(worst),
        nodes: members.sort(
          (a, b) =>
            a.type.localeCompare(b.type) ||
            rankOf(a) - rankOf(b) ||
            compareNames(a.name, b.name) ||
            String(a.id).localeCompare(String(b.id)),
        ),
      };
    })
    .sort(
      (a, b) =>
        a.rank - b.rank ||
        Number(a.name === "") - Number(b.name === "") ||
        compareNames(a.name, b.name),
    );
}

/**
 * The leading words every group name shares, or "" with fewer than two groups:
 * a lone group has nothing to tell apart, so its name stays whole.
 */
function sharedPrefix(names: string[]): string {
  const [first] = names;
  if (first === undefined || names.length < 2) {
    return "";
  }
  let prefix = first;
  for (const name of names) {
    while (!name.startsWith(prefix)) {
      prefix = prefix.slice(0, -1);
    }
  }
  return prefix.slice(0, prefix.lastIndexOf("-") + 1);
}

/**
 * Each group's shortened label. Two names can shorten to the same words, as
 * "prod-api-east" and "prod-api-api-east" both do, and those keep their full names.
 */
function groupLabels(names: string[]): Map<string, string> {
  const prefix = sharedPrefix(names);
  const labels = new Map(names.map((name) => [name, groupLabel(name, prefix)]));
  const counts = new Map<string, number>();
  for (const label of labels.values()) {
    counts.set(label, (counts.get(label) ?? 0) + 1);
  }
  for (const [name, label] of labels) {
    if ((counts.get(label) ?? 0) > 1) {
      labels.set(name, name);
    }
  }
  return labels;
}

/** Below this, a label is a bare "3" or "set-3" that says little on its own. */
const MIN_GROUP_LABEL_LENGTH = 8;

/** The name without the shared prefix, with words put back until it reads. */
function groupLabel(name: string, prefix: string): string {
  const words = name.split("-");
  let kept = name.slice(prefix.length).split("-").length;
  while (kept < words.length && words.slice(-kept).join("-").length < MIN_GROUP_LABEL_LENGTH) {
    kept += 1;
  }
  return words.slice(-kept).join("-");
}

/**
 * Worst first. A disabled node sorts last: an operator turned it off on
 * purpose, so it is the one row that asks nothing of the reader.
 */
function attentionRank(node: StreamNode, serverRevision: string | null): number {
  const state = nodeState(node);
  if (state === "unhealthy") return 0;
  if (state === "draining") return 1;
  if (state === "drained") return 2;
  if (state === "disabled") return 5;
  const flagged =
    describeNodeJobs(node).warning ||
    describeNodeEgress(node).warning ||
    transcodeDirectoryWarning(node) !== null ||
    buildDiffers(node, serverRevision);
  return flagged ? 3 : 4;
}

/**
 * Only called out when both sides know their revision: a build with stripped
 * metadata cannot be compared, and flagging it would be noise on every dev build.
 */
function buildDiffers(node: StreamNode, serverRevision: string | null): boolean {
  const build = node.last_stats?.build;
  return (
    Boolean(build?.available && build.revision && serverRevision) &&
    build?.revision !== serverRevision
  );
}

/** How full a transcode directory gets before the card speaks up about it. */
const TRANSCODE_DIRECTORY_WARNING_PERCENT = 90;

/**
 * The disk reading of a nearly full transcode directory, or null. It stays out
 * of the table until then, because a full directory stops transcodes while a
 * half-full one asks nothing of the reader. A node's only sampled mount is this
 * directory, and an old sample reports nothing, like every other reading here.
 */
function transcodeDirectoryWarning(node: StreamNode): ResourceMetric | null {
  if (node.type !== "transcode") {
    return null;
  }
  const system = describeNodeSystem(node);
  if (system.kind !== "reported") {
    return null;
  }
  const { disk } = system;
  return disk.fill !== null && disk.fill > TRANSCODE_DIRECTORY_WARNING_PERCENT ? disk : null;
}

/**
 * Group count, healthy count, then the work and egress across the fleet.
 * Disabled nodes are left out of the healthy ratio for the same reason the
 * health strip leaves them out: an operator turned them off on purpose. Work
 * and egress count only the nodes whose last check is current, as the server's
 * egress chart does: a disabled node keeps its last reported counts forever.
 */
function FleetSummary({ nodes }: { nodes: StreamNode[] }) {
  const enabled = nodes.filter((node) => node.enabled);
  const live = enabled.filter((node) => node.healthy);
  const healthy = live.length;
  const egressKbps = live.reduce((sum, node) => sum + node.egress_kbps, 0);
  const work = (unit: string, list: StreamNode[]) => {
    const active = list
      .filter((node) => live.includes(node))
      .reduce((sum, node) => sum + node.active_jobs, 0);
    return `${active.toLocaleString()} ${unit}${active === 1 ? "" : "s"}`;
  };
  const proxies = nodes.filter((node) => node.type === "proxy");
  const others = nodes.filter((node) => node.type !== "proxy");
  const groups = new Set(nodes.map(nodeGroupOf).filter(Boolean)).size;
  const parts = [
    groups > 0 ? `${groups} ${groups === 1 ? "group" : "groups"}` : null,
    enabled.length > 0 ? `${healthy}/${enabled.length} healthy` : null,
    proxies.length > 0 ? work("stream", proxies) : null,
    others.length > 0 ? work("transcode", others) : null,
    formatMbps(egressKbps / 1_000),
  ];

  return (
    <span className="text-muted-foreground truncate text-[11px] tabular-nums">
      {parts.filter(Boolean).join(" · ")}
    </span>
  );
}

/** Where a CPU, RAM or GPU reading turns amber. */
const HIGH_LOAD_PERCENT = 90;

const COLUMN_HEADING =
  "bg-card text-muted-foreground border-border sticky top-0 z-10 border-b pb-1 pl-2.5 text-right text-[11px] font-medium whitespace-nowrap";
const CELL = "h-6 py-0.5 pl-2.5 text-right whitespace-nowrap";

/** Narrow columns go first: the name and the two work numbers always stay. */
const ACTIVE_COLUMN = "w-[72px]";
const EGRESS_COLUMN = "w-[60px] @min-[620px]/nodes:w-[88px]";
const CPU_COLUMN =
  "w-[62px] text-left @max-[359px]/nodes:hidden @min-[620px]/nodes:w-[92px] @min-[1200px]/nodes:w-[150px]";
const RAM_COLUMN =
  "w-[62px] text-left @max-[439px]/nodes:hidden @min-[620px]/nodes:w-[92px] @min-[1200px]/nodes:w-[150px]";
const GPU_COLUMN =
  "hidden w-[92px] text-left @min-[620px]/nodes:table-cell @min-[1200px]/nodes:w-[150px]";
const CHECKED_COLUMN = "hidden w-[86px] @min-[1200px]/nodes:table-cell";
const BUILD_COLUMN = "hidden w-[150px] @min-[1200px]/nodes:table-cell";

function dataColumns(showsGPU: boolean) {
  return [
    { label: "Active", className: ACTIVE_COLUMN },
    { label: "Mbps", className: EGRESS_COLUMN },
    { label: "CPU", className: CPU_COLUMN },
    { label: "RAM", className: RAM_COLUMN },
    ...(showsGPU ? [{ label: "GPU", className: GPU_COLUMN }] : []),
    { label: "Checked", className: CHECKED_COLUMN },
    { label: "Build", className: BUILD_COLUMN },
  ];
}

function ColumnHeadings({ showsGPU }: { showsGPU: boolean }) {
  return (
    <thead>
      <tr>
        <th scope="col" className={cn(COLUMN_HEADING, "pl-0 text-left")}>
          Node
        </th>
        {dataColumns(showsGPU).map(({ label, className }) => (
          <th key={label} scope="col" className={cn(COLUMN_HEADING, className)}>
            {label}
          </th>
        ))}
      </tr>
    </thead>
  );
}

/** Enough to keep a worker's number and role, as in "-4 Transcode". */
const NODE_NAME_TAIL_LENGTH = 12;

/**
 * A name shortened in the middle when it does not fit. Workers tend to share
 * their start and differ at the end, so cutting the end would make rows look alike.
 */
function NodeName({ name }: { name: string }) {
  const tail = name.slice(-NODE_NAME_TAIL_LENGTH);
  const head = name.slice(0, name.length - tail.length);
  return (
    <span className="flex min-w-0 font-semibold" title={name}>
      {head ? (
        <span className="min-w-0 overflow-hidden text-ellipsis whitespace-pre">{head}</span>
      ) : null}
      <span className="flex-shrink-0 whitespace-pre">{tail}</span>
    </span>
  );
}

function NodeFlag({
  icon: Icon,
  title,
  labelClassName,
  children,
}: {
  icon: LucideIcon;
  title?: string;
  labelClassName: string;
  children: ReactNode;
}) {
  return (
    <span
      className="text-warning flex flex-shrink-0 items-center gap-1 text-[11px] font-medium"
      title={title}
    >
      <Icon className="h-3.5 w-3.5" aria-hidden="true" />
      <span className={labelClassName}>{children}</span>
    </span>
  );
}

function StatusIcon({ state, title }: { state: NodeState; title?: string }) {
  const status = NODE_STATUS[state];
  const Icon = status.icon;
  const name = title ?? NODE_STATE_LABEL[state];
  // An icon with a name: the tint alone would be the only signal for anyone who
  // cannot separate the hues.
  return (
    <span
      role="img"
      aria-label={name}
      title={name}
      className={cn("flex-shrink-0", status.className)}
    >
      <Icon className="h-3.5 w-3.5" aria-hidden="true" />
    </span>
  );
}

function GroupRows({
  group,
  showHeading,
  showsGPU,
  serverRevision,
}: {
  group: NodeGroup;
  showHeading: boolean;
  showsGPU: boolean;
  serverRevision: string | null;
}) {
  return (
    <tbody>
      {showHeading ? (
        <tr>
          {/* Empty cells rather than a colspan: a hidden column inside a span
              would keep its width in the fixed table layout. */}
          <th scope="rowgroup" className="pt-1.5 pb-0.5 text-left text-[11px] font-bold">
            <div className="flex min-w-0 items-center gap-1.5">
              <StatusIcon state={group.state} />
              <span className="min-w-0 truncate" title={group.name || undefined}>
                {group.label}
              </span>
            </div>
          </th>
          {dataColumns(showsGPU).map(({ label, className }) => (
            <td key={label} className={className} />
          ))}
        </tr>
      ) : null}
      {group.nodes.map((node) => (
        <NodeRow
          key={node.id}
          node={node}
          indent={showHeading}
          showsGPU={showsGPU}
          serverRevision={serverRevision}
        />
      ))}
    </tbody>
  );
}

function NodeRow({
  node,
  indent,
  showsGPU,
  serverRevision,
}: {
  node: StreamNode;
  indent: boolean;
  showsGPU: boolean;
  serverRevision: string | null;
}) {
  const state = nodeState(node);
  const drain = state === "draining" || state === "drained" ? describeNodeDrain(node) : null;
  const readings = nodeReadings(node);
  const build = node.last_stats?.build;
  const storage = transcodeDirectoryWarning(node);
  const noCountsTitle = staleCountsTitle(state);

  return (
    <tr className={state === "disabled" ? "text-muted-foreground" : undefined}>
      <th
        scope="row"
        className={cn("h-6 overflow-hidden py-0.5 text-left font-normal", indent && "pl-5")}
      >
        <div className="flex min-w-0 items-center gap-1.5">
          <StatusIcon state={state} title={drain?.label} />
          <NodeName name={node.name} />
          {/* A tight slot keeps the icons, so warnings cannot push out the name. With both
              showing, the build gives way first: a full disk stops transcodes. */}
          {storage ? (
            <NodeFlag
              icon={HardDrive}
              title={`Transcode directory ${storage.fill}% full. ${storage.title}`}
              labelClassName="@max-[719px]/nodes:sr-only"
            >
              Storage {storage.fill}%
            </NodeFlag>
          ) : null}
          {buildDiffers(node, serverRevision) ? (
            <NodeFlag
              icon={AlertTriangle}
              title={`Build differs from the server. ${buildTitle(node)}`}
              labelClassName={storage ? "@max-[999px]/nodes:sr-only" : "@max-[719px]/nodes:sr-only"}
            >
              Build differs
            </NodeFlag>
          ) : null}
        </div>
      </th>
      <td className={cn(CELL, ACTIVE_COLUMN)} title={noCountsTitle ?? describeNodeJobs(node).title}>
        {noCountsTitle ? <NoReading /> : <ActiveCount node={node} />}
      </td>
      <td
        className={cn(CELL, EGRESS_COLUMN)}
        title={noCountsTitle ?? describeNodeEgress(node).title}
      >
        {noCountsTitle ? <NoReading /> : <EgressValue node={node} />}
      </td>
      <td className={cn(CELL, CPU_COLUMN)}>
        <LoadReading name={`${node.name} CPU`} metric={readings.cpu} missing={readings.missing} />
      </td>
      <td className={cn(CELL, RAM_COLUMN)}>
        <LoadReading name={`${node.name} RAM`} metric={readings.ram} missing={readings.missing} />
      </td>
      {showsGPU ? (
        <td className={cn(CELL, GPU_COLUMN)}>
          {nodeReportsAcceleration(node) ? (
            <LoadReading
              name={`${node.name} GPU`}
              metric={readings.gpu}
              missing={readings.missing}
            />
          ) : null}
        </td>
      ) : null}
      <td className={cn(CELL, CHECKED_COLUMN, "text-muted-foreground text-[11px]")}>
        {lastCheckLabel(node)}
      </td>
      <td
        className={cn(CELL, BUILD_COLUMN, "text-muted-foreground text-[11px]")}
        title={buildTitle(node)}
      >
        {build ? buildLabel(build) : null}
      </td>
    </tr>
  );
}

/**
 * Why a node's work counts are not current, or null when they are. A failed
 * check reports zeros, which would read as an idle node, and a switched-off node
 * leaves the pool and is never checked again, so it keeps whatever its last
 * check reported.
 */
function staleCountsTitle(state: NodeState): string | null {
  if (state === "unhealthy") {
    return "The last health check did not reach this node.";
  }
  if (state === "disabled") {
    return "This node is switched off, so health checks no longer update it.";
  }
  return null;
}

function NoReading() {
  return <span className="text-muted-foreground">–</span>;
}

/** One "Active" column holds both kinds of work, so each count names its unit. */
function ActiveCount({ node }: { node: StreamNode }) {
  const active = node.active_jobs.toLocaleString();
  const cap = positiveCap(node.max_jobs);
  const proxy = node.type === "proxy";
  return (
    <>
      <span className={describeNodeJobs(node).warning ? "text-warning font-semibold" : undefined}>
        {active}
        {cap !== null ? (
          <span className="text-muted-foreground font-normal"> / {cap.toLocaleString()}</span>
        ) : null}
      </span>{" "}
      <abbr
        title={proxy ? "streams" : "transcodes"}
        className="text-muted-foreground text-[10px] no-underline"
      >
        {proxy ? "strm" : "trns"}
      </abbr>
    </>
  );
}

/** The cap is the first thing to go when the column is tight; the use stays. */
function EgressValue({ node }: { node: StreamNode }) {
  const cap = positiveCap(node.max_bandwidth_kbps);
  return (
    <>
      <span
        className={cn(
          describeNodeEgress(node).warning && "text-warning font-semibold",
          node.egress_kbps === 0 && "text-muted-foreground",
        )}
      >
        {formatMbpsValue(node.egress_kbps / 1_000)}
      </span>
      {cap !== null ? (
        <span className="text-muted-foreground hidden @min-[620px]/nodes:inline">
          {" "}
          / {formatMbpsValue(cap / 1_000)}
        </span>
      ) : null}
    </>
  );
}

/**
 * CPU, RAM and GPU from the node's last sample. A reading that is unmeasured or
 * no longer current is null, because a remembered percentage reads as a live one;
 * `missing` then says why, so a stalled sampler doesn't look like one that never ran.
 */
function nodeReadings(node: StreamNode): {
  cpu: ResourceMetric | null;
  ram: ResourceMetric | null;
  gpu: ResourceMetric | null;
  missing: string | undefined;
} {
  const system = describeNodeSystem(node);
  return {
    cpu: system.kind === "reported" ? system.cpu : null,
    ram: system.kind === "reported" ? system.memory : null,
    gpu: describeNodeGPUBusy(node),
    missing: system.kind === "unreported" ? system.title : undefined,
  };
}

/** The number sits under its column heading, with the meter running to its right. */
function LoadReading({
  name,
  metric,
  missing,
}: {
  name: string;
  metric: ResourceMetric | null;
  missing?: string;
}) {
  if (!metric || metric.fill === null) {
    return (
      <span className="text-muted-foreground block w-8 text-right" title={metric?.title ?? missing}>
        –
      </span>
    );
  }
  const high = metric.fill >= HIGH_LOAD_PERCENT;
  return (
    <div className="flex items-center gap-1.5" title={metric.title}>
      <span className={cn("w-8 flex-shrink-0 text-right", high && "text-warning font-semibold")}>
        {metric.fill}%
      </span>
      <div className="bg-foreground/15 hidden h-1 max-w-[90px] min-w-5 flex-1 overflow-hidden rounded-sm @min-[620px]/nodes:block">
        <div
          className={cn("h-full rounded-sm", high ? "bg-warning" : "bg-[var(--chart-1)]")}
          style={{ width: `${metric.fill}%` }}
          role="meter"
          aria-label={name}
          aria-valuenow={metric.fill}
          aria-valuemin={0}
          aria-valuemax={100}
        />
      </div>
    </div>
  );
}

function buildLabel(build: NodeBuildInfo): string {
  const display = build.display || "unavailable";
  return build.build_number ? `${build.build_number} · ${display}` : display;
}

function buildTitle(node: StreamNode): string | undefined {
  const build = node.last_stats?.build;
  if (!build) {
    return undefined;
  }
  return [
    build.revision ? `Revision ${build.revision}` : "Revision unavailable",
    build.built_at ? `built ${build.built_at}` : null,
  ]
    .filter(Boolean)
    .join(" · ");
}

function lastCheckLabel(node: StreamNode): string {
  return (
    formatRelativeTime(node.last_health_check, {
      rounding: "floor",
      justNowLabel: "Just now",
    }) ?? "Never"
  );
}

const NODE_STATUS: Record<NodeState, { icon: LucideIcon; className: string }> = {
  healthy: { icon: CheckCircle2, className: "text-emerald-500" },
  draining: { icon: PauseCircle, className: "text-warning" },
  drained: { icon: PauseCircle, className: "text-info" },
  unhealthy: { icon: XCircle, className: "text-destructive" },
  disabled: { icon: CircleSlash, className: "text-muted-foreground" },
};
