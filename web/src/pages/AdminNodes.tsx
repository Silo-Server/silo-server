import { useState } from "react";
import { useAdminServerSettings } from "@/hooks/queries/admin/settings";
import type { FormEvent, ReactNode } from "react";
import type { StreamNode, CreateNodeRequest, UpdateNodeRequest } from "@/api/types";
import {
  useAdminNodes,
  useCreateNode,
  useUpdateNode,
  useDeleteNode,
  useCheckNodeHealth,
  useReprobeNode,
  useToggleNode,
} from "@/hooks/queries/admin/nodes";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Plus, Pencil, Trash2, RefreshCw, ScanSearch, Info, AlertTriangle } from "lucide-react";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { formatDateTime } from "@/lib/datetime";
import { formatRelativeTime } from "@/lib/date";
import { toggleHWDevice } from "@/lib/hwDevices";
import { cn } from "@/lib/utils";
import type {
  NodeCapabilityStaleReason,
  NodeGPULiveDevice,
  NodeGroupOption,
  NodeHWDeviceRow,
  ResourceMetric,
} from "./adminNodesPresentation";
import {
  HW_ACCEL_INHERIT,
  HW_ACCEL_OVERRIDE_OPTIONS,
  buildNodeHWDeviceRows,
  describeCapabilityDrift,
  describeEffectiveAcceleration,
  describeNodeAccelerationOverride,
  describeNodeEgress,
  describeNodeGPU,
  describeNodeGroups,
  describeNodeJobs,
  describeNodeSystem,
  describeSharedGPU,
  filterNodesByGroup,
  nodeHWDevicePaths,
  nodeHasHWDeviceInventory,
  nodeReportsAcceleration,
  hwDeviceSyntaxChanges,
  nodeUsesCUDADevices,
  parseHWDeviceOverride,
} from "./adminNodesPresentation";

type NodeType = "proxy" | "transcode";

/**
 * Whether a node is in the rotation, and why not when it is out of it.
 *
 * Enabled and healthy are two separate facts, but an operator reading the rack
 * asks one question — can this node take work — so they resolve to one state.
 * Disabled wins over unhealthy: a node somebody switched off is not a node
 * that is failing, however its last health check went.
 */
type NodeState = "healthy" | "unhealthy" | "disabled";

function nodeState(node: StreamNode): NodeState {
  if (!node.enabled) {
    return "disabled";
  }
  return node.healthy ? "healthy" : "unhealthy";
}

const NODE_STATE_LABEL: Record<NodeState, string> = {
  healthy: "Healthy",
  unhealthy: "Unhealthy",
  disabled: "Disabled",
};

/**
 * Status-rail tint. The rail runs down the left edge of every unit, so a rack
 * of nodes is scannable as a column of state before any of it is read.
 */
const NODE_STATE_RAIL: Record<NodeState, string> = {
  healthy: "bg-success/70",
  unhealthy: "bg-destructive",
  disabled: "bg-muted-foreground/30",
};

const NODE_STATE_DOT: Record<NodeState, string> = {
  healthy: "bg-success",
  unhealthy: "bg-destructive",
  disabled: "bg-muted-foreground",
};

/** Micro-caps block label inside a node unit; the app's existing eyebrow idiom. */
function UnitLabel({ children }: { children: ReactNode }) {
  return <p className="hero-eyebrow mb-2.5 text-[0.62rem]">{children}</p>;
}

/**
 * A load bar, and the fixed-height slot it sits in so rows stay aligned whether
 * or not they have one.
 *
 * A null fill draws no bar at all. `ResourceMetric.fill` is null both when
 * nothing measured the reading and when it has no ceiling to measure against,
 * and a bar at zero for either would read as "measured and idle" — the mistake
 * every dash on this page exists to avoid. A measured zero does still draw, as
 * a stub, so that it stays distinguishable from no measurement.
 */
function LoadMeter({ fill, warning }: { fill: number | null; warning?: boolean }) {
  return (
    <div className="h-[3px]" aria-hidden="true">
      {fill !== null && (
        <div className="bg-border/70 h-full overflow-hidden rounded-full">
          <div
            className={cn(
              // The table polls every 30s, so the width change is the only thing
              // on the page that shows a node's load moving rather than just
              // being different. motion-safe, because that is also the only
              // reason anyone would want it off.
              "h-full min-w-[3px] rounded-full motion-safe:transition-[width]",
              warning ? "bg-warning" : "bg-foreground/55",
            )}
            style={{ width: `${Math.min(100, Math.max(0, fill))}%` }}
          />
        </div>
      )}
    </div>
  );
}

/**
 * The grid every reading inside a unit is laid out on: a name, a bar, a value.
 *
 * One grid per block rather than one per row, so the browser sizes the name and
 * value columns to the widest of each and the bars all start and end on the
 * same two lines. Bars of different lengths cannot be compared by eye, which is
 * the only reason to draw them.
 */
const METRIC_GRID = "grid grid-cols-[auto_minmax(2rem,1fr)_auto] items-center gap-x-3 gap-y-2.5";

/** One derived reading, as three cells of the enclosing metric grid. */
function NodeMetricRow({ metric }: { metric: ResourceMetric }) {
  return (
    <>
      <span className="text-muted-foreground text-[11px]" title={metric.title}>
        {metric.label}
      </span>
      <LoadMeter fill={metric.fill} warning={metric.warning} />
      <span
        className={cn(
          "text-right font-mono text-[11px] tabular-nums",
          metric.muted && "text-muted-foreground",
          metric.warning && "text-warning font-medium",
        )}
        title={metric.title}
      >
        {metric.value}
      </span>
    </>
  );
}

