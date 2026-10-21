import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminServerStatus, StreamNode } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useAdminServerStatus: vi.fn(),
  useBuildInfo: vi.fn(),
  useAdminNodes: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerStatus: mocks.useAdminServerStatus,
}));
vi.mock("@/hooks/queries/admin/system", () => ({
  useBuildInfo: mocks.useBuildInfo,
}));
vi.mock("@/hooks/queries/admin/nodes", () => ({
  useAdminNodes: mocks.useAdminNodes,
}));

import { HealthStripWidget } from "./HealthStripWidget";

function status(overrides: Partial<AdminServerStatus> = {}): AdminServerStatus {
  return {
    started_at: new Date(Date.now() - 3 * 3_600_000).toISOString(),
    restart_required: false,
    restart_requested: false,
    health: {
      postgres: { configured: true, ok: true, latency_ms: 1.42 },
      redis: { configured: true, ok: true, latency_ms: 0.31 },
      errors_24h: 4,
      warnings_24h: 12,
    },
    ...overrides,
  };
}

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

function loadedNodes(data: StreamNode[]) {
  return { data, isLoading: false, isSuccess: true, isError: false, error: null };
}

function renderStrip() {
  return render(
    <MemoryRouter>
      <HealthStripWidget />
    </MemoryRouter>,
  );
}

describe("HealthStripWidget", () => {
  beforeEach(() => {
    mocks.useAdminServerStatus.mockReset();
    mocks.useBuildInfo.mockReset();
    mocks.useAdminNodes.mockReset();
    mocks.useBuildInfo.mockReturnValue({ data: { display: "v0.9.1", build_number: 411 } });
    mocks.useAdminNodes.mockReturnValue(loadedNodes([]));
  });

  it("separates an unconfigured dependency from a broken one", () => {
    mocks.useAdminServerStatus.mockReturnValue({
      data: status({
        health: {
          postgres: { configured: true, ok: false },
          redis: { configured: false },
          errors_24h: 0,
          warnings_24h: 0,
        },
      }),
      isLoading: false,
      error: null,
    });

    renderStrip();

    expect(screen.getByText("Unreachable")).toBeTruthy();
    expect(screen.getByText("Not configured")).toBeTruthy();
  });

  it.each([
    // A node an operator disabled is not expected to report healthy, so it is
    // neither half of the ratio: one disabled node reads as no nodes at all.
    {
      name: "leaves disabled nodes out of the ratio",
      nodes: loadedNodes([node({ enabled: false, healthy: false })]),
      shown: ["none", "this server transcodes"],
      hidden: ["0/1"],
    },
    {
      name: "counts only enabled nodes in the denominator",
      nodes: loadedNodes([node(), node({ id: 2, name: "node-2", enabled: false, healthy: false })]),
      shown: ["1/1", "healthy"],
      hidden: [],
    },
    // "none" is a claim about the deployment; an unresolved query cannot back
    // it. Loading shows a placeholder, a failure says the list is unavailable.
    {
      name: "does not claim no nodes while the node list is loading",
      nodes: { data: undefined, isLoading: true, isSuccess: false, isError: false, error: null },
      shown: [],
      hidden: ["none", "this server transcodes"],
    },
    {
      name: "marks the node ratio unavailable when the node query fails",
      nodes: {
        data: undefined,
        isLoading: false,
        isSuccess: false,
        isError: true,
        error: new Error("boom"),
      },
      shown: ["unavailable"],
      hidden: ["none"],
    },
  ])("$name", ({ nodes, shown, hidden }) => {
    mocks.useAdminServerStatus.mockReturnValue({ data: status(), isLoading: false, error: null });
    mocks.useAdminNodes.mockReturnValue(nodes);

    renderStrip();

    for (const text of shown) expect(screen.getByText(text)).toBeTruthy();
    for (const text of hidden) expect(screen.queryByText(text)).toBeNull();
  });
});
