// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { StreamNode } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useAdminNodes: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/nodes", () => ({
  useAdminNodes: mocks.useAdminNodes,
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

/**
 * jsdom has no layout, so the widget's ResizeObserver never fires and the
 * widget stays in row mode. This stub reports a chosen content width on
 * observe, which is exactly what a real observer does on mount.
 */
function stubResizeObserver(width: number) {
  class FakeResizeObserver {
    private readonly callback: ResizeObserverCallback;
    constructor(callback: ResizeObserverCallback) {
      this.callback = callback;
    }
    observe() {
      this.callback(
        [{ contentRect: { width, height: 300 } } as ResizeObserverEntry],
        this as unknown as ResizeObserver,
      );
    }
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
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
  });

  afterEach(() => {
    vi.unstubAllGlobals();
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

  it("keeps stacked rows in a narrow slot", () => {
    stubResizeObserver(400);
    mocks.useAdminNodes.mockReturnValue({
      data: [node()],
      isLoading: false,
      error: null,
    });

    const { container } = renderWidget();

    expect(container.querySelector('[style*="grid-template-columns"]')).toBeNull();
  });

  it("lays nodes out as a card grid once two columns fit", () => {
    stubResizeObserver(640);
    mocks.useAdminNodes.mockReturnValue({
      data: [node(), node({ id: 2, name: "node-2", type: "proxy" })],
      isLoading: false,
      error: null,
    });

    const { container } = renderWidget();

    const grid = container.querySelector('[style*="grid-template-columns"]');
    expect(grid).not.toBeNull();
    expect((grid as HTMLElement).style.gridTemplateColumns).toBe("repeat(2, minmax(0, 1fr))");
  });

  it("says where transcodes run when no nodes exist", () => {
    mocks.useAdminNodes.mockReturnValue({ data: [], isLoading: false, error: null });

    renderWidget();

    expect(screen.getByText("No stream nodes — transcodes run on this server")).toBeTruthy();
  });
});
