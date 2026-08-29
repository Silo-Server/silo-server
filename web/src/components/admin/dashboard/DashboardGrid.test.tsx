import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminSession } from "@/api/types";
import type { DashboardLayout } from "./useDashboardLayout";

const mocks = vi.hoisted(() => ({
  useAdminSessions: vi.fn(),
  useAdminStats: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/stats", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/admin/stats")>(
    "@/hooks/queries/admin/stats",
  );
  return {
    ...actual,
    useAdminSessions: mocks.useAdminSessions,
    useAdminStats: mocks.useAdminStats,
  };
});

import { DashboardGrid, WIDGET_ADD_DRAG_TYPE } from "./DashboardGrid";
import { getDashboardWidget } from "./registry";

function session(overrides: Partial<AdminSession> = {}): AdminSession {
  return {
    session_id: "s1",
    user_id: 1,
    username: "quick",
    media_file_id: 7,
    media_title: "Arrival",
    media_type: "movie",
    is_paused: false,
    started_at: new Date(Date.now() - 60_000).toISOString(),
    ...overrides,
  } as AdminSession;
}

/**
 * A layout holding only the now-playing widget: the grid renders whatever is in
 * `entries`, so a one-entry layout keeps the test off every other widget's data
 * hooks.
 */
function layoutWith(
  isCustomizing: boolean,
  overrides: Partial<DashboardLayout> = {},
): DashboardLayout {
  return {
    entries: [{ id: "now-playing", span: 12, rows: 3 }],
    hiddenWidgets: [],
    isCustomizing,
    setCustomizing: vi.fn(),
    moveWidget: vi.fn(),
    resizeWidget: vi.fn(),
    setWidgetRange: vi.fn(),
    removeWidget: vi.fn(),
    addWidget: vi.fn(),
    resetLayout: vi.fn(),
    ...overrides,
  };
}

function renderGrid(isCustomizing = false) {
  return render(
    <MemoryRouter>
      <DashboardGrid
        layout={layoutWith(isCustomizing)}
        isAddPanelOpen={false}
        onAddPanelOpenChange={() => {}}
      />
    </MemoryRouter>,
  );
}

function rowsOf(container: HTMLElement): string | undefined {
  const host = container.querySelector<HTMLElement>('[data-widget-id="now-playing"]');
  return host?.style.getPropertyValue("--widget-rows");
}

describe("DashboardGrid collapse", () => {
  beforeEach(() => {
    mocks.useAdminSessions.mockReset();
  });

  it("gives back rows when a collapsible widget reports it is empty", async () => {
    mocks.useAdminSessions.mockReturnValue({ data: [], isLoading: false, error: null });

    const { container } = renderGrid();

    expect(await screen.findByText("Nothing playing right now")).toBeTruthy();
    expect(rowsOf(container)).toBe("1");
    expect(
      container.querySelector('[data-widget-id="now-playing"]')?.getAttribute("data-collapsed"),
    ).toBe("true");
  });

  it("keeps its full height while loading and after a failure", () => {
    mocks.useAdminSessions.mockReturnValue({ data: undefined, isLoading: true, error: null });
    const loading = renderGrid();
    expect(rowsOf(loading.container)).toBe("3");
    loading.unmount();

    mocks.useAdminSessions.mockReturnValue({
      data: undefined,
      isLoading: false,
      error: new Error("boom"),
    });
    const failed = renderGrid();
    expect(screen.getByText("Failed to load streams.")).toBeTruthy();
    expect(rowsOf(failed.container)).toBe("3");
  });

  it("stays full size in customize mode so drag and resize targets hold still", async () => {
    mocks.useAdminSessions.mockReturnValue({ data: [], isLoading: false, error: null });

    const { container } = renderGrid(true);

    expect(await screen.findByText("Nothing playing right now")).toBeTruthy();
    expect(rowsOf(container)).toBe("3");
    expect(
      container.querySelector('[data-widget-id="now-playing"]')?.getAttribute("data-collapsed"),
    ).toBeNull();
  });

  it("grows back to its placed height once a stream appears", async () => {
    mocks.useAdminSessions.mockReturnValue({ data: [], isLoading: false, error: null });

    const { container, rerender } = renderGrid();
    expect(rowsOf(container)).toBe("1");

    mocks.useAdminSessions.mockReturnValue({
      data: [session()],
      isLoading: false,
      error: null,
    });
    rerender(
      <MemoryRouter>
        <DashboardGrid
          layout={layoutWith(false)}
          isAddPanelOpen={false}
          onAddPanelOpenChange={() => {}}
        />
      </MemoryRouter>,
    );

    expect(await screen.findByText("Arrival")).toBeTruthy();
    expect(rowsOf(container)).toBe("3");
  });
});

