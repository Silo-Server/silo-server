import type {
  HostDiskStats,
  HostGPUStats,
  HostSystemStats,
  NodeCapabilities,
  NodeRenderDevice,
  ReprobeNodeResult,
  StreamNode,
  SystemResources,
} from "@/api/types";
import { parseHWDeviceList } from "@/lib/hwDevices";
import { formatFileSize } from "@/lib/mediaFormat";

/**
 * How long a stored capability report may go unconfirmed before it is called
 * out. The health sweep only refetches when a node advertises a changed hash,
 * so an unchanged node legitimately keeps a report from hours ago — its age
 * says nothing. What does tick is the sweep itself: every check that finds the
 * advertised hash unchanged re-confirms the stored report. So the report is
 * only doubtful once the checks stop, which at a 30s cadence this allows twenty
 * of before saying so.
 */
export const CAPABILITY_STALE_AFTER_MS = 10 * 60 * 1000;

/** Verification state of the backend a node resolved to. */
export type NodeGPUBackendState = "verified" | "failed" | "unverified" | "none";

export interface NodeGPUBackendBadge {
  /** Uppercase backend name, or "SW" when no hardware backend is in use. */
  label: string;
  state: NodeGPUBackendState;
  /** Tinted badge classes, shared with the activity-method tags. */
  badgeClass: string;
  /** Hover text: the probe outcome, including a failure reason. */
  title: string;
}

/** A backend that had candidate hardware but failed its FFmpeg probe. */
export interface NodeGPUFailure {
  label: string;
  reason: string;
}

export type NodeGPUPresentation =
  | {
      kind: "awaiting";
      label: string;
      title: string;
    }
  | {
      kind: "reported";
      backend: NodeGPUBackendBadge;
      /** Failed backends other than the resolved one, whose reason is in its title. */
      failures: NodeGPUFailure[];
      /** Compact device list, e.g. "2× Intel GPU"; null when none were reported. */
      deviceSummary: string | null;
      /** Full device paths, one per line, for the summary's tooltip. */
      deviceTitle: string | null;
      /** Why this report is no longer trustworthy as current; null when it is. */
      stale: NodeCapabilityStaleReason | null;
      /**
       * Live per-device readings from the node's last health check, matched
       * against the capability inventory. Empty when the node reports no
       * sample — a server or node predating resource sampling renders exactly
       * as it did before, rather than showing zeros it never measured.
       */
      live: NodeGPULiveDevice[];
    };

/**
 * Why a stored capability report is not being treated as current.
 *
 * - `contradicted` — the node reports a different hash than the one stored, so
 *   the stored inventory describes hardware the node itself disagrees with.
 * - `unreported` — the node answers health checks but names no hash at all, as
 *   a build predating capability reports does. It is not standing behind the
 *   stored inventory either; it simply cannot say.
 * - `unconfirmed` — no health check has re-confirmed the report recently enough.
 */
export type NodeCapabilityStaleReason = "contradicted" | "unreported" | "unconfirmed";

/** One GPU's live reading, as rendered next to the capability inventory. */
export interface NodeGPULiveDevice {
  /** Stable key for list rendering; the device id as the node reported it. */
  key: string;
  /** Short device name: "renderD128", or the raw id like "cuda:0". */
  label: string;
  /** Video-engine busy percentage, or a dash when nothing measured it. */
  busy: string;
  /**
   * The number behind `busy`, 0–100, for the engine meter — null when nothing
   * measured it. Null rather than 0 on purpose: a meter drawn at zero is read
   * as an idle GPU, which is the one thing an unmeasured device is not.
   */
  busyFill: number | null;
  /** No measurement behind `busy`: render it muted, since it is not a zero. */
  busyMuted: boolean;
  /** "2 sessions", or "idle" for none. */
  sessions: string;
  /** Hover text: device identity, both engines, whole-GPU/VRAM, and source. */
  title: string;
}

const BACKEND_BADGE_CLASS: Record<NodeGPUBackendState, string> = {
  verified: "bg-success/10 text-success border-success/15",
  failed: "bg-warning/10 text-warning border-warning/15",
  unverified: "bg-surface text-muted-foreground border-border",
  none: "bg-surface text-muted-foreground border-border",
};

/**
 * Describe a node's GPU column. `now` is injected so staleness is testable and
 * so a rendered table can be pinned to one clock reading.
 */
export function describeNodeGPU(node: StreamNode, now: number = Date.now()): NodeGPUPresentation {
  const capabilities = node.capabilities;
  if (!capabilities) {
    return {
      kind: "awaiting",
      label: "Awaiting first report",
      title: "No hardware capability report has been stored for this node yet.",
    };
  }

  const devices = summarizeRenderDevices(capabilities);
  const resolved = capabilities.resolved?.trim().toLowerCase() ?? "";
  return {
    kind: "reported",
    backend: describeBackend(resolved, capabilities.detected_backends ?? []),
    failures: otherFailures(resolved, capabilities.detected_backends ?? []),
    deviceSummary: devices.summary,
    deviceTitle: devices.title,
    stale: capabilityReportStaleness(node, now),
    live: describeLiveGPUs(node),
  };
}

/**
 * Live GPU readings for a node, matched to its capability inventory.
 *
 * An unhealthy node contributes none: its sample is from before the check that
 * failed, and a busy percentage that stopped moving reads as a live one.
 */
function describeLiveGPUs(node: StreamNode): NodeGPULiveDevice[] {
  if (!node.healthy) {
    return [];
  }
  const details = node.capabilities?.render_device_details ?? [];
  return (node.last_stats?.gpu ?? []).map((stats) => describeLiveGPU(stats, details));
}

function describeLiveGPU(
  stats: HostGPUStats,
  details: readonly NodeRenderDevice[],
): NodeGPULiveDevice {
  const device = stats.device?.trim() ?? "";
  // The inventory speaks /dev/dri paths; a sample for a GPU with no readable
  // DRM node names a PCI address or a cuda index instead, which is why the
  // fallback match exists rather than the path lookup alone.
  const detail =
    details.find((candidate) => candidate.path?.trim() === device && device !== "") ??
    details.find((candidate) => candidate.pci_address?.trim() === device && device !== "");
  const measured = isMeasuredGPUSource(stats.source);
  const video = finiteNumber(stats.video_busy_pct);
  const sessions = Math.max(0, Math.trunc(finiteNumber(stats.sessions) ?? 0));

  return {
    key: device || (detail?.path ?? "gpu"),
    label: shortDeviceLabel(device, detail),
    busy: measured && video != null ? `${clampPercent(video)}%` : DASH,
    busyFill: measured && video != null ? clampPercent(video) : null,
    busyMuted: !measured || video == null,
    sessions: sessions === 1 ? "1 session" : sessions === 0 ? "idle" : `${sessions} sessions`,
    title: liveGPUTitle(stats, detail, device, measured),
  };
}