function NodeLoadBlock({ node }: { node: StreamNode }) {
  const system = describeNodeSystem(node);
  return (
    <div>
      <UnitLabel>Load</UnitLabel>
      {system.kind === "unreported" ? (
        <p className="text-muted-foreground text-xs" title={system.title}>
          No resource sample
        </p>
      ) : (
        <div className={METRIC_GRID}>
          <NodeMetricRow metric={system.cpu} />
          <NodeMetricRow metric={system.memory} />
          <NodeMetricRow metric={system.disk} />
          <NodeMetricRow metric={system.network} />
        </div>
      )}
    </div>
  );
}

function NodeCapacityBlock({ node, showEgress }: { node: StreamNode; showEgress: boolean }) {
  return (
    <div>
      <UnitLabel>Capacity</UnitLabel>
      <div className={METRIC_GRID}>
        <NodeMetricRow metric={describeNodeJobs(node)} />
        {showEgress && <NodeMetricRow metric={describeNodeEgress(node)} />}
        {/*
          Relative, because the question this answers is "is anything still
          talking to this node", and an absolute wall-clock stamp makes the
          reader do that subtraction on every unit. The stamp stays in the title
          for when the exact moment matters.
        */}
        <span className="text-muted-foreground text-[11px]">Checked</span>
        <span />
        <span
          className="text-right font-mono text-[11px] tabular-nums"
          title={node.last_health_check ? formatDateTime(node.last_health_check) : undefined}
        >
          {formatRelativeTime(node.last_health_check) ?? "Never"}
        </span>
      </div>
    </div>
  );
}

/** The "override: qsv" line, or nothing on a node that inherits the cluster. */
function NodeOverrideLine({ node }: { node: StreamNode }) {
  const override = describeNodeAccelerationOverride(node);
  if (!override) {
    return null;
  }
  return (
    <div className="text-muted-foreground text-xs" title={override.title}>
      {override.label}
    </div>
  );
}

/**
 * The "Shared GPU" marker, or nothing when this node's card is its own. Muted
 * rather than tinted: sharing hardware is information an operator needs when
 * reading job counts, not a fault.
 */
function NodeSharedGPUBadge({ node, allNodes }: { node: StreamNode; allNodes: StreamNode[] }) {
  const shared = describeSharedGPU(node, allNodes);
  if (!shared) {
    return null;
  }
  return (
    <Badge
      variant="outline"
      className="bg-surface text-muted-foreground border-border"
      title={shared.title}
    >
      {shared.label}
    </Badge>
  );
}

/**
 * The "Drift" marker on a node whose last capability refetch found its hardware
 * got worse. Tinted, unlike the shared-GPU marker: this one is a regression an
 * operator has to act on, and the Health column will not show it.
 */
function NodeDriftBadge({ node }: { node: StreamNode }) {
  const drift = describeCapabilityDrift(node);
  if (!drift) {
    return null;
  }
  return (
    <Badge
      variant="outline"
      className="bg-warning/10 text-warning border-warning/15"
      title={drift.title}
    >
      {drift.label}
    </Badge>
  );
}

// Each reason names a different thing to go look at, so none of them is worth
// collapsing into a generic "out of date": a contradicted report means the
// refetch is failing, an unreported one means the node no longer speaks
// capabilities at all, and an unconfirmed one means the health check stopped
// landing.
function staleInventoryTitle(reason: NodeCapabilityStaleReason, node: StreamNode): string {
  const refreshed = `The inventory below was last refreshed ${formatDateTime(node.capabilities_refreshed_at ?? "")}.`;
  switch (reason) {
    case "contradicted":
      return `This node reports hardware different from what is stored, and the refetch has not landed. ${refreshed}`;
    case "unreported":
      return `This node no longer reports a hardware inventory, so nothing confirms the one stored for it. ${refreshed}`;
    case "unconfirmed":
      return (
        `No health check has confirmed this inventory since ${formatDateTime(node.last_health_check ?? "")}. ` +
        `It was last refreshed ${formatDateTime(node.capabilities_refreshed_at ?? "")}.`
      );
  }
}

/**
 * One GPU's video engine: which device, how hard it is working, what is on it.
 *
 * The bar is the point of the row. Per-device busy percentages are the reading
 * an operator opens this page for when transcodes start queueing, and as four
 * lines of near-identical text they have to be read one at a time instead of
 * compared at a glance.
 */
function NodeEngineRow({ device }: { device: NodeGPULiveDevice }) {
  return (
    <>
      <span
        className="text-muted-foreground max-w-[7rem] truncate font-mono text-[11px]"
        title={device.title}
      >
        {device.label}
      </span>
      <LoadMeter fill={device.busyFill} />
      <span
        className={cn(
          "text-right font-mono text-[11px] tabular-nums",
          device.busyMuted ? "text-muted-foreground" : "text-foreground",
        )}
        title={device.title}
      >
        {device.busy}
      </span>
      <span className="text-muted-foreground text-right text-[10px] whitespace-nowrap">
        {device.sessions}
      </span>
    </>
  );
}

