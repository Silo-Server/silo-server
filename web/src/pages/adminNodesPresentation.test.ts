// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { HostSystemStats, ReprobeNodeResult, StreamNode } from "@/api/types";
import {
  CAPABILITY_STALE_AFTER_MS,
  DISK_FILL_WARNING_PCT,
  HW_ACCEL_INHERIT,
  buildNodeHWDeviceRows,
  describeCapabilityDrift,
  describeGPUBusy,
  describeNodeAccelerationOverride,
  describeNodeEgress,
  describeNodeGPU,
  describeNodeGroups,
  describeNodeJobs,
  describeNodeSystem,
  describeReprobeOutcome,
  describeResourceSample,
  describeSharedGPU,
  filterNodesByGroup,
  nodeHWDevicePaths,
  nodeHasHWDeviceInventory,
  hwDeviceSyntaxChanges,
  nodeUsesCUDADevices,
} from "./adminNodesPresentation";

const NOW = Date.parse("2026-08-26T12:00:00Z");

function makeNode(overrides: Partial<StreamNode> = {}): StreamNode {
  return {
    id: 1,
    name: "transcode-1",
    type: "transcode",
    url: "http://10.0.0.5:8082",
    enabled: true,
    healthy: true,
    active_jobs: 0,
    group: null,
    max_jobs: null,
    max_bandwidth_kbps: null,
    egress_kbps: 0,
    last_health_check: "2026-08-26T11:59:50Z",
    created_at: "2026-08-01T00:00:00Z",
    ...overrides,
  };
}