function liveGPUTitle(
  stats: HostGPUStats,
  detail: NodeRenderDevice | undefined,
  device: string,
  measured: boolean,
): string {
  const identity = [
    device || detail?.path?.trim() || "(unknown device)",
    detail?.description?.trim(),
  ]
    .filter((part): part is string => !!part)
    .join(" — ");
  const lines = [identity];

  if (measured) {
    const engines = [
      formatEngine("video", stats.video_busy_pct),
      formatEngine("render", stats.render_busy_pct),
    ].filter((part): part is string => part !== null);
    if (engines.length > 0) {
      lines.push(engines.join(" · "));
    }
  } else {
    // Zeros with no measurement behind them must not read as an idle GPU.
    lines.push("No source could measure this device on the last sample.");
  }

  const whole = finiteNumber(stats.total_busy_pct);
  if (whole != null) {
    lines.push(`whole GPU ${clampPercent(whole)}% (all tenants)`);
  }
  const vram = formatUsedOfTotal(stats.vram_used_mb, stats.vram_total_mb, mebibytesToBytes);
  if (vram) {
    lines.push(`VRAM ${vram}`);
  }
  const source = stats.source?.trim();
  if (source) {
    lines.push(`source: ${source}`);
  }
  return lines.join("\n");
}

function formatEngine(name: string, value: number | null | undefined): string | null {
  const percent = finiteNumber(value);
  return percent == null ? null : `${name} ${clampPercent(percent)}%`;
}

function isMeasuredGPUSource(source: string | undefined): boolean {
  const normalized = source?.trim().toLowerCase() ?? "";
  return normalized !== "" && normalized !== "unavailable";
}

function shortDeviceLabel(device: string, detail: NodeRenderDevice | undefined): string {
  const path = device || detail?.path?.trim() || "";
  if (path === "") {
    return "GPU";
  }
  const tail = path.split("/").filter((part) => part !== "");
  return tail[tail.length - 1] ?? path;
}

function describeBackend(resolved: string, detected: readonly NodeDetected[]): NodeGPUBackendBadge {
  if (resolved === "" || resolved === "none") {
    const skipped = detected.filter((entry) => entry.skipped);
    if (skipped.length > 0 && skipped.length === detected.length) {
      return badge(
        "SW",
        "none",
        "Encoding in software — the configured GPU devices are not accessible on this node.",
      );
    }
    return badge("SW", "none", "No hardware backend verified — encoding in software.");
  }

  const label = resolved.toUpperCase();
  const entry = findDetectedEntry(resolved, detected);
  if (!entry) {
    // A configured backend wins resolution even with no candidate hardware to
    // probe, so absence of an entry is unknown, not failure.
    return badge(
      label,
      "unverified",
      `${label} is in use but this node reported no verification probe for it.`,
    );
  }
  if (!entry.verified) {
    return badge(label, "failed", `${label} probe failed: ${failureReason(entry)}`);
  }
  const device = entry.device?.trim();
  return badge(
    label,
    "verified",
    device
      ? `${label} verified by FFmpeg probe on ${device}.`
      : `${label} verified by FFmpeg probe.`,
  );
}

function badge(label: string, state: NodeGPUBackendState, title: string): NodeGPUBackendBadge {
  return { label, state, badgeClass: BACKEND_BADGE_CLASS[state], title };
}

type NodeDetected = NonNullable<NodeCapabilities["detected_backends"]>[number];

/** The probe entry for the resolved backend, or undefined when none was run. */
function findDetectedEntry(
  resolved: string,
  detected: readonly NodeDetected[],
): NodeDetected | undefined {
  return detected.find((candidate) => candidate.backend?.trim().toLowerCase() === resolved);
}

/**
 * One-line summary of what a transcode node's acceleration resolves to right
 * now, for display under the per-node override select. Null when the node has
 * never reported capabilities — there is nothing stored to describe yet, which
 * is also true of a server predating capability reporting.
 *
 * This mirrors `describeBackend`'s verified/failed/unverified/none states but
 * renders them as plain prose rather than a badge, since the edit dialog has
 * no badge to hang it on.
 */
export function describeEffectiveAcceleration(node: StreamNode): string | null {
  const capabilities = node.capabilities;
  if (!capabilities) {
    return null;
  }

  const resolved = capabilities.resolved?.trim().toLowerCase() ?? "";
  if (resolved === "" || resolved === "none") {
    return "Currently resolves: software encoding";
  }

  const label = resolved.toUpperCase();
  const entry = findDetectedEntry(resolved, capabilities.detected_backends ?? []);
  if (!entry) {
    return `Currently resolves: ${label} — not verified`;
  }
  if (!entry.verified) {
    return `Currently resolves: ${label} — probe failed`;
  }
  const device = entry.device?.trim();
  return `Currently resolves: ${label} — verified${device ? ` on ${device}` : ""}`;
}

function otherFailures(resolved: string, detected: readonly NodeDetected[]): NodeGPUFailure[] {
  return detected
    .filter((entry) => {
      const backend = entry.backend?.trim().toLowerCase() ?? "";
      // Skipped entries were never probed (their devices are not accessible
      // on this node) — expected on proxies, so they do not warrant a warning.
      return !entry.verified && !entry.skipped && backend !== "" && backend !== resolved;
    })
    .map((entry) => ({
      label: (entry.backend?.trim() ?? "").toUpperCase(),
      reason: failureReason(entry),
    }));
}

function failureReason(entry: NodeDetected): string {
  return entry.reason?.trim() || "no reason reported";
}

