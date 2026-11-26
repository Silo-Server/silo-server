// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DashboardGrid } from "./DashboardGrid";
import type { DashboardLayoutEntry } from "./types";
import type { DashboardLayout } from "./useDashboardLayout";

// The widgets themselves fetch data; the grid's gestures don't depend on them.
vi.mock("./registry", async (original) => {
  const actual = await original<typeof import("./registry")>();
  return {
    ...actual,
    getDashboardWidget: (id: Parameters<typeof actual.getDashboardWidget>[0]) => ({
      ...actual.getDashboardWidget(id),
      Component: () => null,
    }),
  };
});

window.HTMLElement.prototype.setPointerCapture ??= () => {};
window.HTMLElement.prototype.hasPointerCapture ??= () => false;
window.HTMLElement.prototype.releasePointerCapture ??= () => {};

function layoutWith(
  entries: DashboardLayoutEntry[],
  resizeWidget: DashboardLayout["resizeWidget"],
) {
  return {
    entries,
    hiddenWidgets: [],
    isCustomizing: true,
    setCustomizing: vi.fn(),
    moveWidget: vi.fn(),
    resizeWidget,
    setWidgetRange: vi.fn(),
    setWidgetView: vi.fn(),
    removeWidget: vi.fn(),
    addWidget: vi.fn(),
    resetLayout: vi.fn(),
    reloadServerLayout: vi.fn(),
    serverSaveBlocked: false,
  } as unknown as DashboardLayout;
}

function Grid({ layout }: { layout: DashboardLayout }) {
  return <DashboardGrid layout={layout} isAddPanelOpen={false} onAddPanelOpenChange={() => {}} />;
}

const resized: DashboardLayoutEntry[] = [
  { id: "stat-movies", span: 3, rows: 1 },
  { id: "playback-24h", span: 9, rows: 2 },
];

function pressHandle() {
  const handle = screen.getByRole("button", { name: /^Resize Playback activity/ });
  fireEvent.pointerDown(handle, { pointerId: 1, button: 0, isPrimary: true, clientX: 100 });
  return handle;
}

describe("DashboardGrid pointer resize", () => {
  afterEach(cleanup);

  it("saves the size the pointer reached when the layout keeps its geometry", () => {
    const resizeWidget = vi.fn();
    const { rerender } = render(<Grid layout={layoutWith(resized, resizeWidget)} />);
    const handle = pressHandle();
    // A range change replaces the entries but moves nothing. jsdom reports a
    // zero-width grid, so each pixel counts as one column.
    rerender(
      <Grid
        layout={layoutWith(
          resized.map((entry) => ({ ...entry, range: "week" })),
          resizeWidget,
        )}
      />,
    );
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 102 });
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 102 });
    expect(resizeWidget).toHaveBeenCalledWith("playback-24h", { span: 11, rows: 2 });
  });

  it("drops the resize when another control changes the layout under it", () => {
    const resizeWidget = vi.fn();
    const { rerender } = render(<Grid layout={layoutWith(resized, resizeWidget)} />);
    const handle = pressHandle();
    vi.spyOn(handle, "hasPointerCapture").mockReturnValue(true);
    const release = vi.spyOn(handle, "releasePointerCapture");
    // Remove on the widget before it, activated from the keyboard while the
    // mouse holds the handle: Playback keeps its size but moves. Reset and a
    // server layout change the same order and sizes.
    rerender(
      <Grid layout={layoutWith([{ id: "playback-24h", span: 9, rows: 2 }], resizeWidget)} />,
    );
    fireEvent.lostPointerCapture(handle, { pointerId: 1 });
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 400 });
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 400 });
    expect(resizeWidget).not.toHaveBeenCalled();
    // The handle lets go of the pointer as the resize ends.
    expect(release).toHaveBeenCalledWith(1);
  });
});

describe("DashboardGrid add sheet", () => {
  it("keeps wheel input over the sheet from scrolling the dashboard", () => {
    const layout = layoutWith(resized, vi.fn());
    render(<DashboardGrid layout={layout} isAddPanelOpen onAddPanelOpenChange={() => {}} />);
    const event = new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: 40 });
    screen.getByRole("heading", { name: "Add widget" }).dispatchEvent(event);
    expect(event.defaultPrevented).toBe(true);
  });
});