/**
 * jsdom implements neither DragEvent nor DataTransfer, so drag events are
 * dispatched with this stand-in carrying the payload between dragstart and
 * drop, the way a real drag session would.
 */
function stubDataTransfer() {
  const store = new Map<string, string>();
  return {
    setData(type: string, value: string) {
      store.set(type, value);
    },
    getData(type: string) {
      return store.get(type) ?? "";
    },
    get types() {
      return [...store.keys()];
    },
    effectAllowed: "none",
    dropEffect: "none",
  };
}

/**
 * Dispatches a drag event as a MouseEvent so `clientX` survives — jsdom has no
 * DragEvent, and testing-library's fallback drops mouse coordinates — with the
 * stubbed dataTransfer attached the way a real drag event would carry it.
 */
function fireDragEvent(
  element: Element,
  type: "dragstart" | "dragover" | "dragend" | "drop",
  dataTransfer: ReturnType<typeof stubDataTransfer>,
  clientX = 0,
  clientY = 0,
) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, clientX, clientY });
  Object.defineProperty(event, "dataTransfer", { value: dataTransfer });
  fireEvent(element, event);
}

describe("DashboardGrid add-widget drag", () => {
  const libraries = getDashboardWidget("libraries");

  beforeEach(() => {
    mocks.useAdminSessions.mockReset();
    mocks.useAdminStats.mockReset();
    mocks.useAdminSessions.mockReturnValue({ data: [], isLoading: false, error: null });
    // Loading keeps the stat tile on its skeleton, off the stats data shape.
    mocks.useAdminStats.mockReturnValue({ data: undefined, isLoading: true, error: null });
  });

  function renderCustomizing(overrides: Partial<DashboardLayout> = {}) {
    const layout = layoutWith(true, { hiddenWidgets: [libraries], ...overrides });
    const onAddPanelOpenChange = vi.fn();
    const view = render(
      <MemoryRouter>
        <DashboardGrid layout={layout} isAddPanelOpen onAddPanelOpenChange={onAddPanelOpenChange} />
      </MemoryRouter>,
    );
    return { layout, onAddPanelOpenChange, ...view };
  }

  function sheetEntry(): HTMLElement {
    return screen.getByRole("button", { name: new RegExp(libraries.title) });
  }

  it("dropping a sheet entry before a placed widget inserts it there in one call", () => {
    const { layout, onAddPanelOpenChange, container } = renderCustomizing();
    const dataTransfer = stubDataTransfer();

    fireDragEvent(sheetEntry(), "dragstart", dataTransfer);
    expect(dataTransfer.getData(WIDGET_ADD_DRAG_TYPE)).toBe("libraries");
    expect(dataTransfer.effectAllowed).toBe("copy");

    const host = container.querySelector('[data-widget-id="now-playing"]');
    if (!host) throw new Error("expected the placed widget");
    // jsdom rects are all zeros, so a negative clientX lands on the left half.
    fireDragEvent(host, "drop", dataTransfer, -5);

    expect(layout.addWidget).toHaveBeenCalledTimes(1);
    expect(layout.addWidget).toHaveBeenCalledWith("libraries", "now-playing");
    expect(layout.moveWidget).not.toHaveBeenCalled();
    expect(onAddPanelOpenChange).toHaveBeenCalledWith(false);
    expect(screen.getByRole("status").textContent).toContain("added at position 1 of 2");
  });

  it("dropping a sheet entry on the trailing area appends", () => {
    const { layout, container } = renderCustomizing();
    const dataTransfer = stubDataTransfer();

    fireDragEvent(sheetEntry(), "dragstart", dataTransfer);
    const grid = container.querySelector(".admin-widget-grid");
    if (!grid) throw new Error("expected the widget grid");
    fireDragEvent(grid, "drop", dataTransfer, 5);

    expect(layout.addWidget).toHaveBeenCalledWith("libraries", null);
  });

  it("gets the sheet out of the pointer's way during the drag and restores it on cancel", () => {
    const { layout } = renderCustomizing();
    const dataTransfer = stubDataTransfer();
    const sheet = document.querySelector('[data-slot="sheet-content"]');
    if (!sheet) throw new Error("expected the add-widget sheet");

    fireDragEvent(sheetEntry(), "dragstart", dataTransfer);
    expect(sheet.className).toContain("pointer-events-none");

    // A drag that ends without a valid drop only fires dragend.
    fireDragEvent(sheetEntry(), "dragend", dataTransfer);
    expect(sheet.className).not.toContain("pointer-events-none");
    expect(layout.addWidget).not.toHaveBeenCalled();
  });

  // The sheet-add path already appends on the trailing area; a reorder
  // dropped there must not be a silent no-op — mouse users could otherwise
  // never move a widget into the last position.
  it("dropping a reorder on the trailing area appends", () => {
    const { layout, container } = renderCustomizing({
      entries: [
        { id: "now-playing", span: 12, rows: 3 },
        { id: "stat-movies", span: 2, rows: 1 },
      ],
    });
    const dataTransfer = stubDataTransfer();
    const nowPlaying = container.querySelector('[data-widget-id="now-playing"]');
    const grid = container.querySelector(".admin-widget-grid");
    if (!nowPlaying || !grid) throw new Error("expected the widget and grid");

    fireDragEvent(nowPlaying, "dragstart", dataTransfer);
    fireDragEvent(grid, "drop", dataTransfer, 5);

    expect(layout.moveWidget).toHaveBeenCalledWith("now-playing", null);
    expect(layout.addWidget).not.toHaveBeenCalled();
  });

  it("a reorder drag is never treated as an add", () => {
    const { layout, container } = renderCustomizing({
      entries: [
        { id: "now-playing", span: 12, rows: 3 },
        { id: "stat-movies", span: 2, rows: 1 },
      ],
    });
    const dataTransfer = stubDataTransfer();
    const nowPlaying = container.querySelector('[data-widget-id="now-playing"]');
    const statMovies = container.querySelector('[data-widget-id="stat-movies"]');
    if (!nowPlaying || !statMovies) throw new Error("expected both placed widgets");

    fireDragEvent(nowPlaying, "dragstart", dataTransfer);
    fireDragEvent(statMovies, "drop", dataTransfer, -5);

    expect(layout.moveWidget).toHaveBeenCalledWith("now-playing", "stat-movies");
    expect(layout.addWidget).not.toHaveBeenCalled();
  });
});