function capabilityReportStaleness(
  node: StreamNode,
  now: number,
): NodeCapabilityStaleReason | null {
  // An unhealthy node cannot refresh its report; calling that stale would blame
  // the inventory for the outage the Health column already shows.
  if (!node.healthy) {
    return null;
  }
  if (Number.isNaN(Date.parse(node.capabilities_refreshed_at ?? ""))) {
    return null;
  }
  // What the node said about itself on its last health check. Absent means no
  // check has happened here yet — every node reads that way until the first
  // sweep after a restart — which is silence, not disagreement. Present and
  // empty is the node answering with no hash at all, and that is a node not
  // standing behind the stored report any more than a mismatching one does:
  // both leave an inventory nothing currently confirms while the health check
  // goes on succeeding every 30 seconds. This is the staleness a timestamp
  // cannot see.
  const advertised = node.advertised_capabilities_hash?.trim();
  if (advertised !== undefined && advertised !== (node.capabilities_hash?.trim() ?? "")) {
    return advertised === "" ? "unreported" : "contradicted";
  }
  // Measured against the health check, not against the report's own age: the
  // check is what re-confirms the report, and it is the only one of the two
  // that moves on a node whose hardware never changes.
  const lastCheck = Date.parse(node.last_health_check ?? "");
  if (Number.isNaN(lastCheck)) {
    return null;
  }
  return now - lastCheck > CAPABILITY_STALE_AFTER_MS ? "unconfirmed" : null;
}

function summarizeRenderDevices(capabilities: NodeCapabilities): {
  summary: string | null;
  title: string | null;
} {
  const details = capabilities.render_device_details ?? [];
  if (details.length > 0) {
    return {
      summary: countedDescriptions(details),
      title: details.map(describeDeviceLine).join("\n"),
    };
  }

  // A report with paths but no details is still worth a count.
  const paths = (capabilities.render_devices ?? []).filter((path) => path.trim() !== "");
  if (paths.length === 0) {
    return { summary: null, title: null };
  }
  return {
    summary: paths.length === 1 ? "1 render device" : `${paths.length} render devices`,
    title: paths.join("\n"),
  };
}

/** "2× Intel GPU, NVIDIA GPU (0x2204)" — identical descriptions collapse. */
function countedDescriptions(details: readonly NodeRenderDevice[]): string {
  const counted: { label: string; count: number }[] = [];
  for (const device of details) {
    const label = deviceLabel(device);
    const existing = counted.find((entry) => entry.label === label);
    if (existing) {
      existing.count += 1;
    } else {
      counted.push({ label, count: 1 });
    }
  }
  return counted
    .map((entry) => (entry.count > 1 ? `${entry.count}× ${entry.label}` : entry.label))
    .join(", ");
}

function deviceLabel(device: NodeRenderDevice): string {
  return device.description?.trim() || device.path?.trim() || "GPU";
}

function describeDeviceLine(device: NodeRenderDevice): string {
  const path = device.path?.trim() || "(unknown path)";
  const parts = [path, device.description?.trim()].filter((part): part is string => !!part);
  const line = parts.join(" — ");
  const address = device.pci_address?.trim();
  return address ? `${line} (${address})` : line;
}

// --- Shared physical GPUs --------------------------------------------------

/** The "Shared GPU" marker on a node backed by hardware another node also uses. */
export interface SharedGPUBadge {
  label: string;
  /** Hover text naming the other nodes on the same card(s). */
  title: string;
}

/**
 * Describe whether a node shares a physical GPU with any other node, or null
 * when it does not — which is the normal case and renders nothing.
 *
 * `physical_gpu_keys` is derived server-side from each node's capability
 * report, so a node that reports no identifiable GPU (and every node from a
 * server predating the field) carries none and can never match: an unknown GPU
 * is not evidence of sharing in either direction.
 */
export function describeSharedGPU(
  node: StreamNode,
  allNodes: readonly StreamNode[],
): SharedGPUBadge | null {
  const keys = new Set(node.physical_gpu_keys ?? []);
  if (keys.size === 0) {
    return null;
  }

  const others = allNodes
    .filter((candidate) => candidate.id !== node.id)
    .filter((candidate) => (candidate.physical_gpu_keys ?? []).some((key) => keys.has(key)))
    .map((candidate) => candidate.name);
  if (others.length === 0) {
    return null;
  }

  return {
    label: "Shared GPU",
    title: `Shares a physical GPU with: ${others.join(", ")}`,
  };
}

// --- Capability drift ------------------------------------------------------

/** The "Drift" marker on a node whose hardware got worse at its last refetch. */
export interface CapabilityDriftBadge {
  label: string;
  /** Hover text: the server's note, then what it means and how it clears. */
  title: string;
}

/**
 * Describe a node's capability drift, or null when the node carries no standing
 * regression — which is the normal case and renders nothing, as it does on
 * every server predating the field.
 *
 * The note is a warning, not a routing input: a node carrying one is still
 * picked for work exactly as before. It is here because the regression it
 * describes is invisible in the Health column — a node whose GPU driver broke
 * answers health checks perfectly well while encoding in software.
 */
export function describeCapabilityDrift(node: StreamNode): CapabilityDriftBadge | null {
  const note = node.capability_drift?.trim() ?? "";
  if (note === "") {
    return null;
  }
  return {
    label: "Drift",
    title: [
      note,
      "A capability refetch found this node's hardware got worse than the report it replaced.",
      "It stays until a refetch finds every hardware probe passing — re-probe the node to check whether it is still true.",
    ].join("\n"),
  };
}

// --- Per-node device-override picker ---------------------------------------

/** One render device in a node's own device-override picker. */
export interface NodeHWDeviceRow {
  /** Render node path, which is what the override stores. */
  path: string;
  /** The node's own label for the device, or why the path is not in its report. */
  description: string;
  /** Present in the node's stored capability report. */
  reported: boolean;
  /** Pinned by the override currently being edited. */
  selected: boolean;
  /** Hover text: the device's full identity as the node reported it. */
  title: string;
}

interface ReportedDevice {
  path: string;
  description: string;
  title: string;
}

/**
 * The devices a node reported, in report order. Details win when the node sent
 * them; a report carrying only paths (an older node) still yields rows, because
 * the paths are what the override stores and the descriptions are decoration.
 */
function reportedDevices(node: StreamNode | null | undefined): ReportedDevice[] {
  const details = (node?.capabilities?.render_device_details ?? []).filter(
    (device) => (device.path?.trim() ?? "") !== "",
  );
  if (details.length > 0) {
    return details.map((device) => ({
      path: device.path.trim(),
      description: deviceLabel(device),
      title: describeDeviceLine(device),
    }));
  }
  return (node?.capabilities?.render_devices ?? [])
    .map((path) => path.trim())
    .filter((path) => path !== "")
    .map((path) => ({ path, description: "GPU", title: path }));
}

