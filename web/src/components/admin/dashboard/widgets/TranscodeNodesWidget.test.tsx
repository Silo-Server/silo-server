// @vitest-environment jsdom

import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { StreamNode } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useAdminNodes: vi.fn(),
  useBuildInfo: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/nodes", () => ({
  useAdminNodes: mocks.useAdminNodes,
}));

vi.mock("@/hooks/queries/admin/system", () => ({
  useBuildInfo: mocks.useBuildInfo,
}));

import { TranscodeNodesWidget } from "./TranscodeNodesWidget";

function node(overrides: Partial<StreamNode> = {}): StreamNode {
  return {
    id: 1,
    name: "node-1",
    type: "transcode",
    url: "http://node-1:8091",
    enabled: true,
    healthy: true,
    active_jobs: 0,
    group: null,
    max_jobs: 4,
    max_bandwidth_kbps: null,
    egress_kbps: 0,
    last_health_check: new Date().toISOString(),
    created_at: new Date().toISOString(),
    ...overrides,
  };
}

function renderFleet(data: StreamNode[]) {
  mocks.useAdminNodes.mockReturnValue({ data, isLoading: false, error: null });
  render(
    <MemoryRouter>
      <TranscodeNodesWidget />
    </MemoryRouter>,
  );
}

const withDisk = (used_gb: number) => ({
  sampled_at: new Date().toISOString(),
  system: { cpu_pct: 34, disks: [{ role: "scratch", used_gb, total_gb: 100 }] },
});