describe("describeNodeGPU", () => {
  it("reports a node with no stored capabilities as awaiting its first report", () => {
    expect(describeNodeGPU(makeNode(), NOW)).toEqual({
      kind: "awaiting",
      label: "Awaiting first report",
      title: "No hardware capability report has been stored for this node yet.",
    });
  });

  it("marks the resolved backend verified when its probe passed", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: {
          resolved: "qsv",
          detected_backends: [
            {
              backend: "qsv",
              verified: true,
              devices: ["/dev/dri/renderD128"],
              device: "/dev/dri/renderD128",
            },
          ],
        },
        capabilities_refreshed_at: "2026-08-26T11:59:00Z",
      }),
      NOW,
    );

    expect(presentation).toMatchObject({
      kind: "reported",
      backend: {
        label: "QSV",
        state: "verified",
        badgeClass: "bg-success/10 text-success border-success/15",
        title: "QSV verified by FFmpeg probe on /dev/dri/renderD128.",
      },
      failures: [],
      stale: null,
    });
  });

  it("warns with the failure reason when the resolved backend failed its probe", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: {
          resolved: "qsv",
          detected_backends: [
            {
              backend: "qsv",
              verified: false,
              reason: "h264_qsv smoke encode failed: device busy",
            },
          ],
        },
      }),
      NOW,
    );

    expect(presentation).toMatchObject({
      kind: "reported",
      backend: {
        label: "QSV",
        state: "failed",
        badgeClass: "bg-warning/10 text-warning border-warning/15",
        title: "QSV probe failed: h264_qsv smoke encode failed: device busy",
      },
      failures: [],
    });
  });

  it("lists failed backends other than the resolved one", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: {
          resolved: "vaapi",
          detected_backends: [
            { backend: "qsv", verified: false, reason: "h264_qsv encoder unavailable" },
            { backend: "vaapi", verified: true, device: "/dev/dri/renderD128" },
          ],
        },
      }),
      NOW,
    );

    expect(presentation.kind === "reported" && presentation.failures).toEqual([
      { label: "QSV", reason: "h264_qsv encoder unavailable" },
    ]);
  });

  it("does not warn about skipped backends whose devices are inaccessible", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: {
          resolved: "none",
          detected_backends: [
            {
              backend: "qsv",
              verified: false,
              skipped: true,
              reason: "/dev/dri/renderD128: device not accessible on this node",
            },
            {
              backend: "vaapi",
              verified: false,
              skipped: true,
              reason: "/dev/dri/renderD128: device not accessible on this node",
            },
          ],
        },
      }),
      NOW,
    );

    expect(presentation.kind === "reported" && presentation.failures).toEqual([]);
    expect(presentation.kind === "reported" && presentation.backend.label).toBe("SW");
    expect(presentation.kind === "reported" && presentation.backend.title).toContain(
      "not accessible on this node",
    );
  });

  it("falls back to software with no hardware backend resolved", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: { resolved: "none", render_devices: [], render_device_details: [] },
      }),
      NOW,
    );

    expect(presentation).toMatchObject({
      kind: "reported",
      backend: { label: "SW", state: "none" },
      deviceSummary: null,
      deviceTitle: null,
    });
  });

  it("treats a configured backend with no probe entry as unverified, not failed", () => {
    const presentation = describeNodeGPU(makeNode({ capabilities: { resolved: "qsv" } }), NOW);

    expect(presentation).toMatchObject({
      kind: "reported",
      backend: {
        label: "QSV",
        state: "unverified",
        title: "QSV is in use but this node reported no verification probe for it.",
      },
    });
  });

  it("marks a node stale once the health checks that confirm its report stop", () => {
    const node = makeNode({
      capabilities: { resolved: "qsv" },
      capabilities_refreshed_at: new Date(NOW - 6 * 60 * 60 * 1000).toISOString(),
      last_health_check: new Date(NOW - CAPABILITY_STALE_AFTER_MS - 1000).toISOString(),
    });

    expect(describeNodeGPU(node, NOW)).toMatchObject({ stale: "unconfirmed" });
    // The same node read earlier was still being checked: the clock decides.
    expect(describeNodeGPU(node, NOW - CAPABILITY_STALE_AFTER_MS)).toMatchObject({ stale: null });
  });

  // The sweep refetches only when a node advertises a changed hash, so an
  // untouched GPU keeps its original report forever by design. Calling that
  // stale would light the warning on every steady-state node.
  it("does not call an old report stale while health checks keep confirming it", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: { resolved: "qsv" },
        capabilities_refreshed_at: new Date(NOW - 6 * 60 * 60 * 1000).toISOString(),
        last_health_check: new Date(NOW - 20 * 1000).toISOString(),
      }),
      NOW,
    );

    expect(presentation).toMatchObject({ stale: null });
  });

  it("does not call an unhealthy node's report stale", () => {
    const presentation = describeNodeGPU(
      makeNode({
        healthy: false,
        capabilities: { resolved: "qsv" },
        capabilities_refreshed_at: new Date(NOW - 24 * 60 * 60 * 1000).toISOString(),
        last_health_check: new Date(NOW - 24 * 60 * 60 * 1000).toISOString(),
      }),
      NOW,
    );

    expect(presentation).toMatchObject({ stale: null });
  });

  it("is not stale when the server sent no refresh timestamp", () => {
    expect(describeNodeGPU(makeNode({ capabilities: { resolved: "qsv" } }), NOW)).toMatchObject({
      stale: null,
    });
  });

  it("is not stale when the server sent no health check timestamp", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: { resolved: "qsv" },
        capabilities_refreshed_at: new Date(NOW - 6 * 60 * 60 * 1000).toISOString(),
        last_health_check: null,
      }),
      NOW,
    );

    expect(presentation).toMatchObject({ stale: null });
  });

  it("reports no live devices for a node whose server sends no last_stats", () => {
    const presentation = describeNodeGPU(makeNode({ capabilities: { resolved: "qsv" } }), NOW);

    expect(presentation.kind === "reported" && presentation.live).toEqual([]);
  });

  it("matches a live reading to the inventory device it names", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: {
          resolved: "qsv",
          render_device_details: [{ path: "/dev/dri/renderD128", description: "Intel GPU" }],
        },
        last_stats: {
          gpu: [
            {
              device: "/dev/dri/renderD128",
              vendor: "intel",
              sessions: 2,
              video_busy_pct: 42,
              render_busy_pct: 12,
              source: "fdinfo",
            },
          ],
        },
      }),
      NOW,
    );

    expect(presentation.kind === "reported" && presentation.live).toEqual([
      {
        key: "/dev/dri/renderD128",
        label: "renderD128",
        busy: "42%",
        busyFill: 42,
        busyMuted: false,
        sessions: "2 sessions",
        title: ["/dev/dri/renderD128 — Intel GPU", "video 42% · render 12%", "source: fdinfo"].join(
          "\n",
        ),
      },
    ]);
  });

  it("matches a live reading by PCI address when the inventory has no matching path", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: {
          resolved: "vaapi",
          render_device_details: [
            {
              path: "/dev/dri/renderD129",
              pci_address: "0000:03:00.0",
              description: "AMD GPU",
            },
          ],
        },
        last_stats: {
          gpu: [{ device: "0000:03:00.0", sessions: 1, video_busy_pct: 7, source: "fdinfo" }],
        },
      }),
      NOW,
    );

    expect(presentation.kind === "reported" && presentation.live[0]).toMatchObject({
      label: "0000:03:00.0",
      sessions: "1 session",
      title: expect.stringContaining("0000:03:00.0 — AMD GPU"),
    });
  });

  it("keeps an unmatched device rather than dropping the reading", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: { resolved: "nvenc", render_device_details: [] },
        last_stats: {
          gpu: [
            {
              device: "cuda:0",
              vendor: "nvidia",
              sessions: 0,
              video_busy_pct: 61,
              total_busy_pct: 74,
              vram_used_mb: 1024,
              vram_total_mb: 8192,
              source: "nvidia-smi",
            },
          ],
        },
      }),
      NOW,
    );

    expect(presentation.kind === "reported" && presentation.live[0]).toEqual({
      key: "cuda:0",
      label: "cuda:0",
      busy: "61%",
      busyFill: 61,
      busyMuted: false,
      sessions: "idle",
      title: [
        "cuda:0",
        "video 61%",
        "whole GPU 74% (all tenants)",
        "VRAM 1.0 GiB of 8.0 GiB",
        "source: nvidia-smi",
      ].join("\n"),
    });
  });

  // The zeros an unavailable source reports are placeholders, and an operator
  // who reads them as an idle GPU draws the wrong conclusion.
  it("mutes the busy percentage when nothing measured the device", () => {
    const presentation = describeNodeGPU(
      makeNode({
        capabilities: { resolved: "qsv" },
        last_stats: {
          gpu: [{ device: "/dev/dri/renderD128", sessions: 1, source: "unavailable" }],
        },
      }),
      NOW,
    );

    expect(presentation.kind === "reported" && presentation.live[0]).toMatchObject({
      busy: "—",
      busyMuted: true,
      sessions: "1 session",
      title: expect.stringContaining("No source could measure this device"),
    });
  });

  it("drops live readings for an unhealthy node whose sample stopped moving", () => {
    const presentation = describeNodeGPU(
      makeNode({
        healthy: false,
        capabilities: { resolved: "qsv" },
        last_stats: {
          gpu: [
            { device: "/dev/dri/renderD128", sessions: 3, video_busy_pct: 90, source: "fdinfo" },
          ],
        },
      }),
      NOW,
    );

    expect(presentation.kind === "reported" && presentation.live).toEqual([]);
  });
});