/**
 * Whether this node has an inventory to pick from. False sends the editor back
 * to a free-text field: a node that has never reported (or a server predating
 * the inventory) must still be pinnable, and a picker with no rows would make
 * the override look unavailable rather than unknown.
 */
export function nodeHasHWDeviceInventory(node: StreamNode | null | undefined): boolean {
  return reportedDevices(node).length > 0;
}

/** Paths in report order — the order `toggleHWDevice` keeps the stored value in. */
export function nodeHWDevicePaths(node: StreamNode | null | undefined): string[] {
  return reportedDevices(node).map((device) => device.path);
}

/**
 * Build the picker rows: the node's inventory, then any pinned path it does not
 * report. The second group is what keeps a stale override deselectable instead
 * of stranded — a device removed from the node would otherwise stay pinned with
 * no control to clear it.
 */
export function buildNodeHWDeviceRows(
  node: StreamNode | null | undefined,
  override: string | null | undefined,
): NodeHWDeviceRow[] {
  const selected = parseHWDeviceOverride(override);
  const rows: NodeHWDeviceRow[] = reportedDevices(node).map((device) => ({
    ...device,
    reported: true,
    selected: selected.includes(device.path),
  }));
  for (const path of selected) {
    if (rows.some((row) => row.path === path)) {
      continue;
    }
    rows.push({
      path,
      description: "Pinned device this node does not report",
      reported: false,
      selected: true,
      title: `${path} — not in this node's last capability report. A transcode pinned to a device the node cannot open falls back to software.`,
    });
  }
  return rows;
}

// --- Re-probe action -------------------------------------------------------

/** What to tell an operator once a re-probe returns. */
export interface ReprobeOutcome {
  /** The node re-probed. False covers both a refusal and an unreachable node. */
  ok: boolean;
  message: string;
}

/**
 * Describe a re-probe result against the node as it was listed before the call.
 *
 * Comparing the returned hash against the stored one is the only thing that
 * answers the question an operator actually asked — did the driver work land? —
 * so "no change" is reported as plainly as a change. An absent hash on either
 * side (an older node, or a node that never stored a report) leaves the
 * comparison unstated rather than guessed at.
 */
export function describeReprobeOutcome(
  node: StreamNode,
  result: ReprobeNodeResult,
): ReprobeOutcome {
  const name = result.node_name?.trim() || node.name;
  if (result.status !== "ok") {
    const reason = result.error?.trim() || "the node reported no reason";
    return { ok: false, message: `${name}: re-probe failed — ${reason}` };
  }

  const backend = reprobeBackendLabel(result.resolved);
  const hash = result.capability_hash?.trim() ?? "";
  const previous = node.capabilities_hash?.trim() ?? "";
  let message: string;
  if (hash !== "" && previous !== "" && hash === previous) {
    message = `${name}: re-probed, no change${backend ? ` (${backend})` : ""}`;
  } else if (hash !== "" && previous !== "") {
    message = `${name}: re-probed, hardware report changed${backend ? ` — now ${backend}` : ""}`;
  } else {
    message = `${name}: re-probed${backend ? ` — ${backend}` : ""}`;
  }
  // The node has recomputed either way; only the stored row lags, and saying so
  // stops an operator re-running the action to explain an unchanged table.
  if (!result.capabilities_refreshed) {
    message += ". The stored report will catch up on the next health check";
  }
  return { ok: true, message };
}

function reprobeBackendLabel(resolved: string | undefined): string {
  const backend = resolved?.trim().toLowerCase() ?? "";
  if (backend === "") {
    return "";
  }
  return backend === "none" ? "software" : backend.toUpperCase();
}

// --- Per-node acceleration overrides ---------------------------------------

/**
 * Select value standing in for "no override". Radix rejects an empty option
 * value, and the wire form of "inherit" is null, so the picker needs a
 * sentinel of its own.
 */
export const HW_ACCEL_INHERIT = "inherit";

/**
 * The acceleration choices a node may be pinned to, inherit first. The backend
 * values mirror the playback.hw_accel enum: a per-node override may only name a
 * backend the cluster-wide setting could also name.
 */
export const HW_ACCEL_OVERRIDE_OPTIONS: readonly { value: string; label: string }[] = [
  { value: HW_ACCEL_INHERIT, label: "Cluster default" },
  { value: "auto", label: "Auto" },
  { value: "qsv", label: "Intel Quick Sync (QSV)" },
  { value: "vaapi", label: "VA-API" },
  { value: "nvenc", label: "NVIDIA NVENC" },
  { value: "none", label: "Software" },
];

/**
 * Whether this node's devices are named as CUDA identities rather than render
 * paths.
 *
 * NVENC addresses GPUs by CUDA index or GPU UUID and takes the configured value
 * straight through as its `-hwaccel_device`, so a `/dev/dri/renderD…` path is
 * meaningless to it. The cluster-wide Playback form hides its device picker on
 * the same rule; a per-node override has to as well, or switching a node to
 * NVENC leaves it holding a render path with no way to type the CUDA identity
 * the backend actually needs.
 *
 * `override` is the backend selected in the editor. Only the inherit sentinel
 * defers to the cluster: inheriting means running whatever the cluster names,
 * so that is what decides the device syntax — not what this node resolves
 * today, which still reflects the override being given up. A node overriding
 * QSV under an NVENC cluster would otherwise keep the render-path picker while
 * inheritance is selected, leaving no way to type the CUDA identity the backend
 * needs and letting `/dev/dri/…` be saved as an NVENC policy that cannot work.
 *
 * An explicit `auto` is not inheritance. It is this node's own policy — detect
 * against your own hardware — so the cluster's backend says nothing about what
 * it will run, and its own resolution decides. That is also the fallback when
 * inheritance is selected and the cluster names no backend, since an `auto`
 * cluster is inherited as exactly that instruction.
 */