describe("TranscodeNodesWidget", () => {
  beforeEach(() => {
    mocks.useAdminNodes.mockReset();
    mocks.useBuildInfo.mockReset();
    mocks.useBuildInfo.mockReturnValue({
      data: { display: "aaaaaaaa", revision: "aaaaaaaa1111", available: true },
    });
  });

  it("lists proxies first and sorts problem nodes to the top of each type", () => {
    renderFleet([
      node({ id: 1, name: "t-clean" }),
      node({ id: 2, name: "t-full", active_jobs: 4, max_jobs: 4 }),
      node({
        id: 3,
        name: "t-behind",
        last_stats: { build: { display: "b", revision: "bbbb", available: true } },
      }),
      node({ id: 4, name: "t-draining", drain_requested: true }),
      node({ id: 5, name: "t-down", healthy: false }),
      node({ id: 6, name: "t-off", enabled: false }),
      node({ id: 7, name: "t-storage", last_stats: withDisk(95) }),
      node({ id: 8, name: "p-zulu", type: "proxy" }),
      node({ id: 9, name: "p-alpha", type: "proxy" }),
    ]);

    const names = screen
      .getAllByRole("rowheader")
      .map((cell) => cell.textContent?.match(/[tp]-[a-z]+/)?.[0]);
    expect(names).toEqual([
      "p-alpha",
      "p-zulu",
      "t-down",
      "t-draining",
      "t-behind",
      "t-full",
      "t-storage",
      "t-clean",
      "t-off",
    ]);
  });

  describe("groups", () => {
    /** Every row's first cell after the column headings, top to bottom. */
    function rowLabels() {
      return screen
        .getAllByRole("row")
        .slice(1)
        .map((row) => row.querySelector("th")?.textContent);
    }

    it("puts problem groups first and ungrouped nodes after equally healthy groups", () => {
      renderFleet([
        node({ id: 1, name: "a-trans", group: "alpha" }),
        node({ id: 2, name: "a-proxy", type: "proxy", group: "alpha" }),
        node({ id: 3, name: "b-trans", group: "bravo", healthy: false }),
        node({ id: 4, name: "b-proxy", type: "proxy", group: "bravo" }),
        node({ id: 5, name: "u-proxy", type: "proxy" }),
        node({ id: 6, name: "c-trans", group: "charlie", enabled: false }),
      ]);

      expect(rowLabels()).toEqual([
        "bravo",
        "b-proxy",
        "b-trans",
        "alpha",
        "a-proxy",
        "a-trans",
        "Ungrouped",
        "u-proxy",
        "charlie",
        "c-trans",
      ]);
    });

    it("gives a group its worst node's status", () => {
      renderFleet([
        node({ id: 1, name: "ok", group: "alpha" }),
        node({ id: 2, name: "down", group: "alpha", healthy: false }),
        node({ id: 3, name: "off", group: "bravo", enabled: false }),
        node({ id: 4, name: "up", group: "bravo" }),
      ]);

      const status = (label: string) =>
        within(screen.getByRole("row", { name: new RegExp(label) })).getAllByRole("img")[0];
      expect(status("alpha")?.getAttribute("aria-label")).toBe("Unhealthy");
      // A node switched off on purpose does not mark its group as a problem.
      expect(status("bravo")?.getAttribute("aria-label")).toBe("Healthy");
    });

    it.each([
      {
        scenario: "drops the shared prefix, keeping enough words to read",
        groups: ["kronos-silo-3-worker-set-1", "kronos-silo-3-worker-set-2"],
        labels: ["worker-set-1", "worker-set-2"],
      },
      {
        scenario: "only cuts at a word boundary",
        groups: ["worker-10", "worker-11"],
        labels: ["worker-10", "worker-11"],
      },
      {
        scenario: "keeps names that share nothing",
        groups: ["eu-west", "us-east"],
        labels: ["eu-west", "us-east"],
      },
      {
        scenario: "keeps the full name of a lone group",
        groups: ["kronos-silo-3-worker-set-1"],
        labels: ["kronos-silo-3-worker-set-1"],
      },
    ])("$scenario", ({ groups, labels }) => {
      renderFleet(groups.map((group, i) => node({ id: i + 1, name: `node ${i}`, group })));

      expect(rowLabels().filter((label) => !label?.startsWith("node"))).toEqual(labels);
      // The name that was cut stays available.
      expect(screen.getByTitle(groups[0]!)).toBeTruthy();
    });

    it("shows no group headings when no node has a group", () => {
      renderFleet([
        node({ id: 1, name: "solo", group: null }),
        node({ id: 2, name: "pair", group: "" }),
      ]);

      expect(rowLabels()).toEqual(["pair", "solo"]);
      expect(screen.queryByText("Ungrouped")).toBeNull();
    });
  });

  it.each([
    {
      scenario: "totals streams and transcodes separately",
      fleet: [
        node({ id: 1, type: "proxy", active_jobs: 16, egress_kbps: 264_000 }),
        node({ id: 2, type: "transcode", active_jobs: 1 }),
      ],
      header: "2/2 healthy · 16 streams · 1 transcode · 264 Mbps",
    },
    {
      scenario: "leaves out a node type the fleet does not have",
      fleet: [node({ type: "transcode", active_jobs: 2 })],
      header: "1/1 healthy · 2 transcodes · 0 Mbps",
    },
    {
      scenario: "counts named groups only",
      fleet: [
        node({ id: 1, type: "proxy", group: "alpha" }),
        node({ id: 2, type: "proxy", group: "bravo" }),
        node({ id: 3, type: "proxy", group: null }),
      ],
      header: "2 groups · 3/3 healthy · 0 streams · 0 Mbps",
    },
  ])("header $scenario", ({ fleet, header }) => {
    renderFleet(fleet);

    expect(screen.getByText(header)).toBeTruthy();
  });

  describe("load readings", () => {
    const sample = () => ({
      sampled_at: new Date().toISOString(),
      system: { cpu_pct: 34, mem_used_mb: 520, mem_total_mb: 1000 },
      gpu: [{ device: "/dev/dri/renderD128", source: "intel_gpu_top", video_busy_pct: 22 }],
    });

    function cellsOf(name: string) {
      const row = screen.getByRole("row", { name: new RegExp(name) });
      return within(row)
        .getAllByRole("cell")
        .map((cell) => cell.textContent);
    }

    it("shows CPU, RAM, and the busiest GPU engine for a healthy node", () => {
      renderFleet([node({ name: "live", last_stats: sample() })]);

      expect(cellsOf("live")).toEqual(expect.arrayContaining(["34%", "52%", "22%"]));
      expect(screen.getByRole("meter", { name: "live CPU" }).getAttribute("aria-valuenow")).toBe(
        "34",
      );
    });

    it("shows dashes rather than an old sample for a node that stopped answering", () => {
      renderFleet([node({ name: "down", healthy: false, last_stats: sample() })]);

      const cells = cellsOf("down");
      // A failed check reports zeros, which would read as an idle node.
      expect(cells.filter((text) => text === "–")).toHaveLength(5);
      expect(cells.join()).not.toMatch(/%|\d/);
    });

    it("gives a proxy no GPU reading, since it never transcodes on hardware", () => {
      renderFleet([node({ name: "relay", type: "proxy", last_stats: sample() })]);

      expect(cellsOf("relay")).not.toContain("22%");
      expect(screen.queryByRole("columnheader", { name: "GPU" })).toBeNull();
    });

    it.each([
      { scenario: "above 90% full", used: 94, overrides: {}, shown: "Storage 94%" },
      { scenario: "exactly 90% full", used: 90, overrides: {}, shown: null },
      { scenario: "a proxy", used: 94, overrides: { type: "proxy" }, shown: null },
      { scenario: "an old sample", used: 94, overrides: { healthy: false }, shown: null },
    ])("handles the transcode directory warning for $scenario", ({ used, overrides, shown }) => {
      renderFleet([node({ last_stats: withDisk(used), ...overrides })]);

      if (shown) {
        expect(screen.getByText(shown)).toBeTruthy();
      } else {
        expect(screen.queryByText(/^Storage/)).toBeNull();
      }
    });
  });

  it("shows each node's build and only flags a comparable one that differs from the server", () => {
    renderFleet([
      node({
        id: 1,
        name: "current",
        last_stats: {
          build: {
            display: "aaaaaaaa",
            revision: "aaaaaaaa1111",
            build_number: 7,
            available: true,
          },
        },
      }),
      node({
        id: 2,
        name: "behind",
        last_stats: {
          build: {
            display: "bbbbbbbb",
            revision: "bbbbbbbb2222",
            build_number: 6,
            available: true,
          },
        },
      }),
      node({ id: 3, name: "older-node" }),
      node({
        id: 4,
        name: "no-revision",
        last_stats: { build: { display: "unavailable", revision: "", available: false } },
      }),
    ]);

    expect(screen.getByText("7 · aaaaaaaa")).toBeTruthy();
    expect(screen.getByText("6 · bbbbbbbb")).toBeTruthy();
    expect(screen.getByText("unavailable")).toBeTruthy();
    const flagged = screen.getAllByText("Build differs");
    expect(flagged).toHaveLength(1);
    expect(flagged[0]!.closest("tr")?.textContent).toContain("behind");
    // A node predating build reporting draws nothing rather than "unknown".
    const older = screen.getByRole("row", { name: /older-node/ });
    expect(within(older).getAllByRole("cell").at(-1)?.textContent).toBe("");
  });
});
