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

import { formatLatency, formatUptime } from "../format";
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

function renderStrip() {
  return render(
    <MemoryRouter>
      <HealthStripWidget />
    </MemoryRouter>,
  );
}

describe("formatUptime", () => {
  const now = Date.parse("2026-08-26T12:00:00Z");

  it("steps down to the coarsest useful unit", () => {
    expect(formatUptime("2026-08-26T11:59:30Z", now)).toBe("30s");
    expect(formatUptime("2026-08-26T11:18:00Z", now)).toBe("42m");
    expect(formatUptime("2026-08-26T06:48:00Z", now)).toBe("5h 12m");
    expect(formatUptime("2026-08-26T07:00:00Z", now)).toBe("5h");
    expect(formatUptime("2026-08-23T08:00:00Z", now)).toBe("3d 4h");
    expect(formatUptime("2026-08-23T12:00:00Z", now)).toBe("3d");
  });

  it("reports an em dash rather than a negative or bogus uptime", () => {
    expect(formatUptime(undefined, now)).toBe("—");
    expect(formatUptime("not-a-date", now)).toBe("—");
    expect(formatUptime("2026-08-26T12:00:30Z", now)).toBe("0s");
  });
});

describe("formatLatency", () => {
  it("keeps decimals only where they carry information", () => {
    expect(formatLatency(1.42)).toBe("1.42 ms");
    expect(formatLatency(0.31)).toBe("0.31 ms");
    expect(formatLatency(148.6)).toBe("149 ms");
  });
});

describe("HealthStripWidget", () => {
  beforeEach(() => {
    mocks.useAdminServerStatus.mockReset();
    mocks.useBuildInfo.mockReset();
    mocks.useAdminNodes.mockReset();
    mocks.useBuildInfo.mockReturnValue({ data: { display: "v0.9.1", build_number: 411 } });
    mocks.useAdminNodes.mockReturnValue({
      data: [],
      isLoading: false,
      isSuccess: true,
      isError: false,
      error: null,
    });
  });

  it("composes version, uptime, dependencies and error count", () => {
    mocks.useAdminServerStatus.mockReturnValue({
      data: status(),
      isLoading: false,
      error: null,
    });
    mocks.useAdminNodes.mockReturnValue({
      data: [node(), node({ id: 2, name: "node-2", healthy: false })],
      isLoading: false,
      isSuccess: true,
      isError: false,
      error: null,
    });

    renderStrip();

    expect(screen.getByText("411 · v0.9.1")).toBeTruthy();
    expect(screen.getByText("3h")).toBeTruthy();
    expect(screen.getByText("1.42 ms")).toBeTruthy();
    expect(screen.getByText("0.31 ms")).toBeTruthy();
    expect(screen.getByText("1/2")).toBeTruthy();
    expect(screen.getByText("4")).toBeTruthy();
    expect(screen.getByText("12 warnings")).toBeTruthy();
  });

  it("falls back to the version when no ordered build number is available", () => {
    mocks.useAdminServerStatus.mockReturnValue({
      data: status(),
      isLoading: false,
      error: null,
    });
    mocks.useBuildInfo.mockReturnValue({ data: { display: "v0.9.1" } });

    renderStrip();

    expect(screen.getByText("v0.9.1")).toBeTruthy();
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

  // A node an operator disabled is not expected to report healthy, so it is
  // neither half of the ratio: one disabled node reads as no nodes at all.
  it("leaves disabled nodes out of the ratio", () => {
    mocks.useAdminServerStatus.mockReturnValue({
      data: status(),
      isLoading: false,
      error: null,
    });
    mocks.useAdminNodes.mockReturnValue({
      data: [node({ enabled: false, healthy: false })],
      isLoading: false,
      isSuccess: true,
      isError: false,
      error: null,
    });

    renderStrip();

    expect(screen.getByText("none")).toBeTruthy();
    expect(screen.getByText("this server transcodes")).toBeTruthy();
    expect(screen.queryByText("0/1")).toBeNull();
  });

  it("counts only enabled nodes in the denominator", () => {
    mocks.useAdminServerStatus.mockReturnValue({
      data: status(),
      isLoading: false,
      error: null,
    });
    mocks.useAdminNodes.mockReturnValue({
      data: [node(), node({ id: 2, name: "node-2", enabled: false, healthy: false })],
      isLoading: false,
      isSuccess: true,
      isError: false,
      error: null,
    });

    renderStrip();

    expect(screen.getByText("1/1")).toBeTruthy();
    expect(screen.getByText("healthy")).toBeTruthy();
  });

  it("says where transcodes run when no stream nodes are registered", () => {
    mocks.useAdminServerStatus.mockReturnValue({
      data: status(),
      isLoading: false,
      error: null,
    });

    renderStrip();

    expect(screen.getByText("none")).toBeTruthy();
    expect(screen.getByText("this server transcodes")).toBeTruthy();
  });

  // "none" is a claim about the deployment; an unresolved query cannot back
  // it. Loading shows a placeholder, a failure says the list is unavailable.
  it("does not claim no nodes while the node list is loading", () => {
    mocks.useAdminServerStatus.mockReturnValue({
      data: status(),
      isLoading: false,
      error: null,
    });
    mocks.useAdminNodes.mockReturnValue({
      data: undefined,
      isLoading: true,
      isSuccess: false,
      isError: false,
      error: null,
    });

    renderStrip();

    expect(screen.queryByText("none")).toBeNull();
    expect(screen.queryByText("this server transcodes")).toBeNull();
  });

  it("marks the node ratio unavailable when the node query fails", () => {
    mocks.useAdminServerStatus.mockReturnValue({
      data: status(),
      isLoading: false,
      error: null,
    });
    mocks.useAdminNodes.mockReturnValue({
      data: undefined,
      isLoading: false,
      isSuccess: false,
      isError: true,
      error: new Error("boom"),
    });

    renderStrip();

    expect(screen.queryByText("none")).toBeNull();
    expect(screen.getByText("unavailable")).toBeTruthy();
  });

  it("surfaces a failed status load", () => {
    mocks.useAdminServerStatus.mockReturnValue({
      data: undefined,
      isLoading: false,
      error: new Error("boom"),
    });

    renderStrip();

    expect(screen.getByText("Failed to load server health.")).toBeTruthy();
  });
});