describe("describeSharedGPU", () => {
  const nvidiaA = makeNode({ id: 2, name: "transcode-a", physical_gpu_keys: ["GPU-aaa"] });
  const nvidiaB = makeNode({ id: 3, name: "transcode-b", physical_gpu_keys: ["GPU-aaa"] });
  const unique = makeNode({ id: 4, name: "transcode-c", physical_gpu_keys: ["GPU-ccc"] });

  it("says nothing when a node's GPUs are its own", () => {
    expect(describeSharedGPU(unique, [unique, nvidiaA, nvidiaB])).toBeNull();
  });

  it("names the other node on the same card, from either side", () => {
    const nodes = [nvidiaA, nvidiaB, unique];
    expect(describeSharedGPU(nvidiaA, nodes)).toEqual({
      label: "Shared GPU",
      title: "Shares a physical GPU with: transcode-b",
    });
    expect(describeSharedGPU(nvidiaB, nodes)).toEqual({
      label: "Shared GPU",
      title: "Shares a physical GPU with: transcode-a",
    });
  });

  it("matches on one key of several, across node types", () => {
    const dualGPU = makeNode({
      id: 5,
      name: "transcode-dual",
      physical_gpu_keys: ["GPU-aaa", "boot-1|0000:04:00.0"],
    });
    const proxy = makeNode({
      id: 6,
      name: "proxy-same-host",
      type: "proxy",
      physical_gpu_keys: ["boot-1|0000:04:00.0"],
    });

    expect(describeSharedGPU(dualGPU, [dualGPU, nvidiaA, proxy])).toEqual({
      label: "Shared GPU",
      title: "Shares a physical GPU with: transcode-a, proxy-same-host",
    });
  });

  it("does not match a node against itself when the list repeats its id", () => {
    expect(describeSharedGPU(nvidiaA, [nvidiaA, nvidiaA])).toBeNull();
  });
});

const FULL_SAMPLE: HostSystemStats = {
  cpu_pct: 42,
  load1: 1.35,
  cores: 8,
  mem_used_mb: 12800,
  mem_total_mb: 32000,
  disks: [{ path: "/tmp/silo-transcode", used_gb: 435, total_gb: 500 }],
  net_rx_bps: 12_400_000,
  net_tx_bps: 3_100_000,
};

describe("describeNodeSystem", () => {
  // A frozen CPU percentage is indistinguishable from a live one on screen.
  it("shows dashes for an unhealthy node still carrying an older sample", () => {
    expect(
      describeNodeSystem(makeNode({ healthy: false, last_stats: { system: FULL_SAMPLE } })),
    ).toMatchObject({
      kind: "unreported",
      label: "—",
      title: expect.stringContaining("no longer current"),
    });
  });

  it("derives every reading from a complete sample", () => {
    const system = describeNodeSystem(makeNode({ last_stats: { system: FULL_SAMPLE } }));

    expect(system).toMatchObject({
      kind: "reported",
      cpu: { label: "CPU", value: "42%", detail: "8 cores · load 1.35", muted: false, fill: 42 },
      memory: {
        label: "RAM",
        value: "12.5 GiB of 31.3 GiB",
        detail: "40% used",
        muted: false,
        fill: 40,
      },
      disk: {
        label: "Disk",
        value: "87%",
        detail: "/tmp/silo-transcode",
        title: "/tmp/silo-transcode — 87% full (435.0 GiB of 500.0 GiB)",
        muted: false,
        warning: true,
        fill: 87,
      },
      network: { label: "Net", value: "↓ 12.4 Mbps · ↑ 3.1 Mbps", muted: false, fill: null },
    });
  });

  it("warns exactly at the disk fill threshold and not one point below", () => {
    const atThreshold = describeNodeSystem(
      makeNode({
        last_stats: {
          system: { disks: [{ path: "/scratch", used_gb: DISK_FILL_WARNING_PCT, total_gb: 100 }] },
        },
      }),
    );
    const below = describeNodeSystem(
      makeNode({
        last_stats: {
          system: {
            disks: [{ path: "/scratch", used_gb: DISK_FILL_WARNING_PCT - 1, total_gb: 100 }],
          },
        },
      }),
    );

    expect(atThreshold).toMatchObject({ disk: { value: "85%", warning: true } });
    expect(below).toMatchObject({ disk: { value: "84%", warning: false } });
  });

  it("reports the fullest mount and keeps every mount in the tooltip", () => {
    const system = describeNodeSystem(
      makeNode({
        last_stats: {
          system: {
            disks: [
              { path: "/tmp/silo-transcode", used_gb: 10, total_gb: 100 },
              { path: "/media/movies", used_gb: 95, total_gb: 100, stale: true },
              { path: "/media/gone", unavailable: true },
            ],
          },
        },
      }),
    );

    expect(system).toMatchObject({
      disk: {
        value: "95%",
        detail: "/media/movies",
        warning: true,
        title: [
          "/tmp/silo-transcode — 10% full (10.0 GiB of 100.0 GiB)",
          "/media/movies — 95% full (95.0 GiB of 100.0 GiB), carried over from an earlier pass",
          "/media/gone — unavailable on this host",
        ].join("\n"),
      },
    });
  });
});