function NodeAccelerationBlock({ node, allNodes }: { node: StreamNode; allNodes: StreamNode[] }) {
  const gpu = describeNodeGPU(node);
  if (gpu.kind === "awaiting") {
    return (
      <div>
        <UnitLabel>Acceleration</UnitLabel>
        <div className="space-y-1.5">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-muted-foreground text-xs" title={gpu.title}>
              {gpu.label}
            </span>
            <NodeDriftBadge node={node} />
          </div>
          <NodeOverrideLine node={node} />
        </div>
      </div>
    );
  }

  return (
    <div>
      <UnitLabel>Acceleration</UnitLabel>
      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge variant="outline" className={gpu.backend.badgeClass} title={gpu.backend.title}>
            {gpu.backend.label}
          </Badge>
          <NodeDriftBadge node={node} />
          <NodeSharedGPUBadge node={node} allNodes={allNodes} />
          {gpu.failures.length > 0 && (
            <span
              className="text-warning inline-flex"
              title={gpu.failures
                .map((failure) => `${failure.label}: ${failure.reason}`)
                .join("\n")}
            >
              <AlertTriangle
                className="h-3.5 w-3.5"
                aria-label={`${gpu.failures.length} hardware backend probe failure(s) on ${node.name}`}
              />
            </span>
          )}
          {gpu.stale && (
            <span
              className="text-muted-foreground text-xs"
              title={staleInventoryTitle(gpu.stale, node)}
            >
              stale
            </span>
          )}
        </div>
        {gpu.deviceSummary && (
          <p className="text-muted-foreground text-xs" title={gpu.deviceTitle ?? undefined}>
            {gpu.deviceSummary}
          </p>
        )}
        <NodeOverrideLine node={node} />
        {gpu.live.length > 0 && (
          <div className="grid grid-cols-[auto_minmax(2rem,1fr)_auto_auto] items-center gap-x-3 gap-y-2 pt-0.5">
            {gpu.live.map((device) => (
              <NodeEngineRow key={device.key} device={device} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * One bucket in the group filter.
 *
 * A button rather than a link or a select: the whole point of the control is
 * that the options are visible side by side, because the question it answers —
 * which nodes belong to each other — is answered by seeing the buckets, not by
 * opening a menu to read them one at a time.
 */
function GroupChip({
  label,
  count,
  active,
  degraded,
  title,
  onClick,
}: {
  label: string;
  count: number;
  active: boolean;
  degraded?: boolean;
  title?: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      title={title}
      onClick={onClick}
      className={cn(
        "inline-flex items-center gap-2 rounded-full border px-3 py-1 text-xs transition-colors",
        // The selected chip is filled, not tinted. A 10% wash over this page's
        // surfaces is the kind of difference that reads on a designer's monitor
        // and nowhere else, and a filter whose current setting has to be hunted
        // for is worse than no filter: the list looks wrong rather than narrowed.
        active
          ? "border-primary bg-primary text-primary-foreground"
          : "border-border text-muted-foreground hover:bg-surface-hover hover:text-foreground",
      )}
    >
      {degraded && (
        <span
          aria-hidden="true"
          className="bg-warning h-1.5 w-1.5 shrink-0 rounded-full"
          // The dot is decoration; the group's own title says what it means, and
          // the unhealthy member is already spelled out on its own unit.
        />
      )}
      <span className={cn(active && "font-medium")}>{label}</span>
      <span
        className={cn(
          "font-mono text-[10px] tabular-nums",
          active ? "opacity-70" : "text-muted-foreground",
        )}
      >
        {count}
      </span>
    </button>
  );
}

/**
 * The group filter, shown above both sections because a group spans them: its
 * proxy and its transcode node are the pair, and each section holds one half.
 *
 * Absent when there is only one bucket. A filter that can only be switched
 * between "all" and "all" is a control that does nothing.
 */
function NodeGroupFilter({
  options,
  active,
  total,
  onSelect,
}: {
  options: NodeGroupOption[];
  active: string | null;
  total: number;
  onSelect: (group: string | null) => void;
}) {
  if (options.length < 2) {
    return null;
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <p className="hero-eyebrow mr-1 text-[0.62rem]">Groups</p>
      <GroupChip
        label="All"
        count={total}
        active={active === null}
        title="Every node, grouped or not."
        onClick={() => onSelect(null)}
      />
      {options.map((option) => (
        <GroupChip
          key={option.value}
          label={option.label}
          count={option.count}
          active={active === option.value}
          degraded={option.degraded}
          title={option.title}
          onClick={() => onSelect(option.value)}
        />
      ))}
    </div>
  );
}

interface NodeUnitProps {
  node: StreamNode;
  /**
   * Every node of both types. Shared-GPU detection needs it: a proxy and a
   * transcode node on one host share that host's card, and each section holds
   * only half of that pair.
   */
  allNodes: StreamNode[];
  /** Proxy nodes relay bytes and are capped on egress; transcode nodes are not. */
  showEgress: boolean;
  onEdit: (node: StreamNode) => void;
  onDelete: (node: StreamNode) => void;
  onToggle: (node: StreamNode) => void;
  onCheckHealth: (node: StreamNode) => void;
  isChecking: boolean;
  onReprobe: (node: StreamNode) => void;
  isReprobing: boolean;
}

/**
 * One node, as one unit.
 *
 * What a node reports is two-dimensional — a backend and its devices, four host
 * readings, a load against a cap — and a row of eleven columns could only hold
 * that by scrolling sideways and flattening every reading into the same 11px
 * grey. A unit gives the node a header worth scanning and three labelled blocks
 * under it, and it reflows on a narrow screen instead of scrolling off one.
 */
function NodeUnit({
  node,
  allNodes,
  showEgress,
  onEdit,
  onDelete,
  onToggle,
  onCheckHealth,
  isChecking,
  onReprobe,
  isReprobing,
}: NodeUnitProps) {
  const state = nodeState(node);
  // See nodeReportsAcceleration: a proxy reports no hardware, so it has neither
  // an acceleration block to draw nor hardware to re-probe.
  const showsAcceleration = nodeReportsAcceleration(node);

  return (
    <div
      className={cn(
        "surface-panel relative overflow-hidden rounded-2xl border-0 p-4 pl-5 sm:p-5 sm:pl-6",
        // A disabled node is out of the rotation, and every number on it is
        // whatever it was when it left. Dimming says that once, for the whole
        // unit, instead of qualifying each reading.
        state === "disabled" && "opacity-65",
      )}
    >
      <span
        aria-hidden="true"
        className={cn("absolute inset-y-4 left-0 w-[3px] rounded-full", NODE_STATE_RAIL[state])}
      />

      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
            <h3 className="text-[15px] leading-tight font-semibold tracking-tight">{node.name}</h3>
            <span className="inline-flex items-center gap-1.5">
              <span
                aria-hidden="true"
                className={cn("h-1.5 w-1.5 rounded-full", NODE_STATE_DOT[state])}
              />
              <span className="text-muted-foreground text-xs">{NODE_STATE_LABEL[state]}</span>
            </span>
            {node.group && (
              <Badge variant="outline" className="font-mono text-[10px] font-normal">
                {node.group}
              </Badge>
            )}
          </div>
          {/* The URL is the node's machine identity, so it is set in the mono
              face along with the device paths and the readings — everything on
              this page a person did not name. */}
          <p className="text-muted-foreground mt-1 truncate font-mono text-xs" title={node.url}>
            {node.url}
          </p>
          {node.public_url && (
            <p
              className="text-muted-foreground truncate font-mono text-xs"
              title={`Streaming clients connect here; the server uses the address above.`}
            >
              public: {node.public_url}
            </p>
          )}
        </div>

        <div className="flex shrink-0 items-center gap-1">
          {/* Out of the table, this switch has no column header to name it. */}
          <Switch
            checked={node.enabled}
            aria-label={`Enable ${node.name}`}
            onCheckedChange={() => onToggle(node)}
          />
          <span aria-hidden="true" className="bg-border/70 mx-1.5 h-5 w-px" />
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            disabled={isChecking}
            aria-label={`Check health of ${node.name}`}
            onClick={() => onCheckHealth(node)}
          >
            <RefreshCw
              className={`h-3 w-3 ${isChecking ? "animate-spin" : ""}`}
              aria-hidden="true"
            />
          </Button>
          {showsAcceleration && (
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              disabled={isReprobing}
              aria-label={`Re-probe hardware on ${node.name}`}
              title="Re-verify this node's hardware against live devices. Use after a driver or device change; it can take a couple of minutes, and is refused while the node is transcoding."
              onClick={() => onReprobe(node)}
            >
              <ScanSearch
                className={`h-3 w-3 ${isReprobing ? "animate-pulse" : ""}`}
                aria-hidden="true"
              />
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            aria-label={`Edit ${node.name}`}
            onClick={() => onEdit(node)}
          >
            <Pencil className="h-3 w-3" aria-hidden="true" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            aria-label={`Delete ${node.name}`}
            onClick={() => onDelete(node)}
          >
            <Trash2 className="h-3 w-3" aria-hidden="true" />
          </Button>
        </div>
      </div>

      {/* Two blocks on a proxy, three on a transcode node. The track list is
          picked to match, so the survivors fill the row rather than leaving a
          column-shaped hole where acceleration used to be. */}
      <div
        className={cn(
          "border-border/50 mt-4 grid gap-x-8 gap-y-5 border-t pt-4 sm:grid-cols-2",
          showsAcceleration
            ? "xl:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)_minmax(0,0.85fr)]"
            : "xl:grid-cols-[minmax(0,1fr)_minmax(0,0.85fr)]",
        )}
      >
        {showsAcceleration && <NodeAccelerationBlock node={node} allNodes={allNodes} />}
        <NodeLoadBlock node={node} />
        <NodeCapacityBlock node={node} showEgress={showEgress} />
      </div>
    </div>
  );
}

interface NodeSectionProps {
  type: NodeType;
  nodes: StreamNode[];
  /** Every node of both types; see `NodeUnitProps.allNodes`. */
  allNodes: StreamNode[];
  infoBanner: ReactNode;
  /** The group `nodes` was narrowed to, or null when it was not narrowed. */
  activeGroup: string | null;
  onClearGroup: () => void;
  onAdd: () => void;
  onEdit: (node: StreamNode) => void;
  onDelete: (node: StreamNode) => void;
  onToggle: (node: StreamNode) => void;
  onCheckHealth: (node: StreamNode) => void;
  checkingHealthId: number | null;
  onReprobe: (node: StreamNode) => void;
  reprobingId: number | null;
}

function NodeSection({
  type,
  nodes,
  allNodes,
  infoBanner,
  activeGroup,
  onClearGroup,
  onAdd,
  onEdit,
  onDelete,
  onToggle,
  onCheckHealth,
  checkingHealthId,
  onReprobe,
  reprobingId,
}: NodeSectionProps) {
  const label = type === "proxy" ? "Proxy" : "Transcode";

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <h2 className="text-lg font-semibold">{label} Nodes</h2>
          <Badge variant="secondary">{nodes.length}</Badge>
        </div>
        <Button size="sm" onClick={onAdd}>
          <Plus className="mr-1 h-4 w-4" /> Add {label}
        </Button>
      </div>

      {infoBanner}

      {nodes.length === 0 ? (
        <div className="surface-panel-subtle space-y-3 rounded-xl px-4 py-10 text-center">
          {/*
            Under a filter the section is empty because of the filter, not
            because the cluster has none of this kind — and for a transcode
            node that is worth reading rather than dismissing: a group with no
            proxy of its own has its transcodes served by any proxy in the
            cluster instead of staying on one LAN.
          */}
          <p className="text-muted-foreground mx-auto max-w-md text-sm">
            {activeGroup !== null
              ? type === "proxy"
                ? "No proxy node in this group. Transcodes here are served by any proxy in the cluster rather than staying on one LAN."
                : "No transcode node in this group. Nothing is pinned to its proxies."
              : type === "proxy"
                ? "No proxy nodes yet. Add one to deliver streams from somewhere other than this server."
                : "No transcode nodes yet. Add one to move transcoding off this server."}
          </p>
          {activeGroup !== null ? (
            <Button variant="outline" size="sm" onClick={onClearGroup}>
              Show all groups
            </Button>
          ) : (
            <Button variant="outline" size="sm" onClick={onAdd}>
              <Plus className="mr-1 h-4 w-4" /> Add {label}
            </Button>
          )}
        </div>
      ) : (
        <div className="space-y-3">
          {nodes.map((node) => (
            <NodeUnit
              key={node.id}
              node={node}
              allNodes={allNodes}
              showEgress={type === "proxy"}
              onEdit={onEdit}
              onDelete={onDelete}
              onToggle={onToggle}
              onCheckHealth={onCheckHealth}
              isChecking={checkingHealthId === node.id}
              onReprobe={onReprobe}
              isReprobing={reprobingId === node.id}
            />
          ))}
        </div>
      )}
    </section>
  );
}

/**
 * Per-device toggles for one node's device override, mirroring the cluster-wide
 * picker on the Playback settings page — same control, same "nothing selected
 * means inherit" rule, so the two overrides read the same way.
 *
 * "Inherit" is spelled two ways on purpose: it is what an empty selection means,
 * and it is a button, because clearing several toggles one at a time to get back
 * to the default is not an obvious way to say "follow the cluster".
 */
function NodeDevicePicker({
  rows,
  onToggle,
  onInherit,
}: {
  rows: NodeHWDeviceRow[];
  onToggle: (path: string) => void;
  onInherit: () => void;
}) {
  const selectedCount = rows.filter((row) => row.selected).length;

  return (
    <div className="space-y-2">
      <div className="space-y-2">
        {rows.map((row) => (
          <div key={row.path} className="flex items-center justify-between gap-3">
            <div className="min-w-0" title={row.title}>
              <p className={cn("truncate text-sm", !row.reported && "text-muted-foreground")}>
                {row.description}
              </p>
              <p className="text-muted-foreground truncate font-mono text-xs">{row.path}</p>
            </div>
            <Switch
              checked={row.selected}
              aria-label={`Transcode on ${row.path}`}
              onCheckedChange={() => onToggle(row.path)}
            />
          </div>
        ))}
      </div>
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <p className="text-muted-foreground text-sm">
          {/*
            No claim about what the cluster default *is*: it may be unset (each
            node auto-discovers) or an explicit device list, and this form does
            not know which.
          */}
          {selectedCount === 0
            ? "Using the cluster-wide device setting."
            : selectedCount === 1
              ? "All transcodes on this node run on the selected device."
              : "Transcodes on this node balance across the selected devices (least loaded first)."}
        </p>
        {selectedCount > 0 && (
          <Button type="button" variant="ghost" size="sm" className="h-7 px-2" onClick={onInherit}>
            Use cluster default
          </Button>
        )}
      </div>
    </div>
  );
}

function NodeForm({
  node,
  nodeType,
  defaultGroup,
  onClose,
}: {
  node: StreamNode | null;
  nodeType: NodeType;
  /**
   * Group to start a new node in: the one the list is filtered to, so a node
   * added from a filtered view lands in the group being looked at rather than
   * disappearing out of it the moment it is saved. Ignored when editing, where
   * the node's own group is the answer.
   */
  defaultGroup: string;
  onClose: () => void;
}) {
  const [name, setName] = useState(node?.name ?? "");
  const [url, setUrl] = useState(node?.url ?? "");
  const [publicUrl, setPublicUrl] = useState(node?.public_url ?? "");
  const [group, setGroup] = useState(node?.group ?? defaultGroup);
  const [maxJobs, setMaxJobs] = useState(node?.max_jobs?.toString() ?? "");
  const [maxBandwidthMbps, setMaxBandwidthMbps] = useState(
    node?.max_bandwidth_kbps ? (node.max_bandwidth_kbps / 1000).toString() : "",
  );
  const [hwAccelOverride, setHwAccelOverride] = useState(
    node?.hw_accel_override?.trim() || HW_ACCEL_INHERIT,
  );
  const [hwDeviceOverride, setHwDeviceOverride] = useState(node?.hw_device_override ?? "");
  // The picker is driven by the node's own reported inventory; a node that has
  // never reported one keeps the free-text field, since the override still has
  // to be settable on a node this server has not heard from yet.
  // NVENC names GPUs by CUDA index or UUID, so the render-path picker is
  // meaningless for it — the same rule the cluster-wide Playback form applies.
  // The cluster setting is read because "inherit" means running what the
  // cluster names, which this node's current resolution does not describe.
  const { data: serverSettings } = useAdminServerSettings();
  const clusterHWAccel = serverSettings?.["playback.hw_accel"];
  const usesCUDADevices = nodeUsesCUDADevices(node, hwAccelOverride, clusterHWAccel);
  // A device override written for one syntax means nothing in the other: a
  // render path is not a CUDA identity and neither is settable as the other, so
  // moving the selection between them drops the stored value rather than
  // carrying it into a policy it cannot express.
  //
  // Driven by the operator's own edit rather than by watching usesCUDADevices,
  // because that value also moves on its own: the cluster setting arrives after
  // the first render, and an effect would read that as a change and erase a
  // valid override on a dialog nobody had touched yet.
  function selectHWAccelOverride(next: string) {
    if (hwDeviceSyntaxChanges(node, hwAccelOverride, next, clusterHWAccel)) {
      setHwDeviceOverride("");
    }
    setHwAccelOverride(next);
  }
  const hasDeviceInventory = nodeHasHWDeviceInventory(node) && !usesCUDADevices;
  const deviceRows = buildNodeHWDeviceRows(node, hwDeviceOverride);
  const devicePaths = nodeHWDevicePaths(node);
  const effectiveAcceleration = node ? describeEffectiveAcceleration(node) : null;
  const createMutation = useCreateNode();
  const updateMutation = useUpdateNode();
  const isPending = createMutation.isPending || updateMutation.isPending;

  const urlPlaceholder =
    nodeType === "proxy" ? "https://proxy1.example.com" : "http://10.0.0.5:8082";

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    // The backend treats an empty group as "ungrouped" and caps <= 0 as
    // "unlimited", so cleared inputs reset those fields.
    const parsedMaxJobs = parseInt(maxJobs, 10);
    const parsedMaxBandwidthMbps = parseFloat(maxBandwidthMbps);
    const fields = {
      name,
      url,
      group: group.trim(),
      max_jobs: Number.isNaN(parsedMaxJobs) ? 0 : parsedMaxJobs,
      max_bandwidth_kbps: Number.isNaN(parsedMaxBandwidthMbps)
        ? 0
        : Math.round(parsedMaxBandwidthMbps * 1000),
    };
    if (node) {
      // null on either override is what restores inheritance of the
      // cluster-wide playback setting; omitting the key would leave the stored
      // value alone instead. The override controls only render for transcode
      // nodes, so a proxy edit must omit both keys rather than send null —
      // sending null here would clear an existing value the form never showed.
      const body: UpdateNodeRequest = { ...fields };
      if (nodeType === "proxy") {
        // Same shape as the overrides: null clears, so an emptied field sends
        // clients back to the backend URL rather than silently keeping the
        // old public one.
        body.public_url = publicUrl.trim() || null;
      }
      if (nodeType === "transcode") {
        const overrideDevices = parseHWDeviceOverride(hwDeviceOverride);
        body.hw_accel_override = hwAccelOverride === HW_ACCEL_INHERIT ? null : hwAccelOverride;
        body.hw_device_override = overrideDevices.length > 0 ? overrideDevices.join(",") : null;
      }
      updateMutation.mutate({ id: node.id, body }, { onSuccess: onClose });
    } else {
      const body: CreateNodeRequest = { type: nodeType, ...fields };
      if (nodeType === "proxy" && publicUrl.trim() !== "") {
        body.public_url = publicUrl.trim();
      }
      createMutation.mutate(body, { onSuccess: onClose });
    }
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="space-y-2">
        <Label>Name</Label>
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={nodeType === "proxy" ? "Proxy Node 1" : "Transcode Node 1"}
          required
        />
      </div>

      <div className="space-y-2">
        <Label>Type</Label>
        <Badge variant="secondary" className="text-sm">
          {nodeType === "proxy" ? "Proxy" : "Transcode"}
        </Badge>
      </div>

      <div className="space-y-2">
        <Label>URL</Label>
        <Input
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder={urlPlaceholder}
          required
        />
        {nodeType === "transcode" ? (
          <p className="text-muted-foreground text-sm">
            Must be reachable from proxy nodes and the backend server. A private/internal IP or
            localhost is fine — no public URL needed.
          </p>
        ) : (
          <p className="text-muted-foreground text-sm">
            How the server reaches this node — health checks and capability fetches use it, so a
            private/internal address is fine and keeps that traffic off the public network. Without
            a Public URL below, streaming clients use this address too, so it must then be publicly
            accessible.
          </p>
        )}
      </div>

      {nodeType === "proxy" && (
        <div className="space-y-2">
          <Label>Public URL</Label>
          <Input
            value={publicUrl}
            onChange={(e) => setPublicUrl(e.target.value)}
            placeholder="Same as URL"
          />
          <p className="text-muted-foreground text-sm">
            Optional. What streaming clients connect to, when it differs from the URL above — a CDN
            or load-balancer hostname in front of this node. Stream and download links are built on
            it; everything the server does keeps using the URL above. Leave empty to give clients
            the URL above.
          </p>
        </div>
      )}

      <div className="space-y-2">
        <Label>Group</Label>
        <Input value={group} onChange={(e) => setGroup(e.target.value)} placeholder="e.g. rack-1" />
        <p className="text-muted-foreground text-sm">
          Optional. Nodes in the same group are treated as co-located: transcoded streams are served
          by a proxy from the transcode node's group, keeping traffic on the same LAN. A group is
          only used while all of its nodes are healthy.
        </p>
      </div>

      <div className="space-y-2">
        <Label>{nodeType === "proxy" ? "Max Streams" : "Max Transcodes"}</Label>
        <Input
          type="number"
          min={0}
          value={maxJobs}
          onChange={(e) => setMaxJobs(e.target.value)}
          placeholder="Unlimited"
        />
        <p className="text-muted-foreground text-sm">
          Optional concurrency cap for this node. Leave empty (or 0) for unlimited.
        </p>
      </div>

      {nodeType === "proxy" && (
        <div className="space-y-2">
          <Label>Max Egress Bandwidth (Mbps)</Label>
          <Input
            type="number"
            min={0}
            step="any"
            value={maxBandwidthMbps}
            onChange={(e) => setMaxBandwidthMbps(e.target.value)}
            placeholder="Unlimited"
          />
          <p className="text-muted-foreground text-sm">
            Optional. New streams are routed elsewhere once this node's measured egress (plus the
            expected bitrate of the new stream) would exceed the cap. Active streams are never
            interrupted. Leave empty (or 0) for unlimited.
          </p>
        </div>
      )}

      {/* Overrides are edit-only: the create endpoint takes no acceleration
          fields, so offering them here would silently drop what was typed.
          They are also transcode-only: a proxy node only remuxes/strips
          bitstreams, so it never encodes and these fields would be
          meaningless — and their absence keeps a proxy edit from sending
          override fields at all. */}
      {node && nodeType === "transcode" && (
        <>
          <div className="space-y-2">
            <Label htmlFor="node-hw-accel-override">Hardware Acceleration</Label>
            <Select value={hwAccelOverride} onValueChange={selectHWAccelOverride}>
              <SelectTrigger id="node-hw-accel-override" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {HW_ACCEL_OVERRIDE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-sm">
              Optional. Overrides the cluster-wide Hardware Acceleration setting for this node only
              — use it when this node's hardware differs from the rest of the cluster. The cluster
              default is Auto unless changed on the Playback settings page, and Auto detects this
              node's own hardware, not the server's. Applies to new transcodes within a minute;
              restart the node to re-prime its encoder for the new backend.
            </p>
            {effectiveAcceleration && (
              <p className="text-muted-foreground text-sm">{effectiveAcceleration}</p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor={hasDeviceInventory ? undefined : "node-hw-device-override"}>
              GPU Devices
            </Label>
            {hasDeviceInventory ? (
              <NodeDevicePicker
                rows={deviceRows}
                onToggle={(path) =>
                  setHwDeviceOverride(toggleHWDevice(hwDeviceOverride, path, devicePaths))
                }
                onInherit={() => setHwDeviceOverride("")}
              />
            ) : (
              <>
                <Input
                  id="node-hw-device-override"
                  value={hwDeviceOverride}
                  onChange={(e) => setHwDeviceOverride(e.target.value)}
                  placeholder="Cluster default"
                />
                {/*
                  Each branch is a whole sentence rather than a shared tail: an
                  NVENC node reaches the free-text field *with* a reported
                  inventory (its render devices are real, they are just not what
                  NVENC addresses), so "no inventory yet" is only true of the
                  other branch — and splitting one sentence across the
                  conditional also lets JSX drop the space before "empty".

                  Neither branch says what leaving this empty resolves to. Empty
                  inherits the cluster-wide playback.hw_device verbatim, and this
                  form does not know that value — naming a default here would be
                  a guess, and on the NVENC branch a dangerous one, since an
                  inherited /dev/dri path reaches NVENC as a CUDA identity.
                */}
                <p className="text-muted-foreground text-sm">
                  {usesCUDADevices ? (
                    <>
                      Optional. The CUDA device this node encodes on — an index or a GPU UUID (e.g.{" "}
                      <span className="font-mono">0</span> or{" "}
                      <span className="font-mono">GPU-a1b2c3d4</span>). NVENC addresses GPUs by CUDA
                      identity, not by <span className="font-mono">/dev/dri</span> render path, so
                      the device picker does not apply to it. Leaving this empty inherits the
                      cluster-wide device setting, which must itself be a CUDA identity for NVENC to
                      use it — set one here when the cluster is configured with render paths.
                    </>
                  ) : (
                    <>
                      Optional. Comma-separated render device paths this node transcodes on (e.g.{" "}
                      <span className="font-mono">/dev/dri/renderD128,/dev/dri/renderD129</span>).
                      This node has reported no device inventory yet, so there is nothing to pick
                      from. Leave empty to inherit the cluster-wide device setting.
                    </>
                  )}
                </p>
              </>
            )}
          </div>
        </>
      )}

      <Button type="submit" className="w-full" disabled={isPending}>
        {isPending ? "Saving..." : "Save"}
      </Button>
    </form>
  );
}

export default function AdminNodes() {
  const { data: nodes = [], isLoading } = useAdminNodes();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingNode, setEditingNode] = useState<StreamNode | null>(null);
  const [addingNodeType, setAddingNodeType] = useState<NodeType | null>(null);
  const [confirmDeleteNode, setConfirmDeleteNode] = useState<StreamNode | null>(null);
  const [groupFilter, setGroupFilter] = useState<string | null>(null);
  const deleteMutation = useDeleteNode();
  const checkHealthMutation = useCheckNodeHealth();
  const reprobeMutation = useReprobeNode();
  const toggleMutation = useToggleNode();

  const groupOptions = describeNodeGroups(nodes);
  // Derived rather than reset in an effect: the selected group can stop
  // existing without anything on this page doing it — the last node in it is
  // deleted, regrouped from another tab, or the poll simply arrives with a
  // different cluster. Falling back here means the list can never be filtered
  // to a bucket that is gone, and re-selecting the group heals it if it comes
  // back, which a one-shot effect that cleared the state would not.
  const activeGroup =
    groupFilter !== null && groupOptions.some((option) => option.value === groupFilter)
      ? groupFilter
      : null;
  const visibleNodes = filterNodesByGroup(nodes, activeGroup);

  const proxyNodes = visibleNodes.filter((n) => n.type === "proxy");
  const transcodeNodes = visibleNodes.filter((n) => n.type === "transcode");

  const checkingHealthId =
    checkHealthMutation.isPending && checkHealthMutation.variables
      ? checkHealthMutation.variables.id
      : null;

  const reprobingId =
    reprobeMutation.isPending && reprobeMutation.variables ? reprobeMutation.variables.id : null;

  const resolvedNodeType: NodeType = editingNode
    ? (editingNode.type as NodeType)
    : (addingNodeType ?? "proxy");

  function handleAdd(type: NodeType) {
    setAddingNodeType(type);
    setEditingNode(null);
    setDialogOpen(true);
  }

  function handleEdit(node: StreamNode) {
    setEditingNode(node);
    setAddingNodeType(null);
    setDialogOpen(true);
  }

  function handleDelete(node: StreamNode) {
    setConfirmDeleteNode(node);
  }

  function handleDialogChange(open: boolean) {
    setDialogOpen(open);
    if (!open) {
      setEditingNode(null);
      setAddingNodeType(null);
    }
  }

  if (isLoading) return <div className="page-shell py-8">Loading nodes...</div>;

  return (
    <div className="page-shell space-y-8 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Stream Nodes</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Manage proxy and transcode workers that distribute playback load across your
            infrastructure.
          </p>
        </div>
      </div>

      <NodeGroupFilter
        options={groupOptions}
        active={activeGroup}
        total={nodes.length}
        onSelect={setGroupFilter}
      />

      <ConfirmDialog
        open={confirmDeleteNode !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDeleteNode(null);
        }}
        title="Delete node"
        description={`Delete stream node "${confirmDeleteNode?.name}"? This action cannot be undone.`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={() => {
          if (confirmDeleteNode) deleteMutation.mutate(confirmDeleteNode.id);
          setConfirmDeleteNode(null);
        }}
      />

      <NodeSection
        type="proxy"
        nodes={proxyNodes}
        allNodes={nodes}
        activeGroup={activeGroup}
        onClearGroup={() => setGroupFilter(null)}
        onAdd={() => handleAdd("proxy")}
        onEdit={handleEdit}
        onDelete={handleDelete}
        onToggle={(node) => toggleMutation.mutate(node)}
        onCheckHealth={(node) => checkHealthMutation.mutate(node)}
        checkingHealthId={checkingHealthId}
        onReprobe={(node) => reprobeMutation.mutate(node)}
        reprobingId={reprobingId}
        infoBanner={
          <div className="surface-panel-subtle text-info flex items-start gap-2 rounded-xl p-3 text-sm">
            <Info className="mt-0.5 h-4 w-4 shrink-0" />
            <p>Proxy nodes relay streams to end users. The URL must be publicly accessible.</p>
          </div>
        }
      />

      <NodeSection
        type="transcode"
        nodes={transcodeNodes}
        allNodes={nodes}
        activeGroup={activeGroup}
        onClearGroup={() => setGroupFilter(null)}
        onAdd={() => handleAdd("transcode")}
        onEdit={handleEdit}
        onDelete={handleDelete}
        onToggle={(node) => toggleMutation.mutate(node)}
        onCheckHealth={(node) => checkHealthMutation.mutate(node)}
        checkingHealthId={checkingHealthId}
        onReprobe={(node) => reprobeMutation.mutate(node)}
        reprobingId={reprobingId}
        infoBanner={
          <div className="surface-panel-subtle text-warning flex items-start gap-2 rounded-xl p-3 text-sm">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
            <p>
              Transcode nodes handle video transcoding internally.{" "}
              <strong>Must be on the same network as proxy nodes and the backend.</strong> Does not
              need a public URL.
            </p>
          </div>
        }
      />

      <Dialog open={dialogOpen} onOpenChange={handleDialogChange}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {editingNode
                ? "Edit Node"
                : `Add ${resolvedNodeType === "proxy" ? "Proxy" : "Transcode"} Node`}
            </DialogTitle>
          </DialogHeader>
          <NodeForm
            node={editingNode}
            nodeType={resolvedNodeType}
            defaultGroup={activeGroup ?? ""}
            onClose={() => handleDialogChange(false)}
          />
        </DialogContent>
      </Dialog>
    </div>
  );
}