export function nodeUsesCUDADevices(
  node: StreamNode | null | undefined,
  override: string | null | undefined,
  clusterHWAccel?: string | null,
): boolean {
  const selected = override?.trim().toLowerCase() ?? "";
  if (selected === "nvenc") {
    return true;
  }
  if (selected !== "" && selected !== HW_ACCEL_INHERIT && selected !== "auto") {
    return false;
  }
  if (selected === "" || selected === HW_ACCEL_INHERIT) {
    const cluster = clusterHWAccel?.trim().toLowerCase() ?? "";
    if (cluster !== "" && cluster !== "auto") {
      return cluster === "nvenc";
    }
  }
  return node?.capabilities?.resolved?.trim().toLowerCase() === "nvenc";
}

/**
 * Whether moving the backend selection from `current` to `next` leaves the
 * device override meaningless.
 *
 * The two syntaxes are not interchangeable: a render path is not a CUDA index
 * or UUID and neither is settable as the other, so a value written for one is
 * not a value for the other — it is a policy that cannot work. Crossing between
 * them drops it.
 *
 * Takes both selections rather than watching the effective syntax, because that
 * also moves on its own: the cluster setting arrives after the first render,
 * and treating that as a change would erase a valid override on a dialog nobody
 * had touched.
 */
export function hwDeviceSyntaxChanges(
  node: StreamNode | null | undefined,
  current: string | null | undefined,
  next: string | null | undefined,
  clusterHWAccel?: string | null,
): boolean {
  return (
    nodeUsesCUDADevices(node, current, clusterHWAccel) !==
    nodeUsesCUDADevices(node, next, clusterHWAccel)
  );
}

/** A node's own acceleration policy, as rendered beside its GPU inventory. */
export interface NodeAccelerationOverride {
  /** Compact row text, e.g. "override: qsv · /dev/dri/renderD129". */
  label: string;
  /** Hover text: both halves of the policy, and when a change lands. */
  title: string;
}

/**
 * Split a stored comma-separated device override into its paths. Same rules as
 * the cluster-wide picker's list, which is why they share an implementation.
 */
export function parseHWDeviceOverride(value: string | null | undefined): string[] {
  return parseHWDeviceList(value);
}

/**
 * Describe how a node resolves hardware acceleration, or null when it resolves
 * the cluster-wide way. Null is the normal case and renders nothing: the Nodes
 * table would otherwise repeat the Playback settings page on every row, and the
 * point of the line is to mark the rows that do *not* follow it.
 */
export function describeNodeAccelerationOverride(
  node: StreamNode,
): NodeAccelerationOverride | null {
  const accel = node.hw_accel_override?.trim().toLowerCase() ?? "";
  const devices = parseHWDeviceOverride(node.hw_device_override);
  if (accel === "" && devices.length === 0) {
    return null;
  }

  const accelLabel = accel === "none" ? "software" : accel;
  const firstDevice = devices[0] ?? "";
  const deviceLabel = devices.length === 1 ? firstDevice : `${devices.length} devices`;
  const label = [accelLabel, devices.length > 0 ? deviceLabel : ""]
    .filter((part) => part !== "")
    .join(" · ");

  const title = [
    accel === ""
      ? "Acceleration: inherited from the cluster-wide Hardware Acceleration setting."
      : `Acceleration: ${accelLabel}, overriding the cluster-wide Hardware Acceleration setting.`,
    devices.length === 0
      ? "GPU devices: inherited from the cluster-wide setting."
      : `GPU devices: ${devices.join(", ")}.`,
    "A changed override applies to new transcodes within a minute; sessions already running keep the backend they started with.",
  ].join("\n");

  return { label: `override: ${label}`, title };
}

// --- Host resource samples -------------------------------------------------
//
// A node's last_stats and the API host's /admin/system/resources carry the same
// shapes, so both surfaces derive their numbers here. Nothing below invents a
// value: a field the sampler could not measure renders as a dash, never as a
// zero, because a zero on a dashboard is read as "measured and idle".

/** Placeholder for a reading the sample does not carry. */
const DASH = "—";

/**
 * Fill percentage at which a mount is called out. A transcode scratch volume
 * that fills stops transcodes with no other warning, and the last few percent
 * of a volume disappear fast under a segment writer, so the threshold sits far
 * enough below full to leave an operator time to act.
 */
export const DISK_FILL_WARNING_PCT = 85;

/** One derived reading, ready to render in a table cell or a stat tile. */
export interface ResourceMetric {
  label: string;
  /** Rendered value, or a dash when `muted`. */
  value: string;
  /** Short secondary line for the dashboard tiles; empty when there is none. */
  detail: string;
  /** Hover text explaining the value, or why there is none. */
  title: string;
  /** Nothing measured this reading: render it in the muted color. */
  muted: boolean;
  /** Past an attention threshold; render with the warning tint. */
  warning: boolean;
  /**
   * 0–100 for a load meter, or null when this reading has no meter to draw.
   *
   * Null covers two different cases that must render the same way: nothing
   * measured the reading, and the reading has no ceiling to measure against
   * (throughput, or a node with no configured cap). Both get an empty track
   * rather than a bar at zero, because a bar at zero is read as "measured and
   * idle" — the same reason `muted` values render as a dash and never a 0.
   */
  fill: number | null;
}

export type NodeSystemPresentation =
  | {
      kind: "unreported";
      /** Dash, so the column keeps its shape. */
      label: string;
      title: string;
    }
  | {
      kind: "reported";
      cpu: ResourceMetric;
      memory: ResourceMetric;
      disk: ResourceMetric;
      network: ResourceMetric;
    };

/**
 * Describe a node's System column from the sample its last health check
 * carried.
 *
 * An unhealthy node reports nothing rather than its last numbers: the sample
 * predates the check that failed, and a frozen CPU percentage is indis-
 * tinguishable from a live one on screen.
 */
export function describeNodeSystem(node: StreamNode): NodeSystemPresentation {
  const system = node.last_stats?.system;
  if (!system) {
    return {
      kind: "unreported",
      label: DASH,
      title: node.healthy
        ? "This node reported no resource sample. Sampling is Linux-only, and a node running a build from before resource sampling reports none."
        : "This node is not answering health checks, so it has no current resource sample.",
    };
  }
  if (!node.healthy) {
    return {
      kind: "unreported",
      label: DASH,
      title:
        "The last health check did not reach this node, so its most recent resource sample is no longer current.",
    };
  }
  return describeSystemStats(system);
}