describe("describeGPUBusy", () => {
  it("reports the busiest video engine and the total pinned sessions", () => {
    expect(
      describeGPUBusy([
        { device: "/dev/dri/renderD128", video_busy_pct: 42, sessions: 2, source: "fdinfo" },
        { device: "cuda:0", video_busy_pct: 71, sessions: 1, source: "nvidia-smi" },
      ]),
    ).toMatchObject({
      label: "GPU",
      value: "71%",
      detail: "busiest of 2 GPUs · 3 sessions",
      muted: false,
      title: [
        "/dev/dri/renderD128 — video 42% · 2 sessions",
        "cuda:0 — video 71% · 1 session",
      ].join("\n"),
    });
  });

  it("mutes the tile when no device could be measured", () => {
    expect(
      describeGPUBusy([{ device: "/dev/dri/renderD128", sessions: 0, source: "unavailable" }]),
    ).toMatchObject({
      value: "—",
      muted: true,
      title: "/dev/dri/renderD128 — not measured · 0 sessions",
    });
  });
});

describe("describeResourceSample", () => {
  it("does not claim a sample when available is true but the body carries none", () => {
    expect(describeResourceSample({ available: true })).toMatchObject({ kind: "unavailable" });
  });

  it("derives the host readings and omits the GPU tile when there is no GPU", () => {
    const sample = describeResourceSample({
      available: true,
      sampled_at: "2026-08-26T12:00:00Z",
      system: FULL_SAMPLE,
    });

    expect(sample).toMatchObject({
      kind: "sampled",
      cpu: { value: "42%" },
      memory: { value: "12.5 GiB of 31.3 GiB" },
      disk: { value: "87%", warning: true },
      gpu: null,
      sampledAt: "2026-08-26T12:00:00Z",
    });
  });

  it("carries the GPU reading through when the host reports one", () => {
    const sample = describeResourceSample({
      available: true,
      system: FULL_SAMPLE,
      gpu: [{ device: "/dev/dri/renderD128", video_busy_pct: 30, sessions: 1, source: "fdinfo" }],
    });

    expect(sample).toMatchObject({
      kind: "sampled",
      gpu: { value: "30%", detail: "video engine · 1 session" },
      sampledAt: null,
    });
  });
});

describe("describeNodeAccelerationOverride", () => {
  it("renders nothing for a node that inherits the cluster-wide settings", () => {
    expect(describeNodeAccelerationOverride(makeNode())).toBeNull();
    expect(
      describeNodeAccelerationOverride(
        makeNode({ hw_accel_override: null, hw_device_override: null }),
      ),
    ).toBeNull();
    // Whitespace is not an override.
    expect(describeNodeAccelerationOverride(makeNode({ hw_device_override: " , " }))).toBeNull();
  });

  it("shows a single pinned device inline and counts several", () => {
    expect(
      describeNodeAccelerationOverride(
        makeNode({ hw_accel_override: "vaapi", hw_device_override: "/dev/dri/renderD129" }),
      )?.label,
    ).toBe("override: vaapi · /dev/dri/renderD129");

    const many = describeNodeAccelerationOverride(
      makeNode({ hw_device_override: "/dev/dri/renderD128, /dev/dri/renderD129" }),
    );
    expect(many?.label).toBe("override: 2 devices");
    expect(many?.title).toContain("GPU devices: /dev/dri/renderD128, /dev/dri/renderD129.");
    expect(many?.title).toContain("Acceleration: inherited");
  });
});

describe("describeCapabilityDrift", () => {
  it("renders nothing for a node whose last refetch found no regression", () => {
    expect(describeCapabilityDrift(makeNode())).toBeNull();
    expect(describeCapabilityDrift(makeNode({ capability_drift: null }))).toBeNull();
    expect(describeCapabilityDrift(makeNode({ capability_drift: "   " }))).toBeNull();
  });

  it("shows the server's note verbatim and explains how it clears", () => {
    const note = "verified hardware backends lost: qsv; resolved backend qsv -> none";
    const drift = describeCapabilityDrift(makeNode({ capability_drift: note }));

    expect(drift?.label).toBe("Drift");
    expect(drift?.title.split("\n")[0]).toBe(note);
    expect(drift?.title).toContain("got worse than the report it replaced");
    expect(drift?.title).toContain("re-probe the node");
  });
});

