// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
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

function renderWidget() {
  return render(
    <MemoryRouter>
      <TranscodeNodesWidget />
    </MemoryRouter>,
  );
}

describe("TranscodeNodesWidget", () => {
  beforeEach(() => {
    mocks.useAdminNodes.mockReset();
    mocks.useBuildInfo.mockReset();
    mocks.useBuildInfo.mockReturnValue({
      data: { display: "aaaaaaaa", revision: "aaaaaaaa1111", available: true },
    });
  });

  it("sorts nodes by type, then name", () => {
    mocks.useAdminNodes.mockReturnValue({
      data: [
        node({ id: 1, name: "t-zulu", type: "transcode" }),
        node({ id: 2, name: "p-alpha", type: "proxy" }),
        node({ id: 3, name: "t-alpha", type: "transcode" }),
      ],
      isLoading: false,
      error: null,
    });

    renderWidget();

    const names = screen
      .getAllByText(/^(t-|p-)/)
      .map((el) => el.textContent)
      .filter((text) => text === "t-zulu" || text === "t-alpha" || text === "p-alpha");
    expect(names).toEqual(["p-alpha", "t-alpha", "t-zulu"]);
  });

  it("shows each node's build and flags one that differs from the server", () => {
    mocks.useAdminNodes.mockReturnValue({
      data: [
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
      ],
      isLoading: false,
      error: null,
    });

    renderWidget();

    expect(screen.getByText("build 7 · aaaaaaaa")).toBeTruthy();
    expect(screen.getByText("build 6 · bbbbbbbb · differs from server")).toBeTruthy();
    // A node predating build reporting draws nothing rather than "unknown".
    expect(screen.getAllByText(/^build /)).toHaveLength(2);
  });

  it("does not flag a build whose revision cannot be compared", () => {
    mocks.useAdminNodes.mockReturnValue({
      data: [
        node({
          last_stats: { build: { display: "unavailable", revision: "", available: false } },
        }),
      ],
      isLoading: false,
      error: null,
    });

    renderWidget();

    expect(screen.getByText("build unavailable")).toBeTruthy();
    expect(screen.queryByText(/differs from server/)).toBeNull();
  });
});