/** Derive the four host readings from one system sample. */
export function describeSystemStats(system: HostSystemStats): {
  kind: "reported";
  cpu: ResourceMetric;
  memory: ResourceMetric;
  disk: ResourceMetric;
  network: ResourceMetric;
} {
  return {
    kind: "reported",
    cpu: describeCPU(system),
    memory: describeMemory(system),
    disk: describeWorstDisk(system.disks ?? []),
    network: describeNetwork(system),
  };
}

function describeCPU(system: HostSystemStats): ResourceMetric {
  const cpu = finiteNumber(system.cpu_pct);
  const cores = finiteNumber(system.cores);
  const load1 = finiteNumber(system.load1);
  if (cpu == null) {
    return mutedMetric("CPU", "This sample carries no CPU reading.");
  }

  const percent = clampPercent(cpu);
  const detail = [
    cores != null && cores > 0 ? (cores === 1 ? "1 core" : `${cores} cores`) : null,
    load1 != null ? `load ${load1.toFixed(2)}` : null,
  ]
    .filter((part): part is string => part !== null)
    .join(" · ");

  const title = [
    `${percent}% busy across all cores over the last sampling interval.`,
    cores != null && cores > 0
      ? `${cores} CPU(s) available to this host — its cgroup quota where one is set.`
      : null,
    load1 != null
      ? `1-minute load ${load1.toFixed(2)}, which also counts tasks blocked on storage.`
      : null,
  ]
    .filter((part): part is string => part !== null)
    .join(" ");

  return {
    label: "CPU",
    value: `${percent}%`,
    detail,
    title,
    muted: false,
    warning: false,
    fill: percent,
  };
}

function describeMemory(system: HostSystemStats): ResourceMetric {
  const used = finiteNumber(system.mem_used_mb);
  const total = finiteNumber(system.mem_total_mb);
  if (used == null || total == null || total <= 0) {
    return mutedMetric("RAM", "This sample carries no memory reading.");
  }

  const value = formatUsedOfTotal(used, total, mebibytesToBytes) ?? DASH;
  const percent = clampPercent((used / total) * 100);
  return {
    label: "RAM",
    value,
    detail: `${percent}% used`,
    title: `${value} used (${percent}%). Under a cgroup this is the container's limit and working set, not the host's.`,
    muted: false,
    warning: false,
    fill: percent,
  };
}

/**
 * The fullest sampled mount, which for a transcode node is its scratch dir —
 * the only mount it samples. Reporting the worst rather than the first keeps
 * the same rule working on the API host, which also samples the media roots.
 */
export function describeWorstDisk(disks: readonly HostDiskStats[]): ResourceMetric {
  const measured = disks
    .map((disk) => ({ disk, fill: diskFillPercent(disk) }))
    .filter((entry): entry is { disk: HostDiskStats; fill: number } => entry.fill !== null);

  const title = disks.length > 0 ? disks.map(describeDiskLine).join("\n") : "";
  if (measured.length === 0) {
    // Paths that exist but could not be measured are still worth naming in the
    // tooltip, so an operator sees which mount went away rather than a dash
    // with no explanation.
    return mutedMetric("Disk", title === "" ? "This sample carries no disk reading." : title);
  }

  const worst = measured.reduce((a, b) => (b.fill > a.fill ? b : a));
  const name = diskName(worst.disk);
  return {
    label: "Disk",
    value: `${worst.fill}%`,
    detail: name === "" ? "full" : name,
    title,
    muted: false,
    warning: worst.fill >= DISK_FILL_WARNING_PCT,
    fill: worst.fill,
  };
}

function describeNetwork(system: HostSystemStats): ResourceMetric {
  const rx = formatBitsPerSecond(system.net_rx_bps);
  const tx = formatBitsPerSecond(system.net_tx_bps);
  if (rx === null && tx === null) {
    return mutedMetric("Net", "This sample carries no network reading.");
  }

  return {
    label: "Net",
    value: `↓ ${rx ?? DASH} · ↑ ${tx ?? DASH}`,
    detail: "rx · tx",
    title: `Aggregate throughput with loopback excluded: ${rx ?? DASH} in, ${tx ?? DASH} out. In a container this is the container's own network namespace.`,
    muted: false,
    warning: false,
    // A link's throughput has no ceiling this sample knows: the sampler reports
    // bytes moved, never the interface's negotiated speed, so there is nothing
    // honest to draw a bar against.
    fill: null,
  };
}

export type ResourceSamplePresentation =
  | {
      kind: "unavailable";
      title: string;
    }
  | {
      kind: "sampled";
      cpu: ResourceMetric;
      memory: ResourceMetric;
      disk: ResourceMetric;
      network: ResourceMetric;
      /** Busiest GPU's video engine; null when this host reports no GPU. */
      gpu: ResourceMetric | null;
      /** When the sample was taken, for a freshness label; null when unstamped. */
      sampledAt: string | null;
    };

/**
 * Describe the API host's own sample. A server predating the endpoint answers
 * 404 and leaves `resources` undefined, which is the same story as a host that
 * cannot be sampled: no numbers, said plainly, rather than an error.
 */
export function describeResourceSample(
  resources: SystemResources | undefined | null,
): ResourceSamplePresentation {
  const system = resources?.system;
  if (!resources || resources.available !== true || !system) {
    return {
      kind: "unavailable",
      title:
        "This host is not being sampled. Resource sampling is Linux-only, and the first sample lands a few seconds after startup.",
    };
  }

  return {
    ...describeSystemStats(system),
    kind: "sampled",
    gpu: describeGPUBusy(resources.gpu ?? []),
    sampledAt: resources.sampled_at?.trim() || null,
  };
}

/**
 * The busiest GPU's video engine, which is the one an operator asks about when
 * transcodes queue. Averaging would hide a saturated card behind an idle one.
 * Null when the host reports no GPU at all, so the tile is omitted rather than
 * showing a zero.
 */