describe("buildNodeHWDeviceRows", () => {
  const inventoryNode = makeNode({
    capabilities: {
      resolved: "qsv",
      render_device_details: [
        { path: "/dev/dri/renderD128", description: "Intel GPU", pci_address: "0000:00:02.0" },
        { path: "/dev/dri/renderD129", description: "Intel GPU" },
      ],
    },
  });

  it("has nothing to pick from on a node that never reported an inventory", () => {
    expect(nodeHasHWDeviceInventory(makeNode())).toBe(false);
    expect(nodeHasHWDeviceInventory(makeNode({ capabilities: { resolved: "qsv" } }))).toBe(false);
    expect(nodeHasHWDeviceInventory(null)).toBe(false);
    expect(buildNodeHWDeviceRows(makeNode(), "")).toEqual([]);
  });

  it("builds one row per reported device, in report order, none selected by default", () => {
    expect(nodeHasHWDeviceInventory(inventoryNode)).toBe(true);
    expect(nodeHWDevicePaths(inventoryNode)).toEqual([
      "/dev/dri/renderD128",
      "/dev/dri/renderD129",
    ]);
    expect(buildNodeHWDeviceRows(inventoryNode, null)).toEqual([
      {
        path: "/dev/dri/renderD128",
        description: "Intel GPU",
        reported: true,
        selected: false,
        title: "/dev/dri/renderD128 — Intel GPU (0000:00:02.0)",
      },
      {
        path: "/dev/dri/renderD129",
        description: "Intel GPU",
        reported: true,
        selected: false,
        title: "/dev/dri/renderD129 — Intel GPU",
      },
    ]);
  });

  it("checks exactly the devices the override pins", () => {
    const rows = buildNodeHWDeviceRows(inventoryNode, " /dev/dri/renderD129 ");

    expect(rows.map((row) => row.selected)).toEqual([false, true]);
  });

  it("falls back to bare paths from a node that reports no device details", () => {
    const rows = buildNodeHWDeviceRows(
      makeNode({
        capabilities: { resolved: "vaapi", render_devices: ["/dev/dri/renderD128", "  "] },
      }),
      "/dev/dri/renderD128",
    );

    expect(rows).toEqual([
      {
        path: "/dev/dri/renderD128",
        description: "GPU",
        reported: true,
        selected: true,
        title: "/dev/dri/renderD128",
      },
    ]);
  });

  // A pinned device the node stopped reporting would otherwise be stranded:
  // checked in the stored value with no control to clear it.
  it("keeps a pinned device the node no longer reports, and marks it unreported", () => {
    const rows = buildNodeHWDeviceRows(inventoryNode, "/dev/dri/renderD129,/dev/dri/renderD200");

    expect(rows).toHaveLength(3);
    expect(rows[2]).toMatchObject({
      path: "/dev/dri/renderD200",
      reported: false,
      selected: true,
      description: "Pinned device this node does not report",
    });
    expect(rows[2]?.title).toContain("not in this node's last capability report");
  });
});

describe("describeReprobeOutcome", () => {
  function result(overrides: Partial<ReprobeNodeResult> = {}): ReprobeNodeResult {
    return {
      node_id: 1,
      node_name: "transcode-1",
      status: "ok",
      capabilities_refreshed: true,
      ...overrides,
    };
  }

  it("surfaces the node's own reason when the re-probe failed", () => {
    expect(
      describeReprobeOutcome(
        makeNode(),
        result({ status: "error", error: "node could not complete its hardware probe" }),
      ),
    ).toEqual({
      ok: false,
      message: "transcode-1: re-probe failed — node could not complete its hardware probe",
    });
  });

  it("reports an unchanged hash as plainly as a change", () => {
    const node = makeNode({ capabilities_hash: "sha256:aaa" });

    expect(
      describeReprobeOutcome(node, result({ capability_hash: "sha256:aaa", resolved: "qsv" })),
    ).toEqual({ ok: true, message: "transcode-1: re-probed, no change (QSV)" });
  });

  it("calls out a changed report and the backend it resolved to", () => {
    const node = makeNode({ capabilities_hash: "sha256:aaa" });

    expect(
      describeReprobeOutcome(node, result({ capability_hash: "sha256:bbb", resolved: "vaapi" })),
    ).toEqual({ ok: true, message: "transcode-1: re-probed, hardware report changed — now VAAPI" });
  });

  // Without a hash on both sides there is nothing to compare, and claiming
  // either answer would be a guess.
  it("leaves the comparison unstated when either hash is missing", () => {
    expect(describeReprobeOutcome(makeNode(), result({ resolved: "qsv" })).message).toBe(
      "transcode-1: re-probed — QSV",
    );
    expect(
      describeReprobeOutcome(makeNode({ capabilities_hash: "sha256:aaa" }), result()).message,
    ).toBe("transcode-1: re-probed");
  });

  it("says when the stored row has not caught up yet", () => {
    expect(
      describeReprobeOutcome(
        makeNode({ capabilities_hash: "sha256:aaa" }),
        result({ capability_hash: "sha256:aaa", capabilities_refreshed: false }),
      ),
    ).toEqual({
      ok: true,
      message:
        "transcode-1: re-probed, no change. The stored report will catch up on the next health check",
    });
  });
});