describe("DashboardGrid drag auto-scroll", () => {
  beforeEach(() => {
    mocks.useAdminSessions.mockReset();
    mocks.useAdminStats.mockReset();
    mocks.useAdminSessions.mockReturnValue({ data: [], isLoading: false, error: null });
    mocks.useAdminStats.mockReturnValue({ data: undefined, isLoading: true, error: null });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("a dragover near the top edge scrolls the container up and dragend stops it", () => {
    // A synchronous stand-in for requestAnimationFrame: the loop's frames pile
    // up here and the test pumps them by hand, so a loop that never stops
    // would run away only as far as `runFrames` lets it.
    const frames: FrameRequestCallback[] = [];
    vi.stubGlobal(
      "requestAnimationFrame",
      vi.fn((cb: FrameRequestCallback) => frames.push(cb)),
    );
    vi.stubGlobal("cancelAnimationFrame", vi.fn());
    const runFrames = (n: number) => {
      for (let i = 0; i < n && frames.length > 0; i++) frames.shift()!(0);
    };

    const { container } = render(
      <MemoryRouter>
        <DashboardGrid
          layout={layoutWith(true, {
            entries: [
              { id: "now-playing", span: 12, rows: 3 },
              { id: "stat-movies", span: 2, rows: 1 },
            ],
          })}
          isAddPanelOpen={false}
          onAddPanelOpenChange={() => {}}
        />
      </MemoryRouter>,
    );

    // The RTL container is the grid's parent; dress it up as the scrollable
    // ancestor findScrollContainer should resolve (jsdom has no layout, so
    // scroll metrics and the rect are stubbed).
    container.style.overflowY = "auto";
    Object.defineProperty(container, "scrollHeight", { value: 1000, configurable: true });
    Object.defineProperty(container, "clientHeight", { value: 400, configurable: true });
    container.getBoundingClientRect = () => ({ top: 0, bottom: 400 }) as DOMRect;
    let scrollTop = 100;
    Object.defineProperty(container, "scrollTop", {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => {
        scrollTop = Math.max(0, value);
      },
    });

    const nowPlaying = container.querySelector('[data-widget-id="now-playing"]');
    const statMovies = container.querySelector('[data-widget-id="stat-movies"]');
    if (!nowPlaying || !statMovies) throw new Error("expected both placed widgets");

    const dataTransfer = stubDataTransfer();
    fireDragEvent(nowPlaying, "dragstart", dataTransfer);
    // Pointer 10px from the top edge: well inside the edge zone.
    fireDragEvent(statMovies, "dragover", dataTransfer, 0, 10);

    runFrames(3);
    expect(scrollTop).toBeLessThan(100);
    const afterScrolling = scrollTop;
    expect(frames.length).toBeGreaterThan(0); // still looping mid-drag

    fireDragEvent(nowPlaying, "dragend", dataTransfer);
    runFrames(5);
    expect(scrollTop).toBe(afterScrolling); // stopped: no further scrolling
    expect(frames.length).toBe(0); // and nothing new scheduled
  });
});