export function describeGPUBusy(gpu: readonly HostGPUStats[]): ResourceMetric | null {
  if (gpu.length === 0) {
    return null;
  }

  const sessions = gpu.reduce((total, stats) => total + (finiteNumber(stats.sessions) ?? 0), 0);
  const sessionLabel = sessions === 1 ? "1 session" : `${sessions} sessions`;
  const title = gpu
    .map((stats) => {
      const device = stats.device?.trim() || "(unknown device)";
      const measured = isMeasuredGPUSource(stats.source);
      const video = finiteNumber(stats.video_busy_pct);
      const reading = measured && video != null ? `video ${clampPercent(video)}%` : "not measured";
      const count = Math.max(0, Math.trunc(finiteNumber(stats.sessions) ?? 0));
      return `${device} — ${reading} · ${count === 1 ? "1 session" : `${count} sessions`}`;
    })
    .join("\n");

  const busiest = gpu
    .filter((stats) => isMeasuredGPUSource(stats.source))
    .map((stats) => finiteNumber(stats.video_busy_pct))
    .filter((value): value is number => value !== null)
    .reduce<number | null>((best, value) => (best === null || value > best ? value : best), null);

  if (busiest === null) {
    return mutedMetric("GPU", title === "" ? "No GPU reading in this sample." : title);
  }

  const label = gpu.length === 1 ? "video engine" : `busiest of ${gpu.length} GPUs`;
  return {
    label: "GPU",
    value: `${clampPercent(busiest)}%`,
    detail: `${label} · ${sessionLabel}`,
    title,
    muted: false,
    warning: false,
    fill: clampPercent(busiest),
  };
}

// --- Groups ----------------------------------------------------------------
//
// A group is not a label an operator hangs on a node; it is a co-location
// contract the planner enforces. `internal/nodepool` pairs a transcode node
// with a proxy from its own group so transcoded bytes never cross the LAN
// twice, and it holds that pairing strictly: a grouped transcode node takes
// work only while its whole group is healthy, and a group that has proxies of
// its own never spills onto another group's.
//
// So "which nodes belong to each other" is the same question as "which nodes
// fail together", and everything derived here answers both.

/** A node's group bucket: its trimmed label, or "" when it has none. */
export function nodeGroupOf(node: StreamNode): string {
  return node.group?.trim() ?? "";
}

/** One bucket in the group filter. */
export interface NodeGroupOption {
  /** The stored group label, or "" for the ungrouped bucket. */
  value: string;
  label: string;
  count: number;
  proxies: number;
  transcodes: number;
  /**
   * An enabled member is unhealthy, so the planner has taken this group out of
   * service: its transcode nodes stop being selected, and because a group
   * holding proxies never falls back to another group's, nothing covers for
   * them. Disabled members do not count against it — the pools only hold
   * enabled nodes.
   *
   * Always false for the ungrouped bucket, which is not a group and has no
   * pairing to lose.
   */
  degraded: boolean;
  /** Hover text: what is in the bucket, and what that means for routing. */
  title: string;
}

/**
 * Bucket every node by group, in the order the filter offers them: groups
 * alphabetically, then the ungrouped bucket, which is a leftover rather than a
 * group and so never sorts among them.
 *
 * A bucket exists only because a node is in it, so there are no empty ones and
 * no configured-but-unused group can appear — the node list is the only place
 * a group is recorded.
 */
export function describeNodeGroups(nodes: readonly StreamNode[]): NodeGroupOption[] {
  const buckets = new Map<string, StreamNode[]>();
  for (const node of nodes) {
    const key = nodeGroupOf(node);
    const existing = buckets.get(key);
    if (existing) {
      existing.push(node);
    } else {
      buckets.set(key, [node]);
    }
  }

  return [...buckets.entries()]
    .map(([value, members]) => describeNodeGroup(value, members))
    .sort((a, b) => {
      if (a.value === "") return 1;
      if (b.value === "") return -1;
      return a.label.localeCompare(b.label);
    });
}

function describeNodeGroup(value: string, members: readonly StreamNode[]): NodeGroupOption {
  const proxies = members.filter((node) => node.type === "proxy").length;
  const transcodes = members.filter((node) => node.type === "transcode").length;
  // Enabled members only, matching nodepool's groupHealth: the pools hold no
  // disabled nodes, so switching one off takes it out of the group rather than
  // holding the group down.
  const degraded = value !== "" && members.some((node) => node.enabled && !node.healthy);

  const lines = [describeGroupComposition(proxies, transcodes)];
  if (value === "") {
    lines.push(
      "These nodes have no group, so each is selected on its own: the least-loaded transcode node, and any proxy in the cluster.",
    );
  } else if (proxies > 0 && transcodes > 0) {
    lines.push(
      "Transcodes on this group's nodes are served by a proxy in the same group, so the stream stays on one LAN.",
    );
  } else if (transcodes > 0) {
    lines.push(
      "No proxy in this group, so transcodes here are served by any proxy in the cluster.",
    );
  } else {
    lines.push("No transcode node in this group, so nothing is pinned to these proxies.");
  }
  if (degraded) {
    lines.push(
      "Out of service: an enabled member is not healthy, so transcode nodes in this group stop taking work — and a group with its own proxies never falls back to another group's.",
    );
  }

  return {
    value,
    label: value === "" ? "Ungrouped" : value,
    count: members.length,
    proxies,
    transcodes,
    degraded,
    title: lines.join("\n"),
  };
}

function describeGroupComposition(proxies: number, transcodes: number): string {
  const parts = [
    proxies > 0 ? (proxies === 1 ? "1 proxy node" : `${proxies} proxy nodes`) : null,
    transcodes > 0
      ? transcodes === 1
        ? "1 transcode node"
        : `${transcodes} transcode nodes`
      : null,
  ].filter((part): part is string => part !== null);
  return `${parts.join(", ")}.`;
}

/**
 * Narrow a node list to one group. A null group is no filter at all, which is
 * a different thing from the ungrouped bucket ("").
 */
export function filterNodesByGroup(
  nodes: readonly StreamNode[],
  group: string | null,
): StreamNode[] {
  if (group === null) {
    return [...nodes];
  }
  return nodes.filter((node) => nodeGroupOf(node) === group);
}

// --- Node capacity ---------------------------------------------------------
//
// What a node is carrying against what it was allowed to carry. Both readings
// are ResourceMetrics so they render with the same meter as CPU and disk: an
// operator deciding whether a node can take another stream is asking the same
// question of all four.

/**
 * Fraction of a cap that is in use, or null when there is no cap. An uncapped
 * node is the default and the common case, and it genuinely has no meter: the
 * count is real, but drawing it against a ceiling this page invented would
 * report a node as nearly full on the strength of a made-up number.
 */