describe("nodeUsesCUDADevices", () => {
  it("follows what the node resolves to when the cluster names no backend", () => {
    const node = makeNode({ capabilities: { resolved: "nvenc" } });
    expect(nodeUsesCUDADevices(node, HW_ACCEL_INHERIT)).toBe(true);
    expect(nodeUsesCUDADevices(node, "auto")).toBe(true);
    expect(nodeUsesCUDADevices(node, HW_ACCEL_INHERIT, "auto")).toBe(true);
  });

  // Inheriting means running what the cluster names. A node overriding QSV
  // under an NVENC cluster still resolves qsv today, and following that would
  // keep the render-path picker while inheritance is selected — leaving no way
  // to type the CUDA identity and letting /dev/dri/… be saved as an NVENC
  // policy that cannot work.
  it("follows the cluster backend when inheritance is selected", () => {
    const node = makeNode({
      capabilities: { resolved: "qsv" },
      hw_accel_override: "qsv",
      hw_device_override: "/dev/dri/renderD128",
    });
    expect(nodeUsesCUDADevices(node, HW_ACCEL_INHERIT, "nvenc")).toBe(true);
  });

  it("uses render paths when the cluster names a render-device backend", () => {
    const node = makeNode({ capabilities: { resolved: "nvenc" } });
    expect(nodeUsesCUDADevices(node, HW_ACCEL_INHERIT, "qsv")).toBe(false);
    expect(nodeUsesCUDADevices(node, HW_ACCEL_INHERIT, "vaapi")).toBe(false);
  });

  // Auto is not inheritance: it tells this node to detect against its own
  // hardware, so what the cluster names says nothing about what it will run.
  // Reading the cluster there would switch a node whose Auto resolves QSV to
  // CUDA syntax and throw away a valid render-path override.
  it("keeps an explicit auto on the node's own resolution", () => {
    const qsv = makeNode({ capabilities: { resolved: "qsv" } });
    expect(nodeUsesCUDADevices(qsv, "auto", "nvenc")).toBe(false);
    const nvenc = makeNode({ capabilities: { resolved: "nvenc" } });
    expect(nodeUsesCUDADevices(nvenc, "auto", "qsv")).toBe(true);
  });

  // The node's own selection still wins: it is what that node will run.
  it("keeps an explicit override ahead of the cluster backend", () => {
    const node = makeNode({ capabilities: { resolved: "qsv" } });
    expect(nodeUsesCUDADevices(node, "nvenc", "qsv")).toBe(true);
    expect(nodeUsesCUDADevices(node, "qsv", "nvenc")).toBe(false);
  });

  it("uses render paths for a node that resolves to one", () => {
    const node = makeNode({ capabilities: { resolved: "qsv" } });
    expect(nodeUsesCUDADevices(node, HW_ACCEL_INHERIT)).toBe(false);
    expect(nodeUsesCUDADevices(null, HW_ACCEL_INHERIT)).toBe(false);
  });
});

describe("hwDeviceSyntaxChanges", () => {
  // Under an NVENC cluster, giving up a QSV override means the device value has
  // to be a CUDA identity — the render path it holds cannot become one.
  it("reports a crossing when inheritance flips the syntax", () => {
    const node = makeNode({
      capabilities: { resolved: "qsv" },
      hw_accel_override: "qsv",
      hw_device_override: "/dev/dri/renderD128",
    });
    expect(hwDeviceSyntaxChanges(node, "qsv", HW_ACCEL_INHERIT, "nvenc")).toBe(true);
    expect(hwDeviceSyntaxChanges(node, HW_ACCEL_INHERIT, "qsv", "nvenc")).toBe(true);
  });

  // Both name render paths, so the value the operator typed still means what it
  // meant. Dropping it here would be losing work for nothing.
  it("reports no crossing between two render-device backends", () => {
    const node = makeNode({ capabilities: { resolved: "qsv" } });
    expect(hwDeviceSyntaxChanges(node, "qsv", "vaapi", "qsv")).toBe(false);
  });

  // The whole reason this takes both selections: the cluster setting is absent
  // on the first render and arrives with the query. Nothing the operator did
  // changed, so nothing may be erased.
  it("reports no crossing when only the cluster setting arrives", () => {
    const node = makeNode({ capabilities: { resolved: "qsv" } });
    expect(hwDeviceSyntaxChanges(node, HW_ACCEL_INHERIT, HW_ACCEL_INHERIT, undefined)).toBe(false);
    expect(hwDeviceSyntaxChanges(node, HW_ACCEL_INHERIT, HW_ACCEL_INHERIT, "nvenc")).toBe(false);
  });
});

describe("capability report staleness from an unconfirmed hash", () => {
  const base = {
    id: 1,
    name: "gpu-1",
    type: "transcode",
    url: "http://gpu-1",
    enabled: true,
    healthy: true,
    created_at: "2026-08-28T00:00:00Z",
    last_health_check: "2026-08-28T00:00:00Z",
    capabilities_refreshed_at: "2026-08-28T00:00:00Z",
    capabilities: { resolved: "qsv" },
  } as unknown as StreamNode;

  // A failing refetch leaves the two hashes apart while the health check goes on
  // succeeding every 30 seconds, so the timestamp says fresh about an inventory
  // the node has already contradicted.
  it("marks a report stale when the node advertises a different hash", () => {
    const node = {
      ...base,
      capabilities_hash: "sha256:stored",
      advertised_capabilities_hash: "sha256:newer",
    } as StreamNode;
    const gpu = describeNodeGPU(node, Date.parse("2026-08-28T00:00:10Z"));
    expect(gpu.kind === "reported" && gpu.stale).toBe("contradicted");
  });

  // A node downgraded to a build that predates capability reports answers health
  // checks with no hash at all. It is not confirming the stored inventory any
  // more than a mismatching node is, and the timestamp rule alone would present
  // that inventory as current for as long as the node keeps answering.
  it("marks a report stale when a checked node advertises no hash", () => {
    const node = {
      ...base,
      capabilities_hash: "sha256:stored",
      advertised_capabilities_hash: "",
    } as StreamNode;
    const gpu = describeNodeGPU(node, Date.parse("2026-08-28T00:00:10Z"));
    expect(gpu.kind === "reported" && gpu.stale).toBe("unreported");
  });

  it("leaves a matching hash alone on a freshly checked node", () => {
    const node = {
      ...base,
      capabilities_hash: "sha256:stored",
      advertised_capabilities_hash: "sha256:stored",
    } as StreamNode;
    const gpu = describeNodeGPU(node, Date.parse("2026-08-28T00:00:10Z"));
    expect(gpu.kind === "reported" && gpu.stale).toBe(null);
  });

  // A node that never advertises one — an older build — keeps the timestamp rule.
  it("falls back to the health-check age when no hash is advertised", () => {
    const node = { ...base, capabilities_hash: "sha256:stored" } as StreamNode;
    const fresh = describeNodeGPU(node, Date.parse("2026-08-28T00:00:10Z"));
    expect(fresh.kind === "reported" && fresh.stale).toBe(null);
    const aged = describeNodeGPU(node, Date.parse("2026-08-28T00:20:00Z"));
    expect(aged.kind === "reported" && aged.stale).toBe("unconfirmed");
  });
});

// Everything the redesigned page draws a bar from. A bar is a stronger claim
// than a number: it says "this much of what is available", so every reading
// that has no denominator — or that nothing measured — has to arrive with no
// fill at all rather than a fill of zero, which on screen is an idle node.
describe("load meter fills", () => {
  it("gives an unmeasured reading no fill rather than a fill of zero", () => {
    const system = describeNodeSystem(
      makeNode({ last_stats: { system: { cpu_pct: 0, mem_total_mb: 0, disks: [] } } }),
    );

    expect(system).toMatchObject({
      kind: "reported",
      // A real zero still fills — it was measured, and the node is idle.
      cpu: { value: "0%", detail: "", muted: false, fill: 0 },
      memory: {
        value: "—",
        muted: true,
        fill: null,
        title: "This sample carries no memory reading.",
      },
      disk: {
        value: "—",
        muted: true,
        fill: null,
        title: "This sample carries no disk reading.",
      },
      network: {
        value: "—",
        muted: true,
        fill: null,
        title: "This sample carries no network reading.",
      },
    });
  });

  it("gives a GPU nothing could measure no engine fill", () => {
    const capabilities = {
      resolved: "vaapi",
      render_devices: ["/dev/dri/renderD128", "/dev/dri/renderD129"],
    };
    const presentation = describeNodeGPU(
      makeNode({
        capabilities,
        last_stats: {
          gpu: [
            { device: "/dev/dri/renderD128", video_busy_pct: 0, source: "fdinfo" },
            { device: "/dev/dri/renderD129", video_busy_pct: 0, source: "unavailable" },
          ],
        },
      }),
      NOW,
    );

    expect(presentation.kind === "reported" && presentation.live.map((d) => d.busyFill)).toEqual([
      0,
      null,
    ]);
  });
});

describe("describeNodeJobs", () => {
  it("reports a capped node against its cap", () => {
    expect(describeNodeJobs(makeNode({ active_jobs: 3, max_jobs: 4 }))).toMatchObject({
      label: "Transcodes",
      value: "3 / 4",
      warning: false,
      fill: 75,
    });
  });

  it("warns once a capped node is full", () => {
    expect(describeNodeJobs(makeNode({ active_jobs: 4, max_jobs: 4 }))).toMatchObject({
      value: "4 / 4",
      warning: true,
      fill: 100,
    });
  });

  // Both spellings of "unlimited" the API uses, and the reason neither draws a
  // bar: there is no ceiling, so any bar would be measured against a number
  // this page made up.
  it("draws no meter for an uncapped node", () => {
    expect(describeNodeJobs(makeNode({ active_jobs: 7, max_jobs: null }))).toMatchObject({
      value: "7",
      detail: "no cap",
      warning: false,
      fill: null,
    });
    expect(describeNodeJobs(makeNode({ active_jobs: 7, max_jobs: 0 })).fill).toBe(null);
  });
});

describe("describeNodeEgress", () => {
  it("reports measured egress against a cap", () => {
    expect(
      describeNodeEgress(
        makeNode({ type: "proxy", egress_kbps: 250_000, max_bandwidth_kbps: 500_000 }),
      ),
    ).toMatchObject({ value: "250 / 500 Mbps", warning: false, fill: 50 });
  });

  it("warns once measured egress reaches the cap", () => {
    expect(
      describeNodeEgress(
        makeNode({ type: "proxy", egress_kbps: 500_000, max_bandwidth_kbps: 500_000 }),
      ),
    ).toMatchObject({ warning: true, fill: 100 });
  });

  it("draws no meter for an uncapped node", () => {
    expect(
      describeNodeEgress(
        makeNode({ type: "proxy", egress_kbps: 12_340, max_bandwidth_kbps: null }),
      ),
    ).toMatchObject({ value: "12.3 Mbps", detail: "no cap", fill: null });
  });
});