function capacityFill(used: number, cap: number | null): number | null {
  return cap != null && cap > 0 ? clampPercent((used / cap) * 100) : null;
}

/**
 * Whether this node's card has hardware acceleration to show at all.
 *
 * Only a transcode node does. A proxy relays bytes and runs identity/remux
 * recipes on ffmpeg; it never executes a hardware transcode, so it stopped
 * probing for one and its capability report now carries no backend, no device,
 * and no GPU identity. Drawing the acceleration block for it would report
 * "software" as a finding rather than as the absence of a question, and the
 * re-probe button beside it promises a hardware re-verification a proxy no
 * longer performs.
 */
export function nodeReportsAcceleration(node: Pick<StreamNode, "type">): boolean {
  return node.type !== "proxy";
}

/** Concurrency: transcodes on a transcode node, relayed streams on a proxy. */
export function describeNodeJobs(node: StreamNode): ResourceMetric {
  const label = node.type === "proxy" ? "Streams" : "Transcodes";
  const active = Math.max(0, Math.trunc(finiteNumber(node.active_jobs) ?? 0));
  // A cap at or below zero is how the API spells "unlimited", which is also
  // what an absent one means.
  const cap = finiteNumber(node.max_jobs);
  const capped = cap != null && cap > 0 ? cap : null;

  return {
    label,
    value: capped == null ? `${active}` : `${active} / ${capped}`,
    detail: capped == null ? "no cap" : "in use",
    title:
      capped == null
        ? `${active} running. This node has no concurrency cap, so the planner places work on it by load alone.`
        : `${active} of ${capped} running. New work goes elsewhere once this node is full.`,
    muted: false,
    warning: capped != null && active >= capped,
    fill: capacityFill(active, capped),
  };
}

/** Measured egress against a proxy node's bandwidth cap. */
export function describeNodeEgress(node: StreamNode): ResourceMetric {
  const kbps = Math.max(0, finiteNumber(node.egress_kbps) ?? 0);
  const cap = finiteNumber(node.max_bandwidth_kbps);
  const capped = cap != null && cap > 0 ? cap : null;
  const used = formatMbps(kbps);

  return {
    label: "Egress",
    value: capped == null ? `${used} Mbps` : `${used} / ${formatMbps(capped)} Mbps`,
    detail: capped == null ? "no cap" : "of cap",
    title:
      capped == null
        ? `Measured egress ${used} Mbps. This node has no bandwidth cap.`
        : `Measured egress ${used} Mbps of ${formatMbps(capped)} Mbps. New streams are routed elsewhere once this node's egress plus the new stream's expected bitrate would exceed the cap; streams already running are never interrupted.`,
    muted: false,
    warning: capped != null && kbps >= capped,
    fill: capacityFill(kbps, capped),
  };
}

/** Kilobits per second as Mbps to one decimal, trailing zero dropped. */
function formatMbps(kbps: number): string {
  return (Math.round(kbps / 100) / 10).toString();
}

/**
 * Name a mount for display: its path where the surface carried one, otherwise
 * its role.
 *
 * A node's `/health` takes no credential, so it reports what a mount is for and
 * not where it is — the same reason `/metrics` labels its disk series by role.
 * Everything the Nodes page draws comes from `last_stats`, which is that
 * response, so "scratch" or "library-2" is what an operator sees there; the
 * paths are on the admin-authenticated resources endpoint.
 */
function diskName(disk: HostDiskStats): string {
  return disk.path?.trim() || disk.role?.trim() || "";
}

function describeDiskLine(disk: HostDiskStats): string {
  const path = diskName(disk) || "(unknown mount)";
  if (disk.unavailable) {
    return `${path} — unavailable on this host`;
  }
  const fill = diskFillPercent(disk);
  if (fill === null) {
    return `${path} — no capacity reported`;
  }
  const capacity = formatUsedOfTotal(disk.used_gb, disk.total_gb, gibibytesToBytes);
  const line = `${path} — ${fill}% full${capacity ? ` (${capacity})` : ""}`;
  // "Real but old" is the normal reading for a network mount whose server went
  // away, and it is a different fact from "never measured".
  return disk.stale ? `${line}, carried over from an earlier pass` : line;
}

function diskFillPercent(disk: HostDiskStats): number | null {
  if (disk.unavailable) {
    return null;
  }
  const used = finiteNumber(disk.used_gb);
  const total = finiteNumber(disk.total_gb);
  if (used == null || total == null || total <= 0) {
    return null;
  }
  return clampPercent((used / total) * 100);
}

/** "12.4 GiB of 31.3 GiB", or null when either side is missing. */
function formatUsedOfTotal(
  used: number | null | undefined,
  total: number | null | undefined,
  toBytes: (value: number) => number,
): string | null {
  const usedValue = finiteNumber(used);
  const totalValue = finiteNumber(total);
  if (usedValue == null || totalValue == null || totalValue <= 0) {
    return null;
  }
  const usedLabel = formatFileSize(toBytes(usedValue), { iecUnits: true, fallback: "0 B" });
  const totalLabel = formatFileSize(toBytes(totalValue), { iecUnits: true });
  return `${usedLabel} of ${totalLabel}`;
}

/**
 * Humanize a *bits*-per-second rate, which is the unit the sampler reports and
 * the unit egress is already quoted in elsewhere in the cluster. Decimal scale,
 * because network rates are decimal everywhere they are quoted.
 */
export function formatBitsPerSecond(bps: number | null | undefined): string | null {
  const value = finiteNumber(bps);
  if (value == null || value < 0) {
    return null;
  }
  if (value >= 1e9) {
    return `${(value / 1e9).toFixed(1)} Gbps`;
  }
  if (value >= 1e6) {
    return `${(value / 1e6).toFixed(1)} Mbps`;
  }
  if (value >= 1e3) {
    return `${Math.round(value / 1e3)} kbps`;
  }
  return `${Math.round(value)} bps`;
}

function mebibytesToBytes(value: number): number {
  return value * 1024 ** 2;
}

function gibibytesToBytes(value: number): number {
  return value * 1024 ** 3;
}

function mutedMetric(label: string, title: string): ResourceMetric {
  return { label, value: DASH, detail: "", title, muted: true, warning: false, fill: null };
}

function finiteNumber(value: number | null | undefined): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function clampPercent(value: number): number {
  return Math.min(100, Math.max(0, Math.round(value)));
}