// A group is a co-location contract the planner enforces, not a label: a
// grouped transcode node takes work only while its whole group is healthy, and
// a group holding proxies of its own never falls back to another group's. So
// the filter's buckets are also failure domains, and these cover the parts an
// operator would act on.
describe("describeNodeGroups", () => {
  function grouped(id: number, group: string | null, overrides: Partial<StreamNode> = {}) {
    return makeNode({ id, name: `node-${id}`, group, ...overrides });
  }

  it("buckets by group, alphabetically, with the ungrouped leftover last", () => {
    const groups = describeNodeGroups([
      grouped(1, "rack-2"),
      grouped(2, null),
      grouped(3, "rack-1"),
      grouped(4, "rack-2"),
    ]);

    expect(groups.map((g) => [g.label, g.count])).toEqual([
      ["rack-1", 1],
      ["rack-2", 2],
      ["Ungrouped", 1],
    ]);
  });

  // The API trims a group before storing it, so two spellings that differ only
  // in whitespace are one group there and have to be one bucket here.
  it("treats whitespace-only differences as the same group", () => {
    const groups = describeNodeGroups([grouped(1, "rack-1"), grouped(2, "  rack-1  ")]);
    expect(groups).toHaveLength(1);
    expect(groups[0]?.count).toBe(2);
  });

  // Blank and null both mean ungrouped to the API; neither is a group label.
  it("folds a blank group into the ungrouped bucket", () => {
    const groups = describeNodeGroups([grouped(1, "   "), grouped(2, null)]);
    expect(groups.map((g) => [g.value, g.count])).toEqual([["", 2]]);
  });

  it("counts each type, because a group's capacity is bounded by its proxies", () => {
    const [group] = describeNodeGroups([
      grouped(1, "rack-1", { type: "proxy" }),
      grouped(2, "rack-1", { type: "transcode" }),
      grouped(3, "rack-1", { type: "transcode" }),
    ]);

    expect(group).toMatchObject({ proxies: 1, transcodes: 2, count: 3 });
    expect(group?.title).toContain("1 proxy node, 2 transcode nodes.");
    expect(group?.title).toContain("stays on one LAN");
  });

  it("marks a group whose enabled member is unhealthy as out of service", () => {
    const [group] = describeNodeGroups([
      grouped(1, "rack-1", { type: "proxy" }),
      grouped(2, "rack-1", { type: "transcode", healthy: false }),
    ]);

    expect(group?.degraded).toBe(true);
    expect(group?.title).toContain("Out of service");
  });

  // nodepool's groupHealth runs over the pools, which hold only enabled nodes:
  // switching a node off takes it out of the group rather than holding the
  // whole group down.
  it("does not hold a group down for a member that is switched off", () => {
    const [group] = describeNodeGroups([
      grouped(1, "rack-1", { type: "proxy" }),
      grouped(2, "rack-1", { type: "transcode", enabled: false, healthy: false }),
    ]);

    expect(group?.degraded).toBe(false);
  });

  // The ungrouped bucket is a leftover, not a group: its nodes are selected
  // individually, so there is no pairing for an unhealthy one to take down.
  it("never marks the ungrouped bucket degraded", () => {
    const [group] = describeNodeGroups([grouped(1, null, { healthy: false })]);
    expect(group).toMatchObject({ value: "", degraded: false });
    expect(group?.title).toContain("no group");
  });
});

describe("filterNodesByGroup", () => {
  const nodes = [
    makeNode({ id: 1, group: "rack-1" }),
    makeNode({ id: 2, group: "rack-2" }),
    makeNode({ id: 3, group: null }),
  ];

  it("narrows to one group", () => {
    expect(filterNodesByGroup(nodes, "rack-1").map((n) => n.id)).toEqual([1]);
  });

  // "" is the ungrouped bucket, which is a different selection from null — no
  // filter at all. Collapsing the two would make "Ungrouped" show everything.
  it("treats the empty group as the ungrouped bucket, not as no filter", () => {
    expect(filterNodesByGroup(nodes, "").map((n) => n.id)).toEqual([3]);
  });
});

describe("resource attribution", () => {
  it("keeps process RSS separate from Go heap and labels the cgroup scope", () => {
    const sample = describeResourceSample(
      {
        available: true,
        sampled_at: new Date(NOW).toISOString(),
        system: FULL_SAMPLE,
        attribution: {
          instance_id: "instance-a",
          sample_interval_seconds: 5,
          cpu: { available: true, scope: "cgroup" },
          memory: { available: true, scope: "cgroup" },
          process: { resident_bytes: 512 * 1024 ** 2, heap_live_bytes: 128 * 1024 ** 2 },
        },
      },
      NOW,
    );
    expect(sample).toMatchObject({
      kind: "sampled",
      stale: false,
      instanceID: "instance-a",
      cpu: { label: "Cgroup CPU" },
      memory: { label: "Cgroup RAM" },
      processMemory: { value: "512.0 MiB", fill: null },
      heapMemory: { value: "128.0 MiB", fill: null },
    });
  });

  it("shows unavailable readings as dashes and marks stalled samples stale", () => {
    const sample = describeResourceSample(
      {
        available: true,
        sampled_at: new Date(NOW - 20_000).toISOString(),
        system: FULL_SAMPLE,
        attribution: {
          sample_interval_seconds: 5,
          cpu: { available: false },
          memory: { available: false },
          network: { available: false },
          process: {},
        },
      },
      NOW,
    );
    expect(sample).toMatchObject({
      kind: "sampled",
      stale: true,
      cpu: { muted: true },
      memory: { muted: true },
      network: { muted: true },
      processMemory: { muted: true },
      heapMemory: { muted: true },
    });
  });

  it("detects a stopped worker sampler without treating normal health polling as stale", () => {
    const node = makeNode({
      healthy: true,
      last_health_check: new Date(NOW).toISOString(),
      last_stats: {
        system: FULL_SAMPLE,
        sampled_at: new Date(NOW - 5000).toISOString(),
        attribution: { sample_interval_seconds: 5 },
      },
    });
    expect(describeNodeSystem(node, NOW + 30_000).kind).toBe("reported");
    node.last_stats!.sampled_at = new Date(NOW - 20_000).toISOString();
    expect(describeNodeSystem(node, NOW)).toMatchObject({ kind: "unreported", label: "Stale" });
  });
});
