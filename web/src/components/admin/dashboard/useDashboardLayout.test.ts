// @vitest-environment jsdom

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminDashboardLayoutResponse } from "@/api/types";
import { DASHBOARD_WIDGETS, DEFAULT_LAYOUT } from "./registry";
import {
  DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS,
  dashboardLayoutStorageKey,
  LEGACY_DASHBOARD_LAYOUT_STORAGE_KEY,
  useDashboardLayout,
} from "./useDashboardLayout";
import type { DashboardLayoutEntry } from "./types";

const ADMIN_USER_ID = 1;

const mocks = vi.hoisted(() => ({
  query: { data: undefined as AdminDashboardLayoutResponse | undefined, isSuccess: false },
  save: vi.fn(),
  reset: vi.fn(),
  userId: 1,
}));

vi.mock("@/hooks/queries/admin/dashboardLayout", () => ({
  useAdminDashboardLayout: () => mocks.query,
  useSaveAdminDashboardLayout: () => ({ mutate: mocks.save }),
  useResetAdminDashboardLayout: () => ({ mutate: mocks.reset }),
}));

// The layout cache is keyed by the signed-in account, so every test needs an
// authenticated user; `mocks.userId` is the account the hook sees.
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ user: { id: mocks.userId } }),
}));

function storageKey(userId: number = mocks.userId): string {
  return dashboardLayoutStorageKey(userId);
}

function serverLayout(entries: unknown, updatedAt = "2026-08-26T10:00:00Z") {
  mocks.query = {
    data: { layout: { version: 1, entries }, updated_at: updatedAt },
    isSuccess: true,
  };
}

function serverNoLayout() {
  mocks.query = { data: { layout: null, updated_at: null }, isSuccess: true };
}

function readStored(userId?: number): { version: number; entries: DashboardLayoutEntry[] } {
  const raw = window.localStorage.getItem(storageKey(userId));
  if (raw === null) {
    throw new Error("expected a persisted layout");
  }
  return JSON.parse(raw) as { version: number; entries: DashboardLayoutEntry[] };
}

function writeStored(entries: unknown, userId?: number) {
  window.localStorage.setItem(storageKey(userId), JSON.stringify({ version: 1, entries }));
}

// A widget joins the registry before it joins DEFAULT_LAYOUT — new widgets
// ship hidden and are discovered through the Add-widget sheet — so expected
// "hidden" sets are derived from the registry instead of hardcoded.
function hiddenWidgetIds(...alsoRemoved: string[]): string[] {
  const visible = new Set(
    DEFAULT_LAYOUT.map((entry) => entry.id).filter((id) => !alsoRemoved.includes(id)),
  );
  return DASHBOARD_WIDGETS.filter((widget) => !visible.has(widget.id)).map((widget) => widget.id);
}

describe("useDashboardLayout", () => {
  beforeEach(() => {
    window.localStorage.clear();
    mocks.userId = ADMIN_USER_ID;
    mocks.query = { data: undefined, isSuccess: false };
    mocks.save.mockReset();
    mocks.reset.mockReset();
  });

  it("uses the default layout when storage is empty", () => {
    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual(DEFAULT_LAYOUT);
    expect(result.current.hiddenWidgets.map((w) => w.id)).toEqual(hiddenWidgetIds());
    expect(result.current.isCustomizing).toBe(false);
  });

  it("falls back to the default layout on corrupt JSON", () => {
    window.localStorage.setItem(storageKey(), "{not json");

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual(DEFAULT_LAYOUT);
  });

  it("falls back to the default layout on an unexpected shape", () => {
    window.localStorage.setItem(
      storageKey(),
      JSON.stringify({ version: 2, entries: [{ id: "libraries", span: 7 }] }),
    );

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual(DEFAULT_LAYOUT);
  });

  it("drops unknown widget ids on load", () => {
    writeStored([
      { id: "libraries", span: 7 },
      { id: "not-a-widget", span: 6 },
      { id: "users", span: 5 },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([
      { id: "libraries", span: 7, rows: 4 },
      { id: "users", span: 5, rows: 4 },
    ]);
  });

  it("clamps spans to the widget's [minSpan, maxSpan] on load", () => {
    writeStored([
      { id: "stat-movies", span: 1, rows: 1 }, // min 2
      { id: "now-playing", span: 40, rows: 4 }, // max 12
      { id: "users", span: "wide", rows: 4 }, // non-numeric -> defaultSpan
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([
      { id: "stat-movies", span: 2, rows: 1 },
      { id: "now-playing", span: 12, rows: 4 },
      { id: "users", span: 5, rows: 4 },
    ]);
  });

  it("clamps rows to the widget's [minRows, maxRows] on load", () => {
    writeStored([
      { id: "stat-movies", span: 3, rows: 0 }, // min 1
      { id: "now-playing", span: 12, rows: 40 }, // max 8
      { id: "users", span: 5, rows: "tall" }, // non-numeric -> defaultRows
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([
      { id: "stat-movies", span: 3, rows: 1 },
      { id: "now-playing", span: 12, rows: 8 },
      { id: "users", span: 5, rows: 4 },
    ]);
  });

  // Every layout saved before two-axis resizing shipped is missing `rows`; the
  // widget's default height is what those admins were already looking at.
  it("gives entries without rows the widget's default height", () => {
    writeStored([
      { id: "libraries", span: 7 },
      { id: "watch-providers", span: 9 },
      { id: "stat-movies", span: 3 },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([
      { id: "libraries", span: 7, rows: 4 },
      { id: "watch-providers", span: 9, rows: 1 },
      { id: "stat-movies", span: 3, rows: 1 },
    ]);
  });

  // "trakt-sync" became "watch-providers" once the widget covered every watch
  // provider. A layout stored under the old id is the admin's arrangement, so
  // it resolves to the replacement rather than being dropped as unknown.
  it("resolves a renamed widget id to its replacement", () => {
    writeStored([
      { id: "libraries", span: 7, rows: 4 },
      { id: "trakt-sync", span: 9, rows: 1 },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([
      { id: "libraries", span: 7, rows: 4 },
      { id: "watch-providers", span: 9, rows: 1 },
    ]);
  });

  // A document holding both ids must not render the widget twice: dedupe keys
  // off the resolved widget, not the stored string.
  it("collapses a renamed id and its replacement into one entry", () => {
    writeStored([
      { id: "trakt-sync", span: 9, rows: 1 },
      { id: "watch-providers", span: 12, rows: 2 },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([{ id: "watch-providers", span: 9, rows: 1 }]);
  });

  // Windows arrived after the first layouts were saved, so an entry without
  // one is the common case rather than a corrupt one.
  it("fills in the widget's default window and leaves unranged widgets alone", () => {
    writeStored([
      { id: "egress-24h", span: 6, rows: 3 },
      { id: "top-titles", span: 6, rows: 3 },
      { id: "users", span: 5, rows: 4 },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([
      { id: "egress-24h", span: 6, rows: 3, range: "day" },
      { id: "top-titles", span: 6, rows: 3, range: "week" },
      { id: "users", span: 5, rows: 4 },
    ]);
  });

  it("keeps a stored window the widget allows", () => {
    writeStored([
      { id: "egress-24h", span: 6, rows: 3, range: "month" },
      { id: "top-titles", span: 6, rows: 3, range: "day" },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([
      { id: "egress-24h", span: 6, rows: 3, range: "month" },
      { id: "top-titles", span: 6, rows: 3, range: "day" },
    ]);
  });

  // The leaderboards do not offer an hour, and an unranged widget must not
  // start carrying a window because one was stored for it.
  it("replaces a window the widget does not offer and drops one it cannot use", () => {
    writeStored([
      { id: "top-profiles", span: 6, rows: 3, range: "hour" },
      { id: "egress-24h", span: 6, rows: 3, range: "fortnight" },
      { id: "concurrent-streams-24h", span: 6, rows: 3, range: 7 },
      { id: "users", span: 5, rows: 4, range: "month" },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([
      { id: "top-profiles", span: 6, rows: 3, range: "week" },
      { id: "egress-24h", span: 6, rows: 3, range: "day" },
      { id: "concurrent-streams-24h", span: 6, rows: 3, range: "day" },
      { id: "users", span: 5, rows: 4 },
    ]);
    expect(result.current.entries[3]).not.toHaveProperty("range");
  });

  it("setWidgetRange changes the window and persists", () => {
    writeStored([{ id: "egress-24h", span: 6, rows: 3, range: "day" }]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.setWidgetRange("egress-24h", "week");
    });

    expect(result.current.entries).toEqual([{ id: "egress-24h", span: 6, rows: 3, range: "week" }]);
    expect(readStored().entries).toEqual([{ id: "egress-24h", span: 6, rows: 3, range: "week" }]);
  });

  // Picking a window is an everyday viewing action, not an arrangement one.
  it("setWidgetRange works outside customize mode", () => {
    writeStored([{ id: "top-titles", span: 6, rows: 3, range: "week" }]);
    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.isCustomizing).toBe(false);

    act(() => {
      result.current.setWidgetRange("top-titles", "month");
    });

    expect(result.current.entries).toEqual([
      { id: "top-titles", span: 6, rows: 3, range: "month" },
    ]);
    expect(readStored().entries).toEqual([{ id: "top-titles", span: 6, rows: 3, range: "month" }]);
  });

  it("setWidgetRange ignores a window the widget does not offer", () => {
    writeStored([
      { id: "top-titles", span: 6, rows: 3, range: "week" },
      { id: "users", span: 5, rows: 4 },
    ]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.setWidgetRange("top-titles", "hour");
      result.current.setWidgetRange("users", "month");
    });

    expect(result.current.entries).toEqual([
      { id: "top-titles", span: 6, rows: 3, range: "week" },
      { id: "users", span: 5, rows: 4 },
    ]);
  });

  it("round-trips a chosen window through localStorage", () => {
    const first = renderHook(() => useDashboardLayout());
    act(() => {
      first.result.current.setWidgetRange("egress-24h", "month");
      first.result.current.setWidgetRange("top-profiles", "day");
    });
    const saved = first.result.current.entries;
    first.unmount();

    const second = renderHook(() => useDashboardLayout());

    expect(second.result.current.entries).toEqual(saved);
    expect(second.result.current.entries).toContainEqual({
      id: "egress-24h",
      span: 6,
      rows: 3,
      range: "month",
    });
    expect(second.result.current.entries).toContainEqual({
      id: "top-profiles",
      span: 6,
      rows: 3,
      range: "day",
    });
  });

  it("addWidget starts a ranged widget on its default window", () => {
    writeStored([{ id: "libraries", span: 7, rows: 4 }]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.addWidget("playback-reliability");
    });

    expect(result.current.entries).toContainEqual({
      id: "playback-reliability",
      span: 6,
      rows: 3,
      range: "day",
    });
  });

  it("exposes hidden widgets in registry order", () => {
    writeStored([
      { id: "users", span: 5, rows: 4 },
      { id: "stat-storage", span: 3, rows: 1 },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.hiddenWidgets.map((w) => w.id)).toEqual(
      DASHBOARD_WIDGETS.filter((w) => w.id !== "users" && w.id !== "stat-storage").map((w) => w.id),
    );
  });

  it("addWidget appends with the default span and rows and persists", () => {
    writeStored([{ id: "libraries", span: 7, rows: 4 }]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.addWidget("now-playing");
    });

    const expected = [
      { id: "libraries", span: 7, rows: 4 },
      { id: "now-playing", span: 12, rows: 3 },
    ];
    expect(result.current.entries).toEqual(expected);
    expect(readStored()).toEqual({ version: 1, entries: expected });
  });

  it("addWidget with a beforeId inserts in front of that entry and persists", () => {
    writeStored([
      { id: "libraries", span: 7, rows: 4 },
      { id: "users", span: 5, rows: 4 },
    ]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.addWidget("now-playing", "users");
    });

    const expected = [
      { id: "libraries", span: 7, rows: 4 },
      { id: "now-playing", span: 12, rows: 3 },
      { id: "users", span: 5, rows: 4 },
    ];
    expect(result.current.entries).toEqual(expected);
    expect(readStored()).toEqual({ version: 1, entries: expected });
  });

  it("addWidget with a beforeId stamps a ranged widget's default window", () => {
    writeStored([
      { id: "libraries", span: 7, rows: 4 },
      { id: "users", span: 5, rows: 4 },
    ]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.addWidget("playback-reliability", "libraries");
    });

    expect(result.current.entries).toEqual([
      { id: "playback-reliability", span: 6, rows: 3, range: "day" },
      { id: "libraries", span: 7, rows: 4 },
      { id: "users", span: 5, rows: 4 },
    ]);
  });

  // The drop anchor may have been removed between the drag starting and the
  // drop landing; the add still happens rather than vanishing.
  it("addWidget appends when the beforeId is not placed", () => {
    writeStored([{ id: "libraries", span: 7, rows: 4 }]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.addWidget("now-playing", "users");
    });

    expect(result.current.entries.map((entry) => entry.id)).toEqual(["libraries", "now-playing"]);
  });

  it("addWidget ignores a widget that is already placed, beforeId or not", () => {
    writeStored([
      { id: "libraries", span: 7, rows: 4 },
      { id: "users", span: 5, rows: 4 },
    ]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.addWidget("users", "libraries");
      result.current.addWidget("libraries");
    });

    expect(result.current.entries.map((entry) => entry.id)).toEqual(["libraries", "users"]);
  });

  it("removeWidget hides the widget and persists", () => {
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.removeWidget("top-titles");
    });

    expect(result.current.entries.some((entry) => entry.id === "top-titles")).toBe(false);
    expect(result.current.hiddenWidgets.map((w) => w.id)).toEqual(hiddenWidgetIds("top-titles"));
    expect(readStored().entries.some((entry) => entry.id === "top-titles")).toBe(false);
  });

  it("moveWidget inserts before the target and persists", () => {
    writeStored([
      { id: "libraries", span: 7, rows: 4 },
      { id: "users", span: 5, rows: 4 },
      { id: "recent-activity", span: 12, rows: 4 },
    ]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.moveWidget("recent-activity", "users");
    });

    expect(result.current.entries.map((entry) => entry.id)).toEqual([
      "libraries",
      "recent-activity",
      "users",
    ]);
    expect(readStored().entries.map((entry) => entry.id)).toEqual([
      "libraries",
      "recent-activity",
      "users",
    ]);
  });

  it("moveWidget with a null beforeId moves to the end", () => {
    writeStored([
      { id: "libraries", span: 7, rows: 4 },
      { id: "users", span: 5, rows: 4 },
    ]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.moveWidget("libraries", null);
    });

    expect(result.current.entries.map((entry) => entry.id)).toEqual(["users", "libraries"]);
    expect(readStored().entries.map((entry) => entry.id)).toEqual(["users", "libraries"]);
  });

  it("resizeWidget clamps the span and persists", () => {
    writeStored([{ id: "users", span: 5, rows: 4 }]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.resizeWidget("users", { span: 6 });
    });
    expect(result.current.entries).toEqual([{ id: "users", span: 6, rows: 4 }]);
    expect(readStored().entries).toEqual([{ id: "users", span: 6, rows: 4 }]);

    act(() => {
      result.current.resizeWidget("users", { span: 99 });
    });
    expect(result.current.entries).toEqual([{ id: "users", span: 8, rows: 4 }]);

    act(() => {
      result.current.resizeWidget("users", { span: 1 });
    });
    expect(result.current.entries).toEqual([{ id: "users", span: 4, rows: 4 }]);
    expect(readStored().entries).toEqual([{ id: "users", span: 4, rows: 4 }]);
  });

  it("resizeWidget clamps rows and leaves the span alone", () => {
    writeStored([{ id: "users", span: 5, rows: 4 }]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.resizeWidget("users", { rows: 6 });
    });
    expect(result.current.entries).toEqual([{ id: "users", span: 5, rows: 6 }]);
    expect(readStored().entries).toEqual([{ id: "users", span: 5, rows: 6 }]);

    act(() => {
      result.current.resizeWidget("users", { rows: 99 });
    });
    expect(result.current.entries).toEqual([{ id: "users", span: 5, rows: 8 }]);

    act(() => {
      result.current.resizeWidget("users", { rows: 0 });
    });
    expect(result.current.entries).toEqual([{ id: "users", span: 5, rows: 2 }]);
  });

  it("resizeWidget changes both axes at once", () => {
    writeStored([{ id: "users", span: 5, rows: 4 }]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.resizeWidget("users", { span: 8, rows: 2 });
    });

    expect(result.current.entries).toEqual([{ id: "users", span: 8, rows: 2 }]);
    expect(readStored().entries).toEqual([{ id: "users", span: 8, rows: 2 }]);
  });

  // watch-providers tops out at 3 rows, so a taller resize clamps rather than
  // storing a height the grid would not render.
  it("resizeWidget clamps an axis to the widget's ceiling", () => {
    writeStored([{ id: "watch-providers", span: 9, rows: 1 }]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.resizeWidget("watch-providers", { span: 12, rows: 5 });
    });

    expect(result.current.entries).toEqual([{ id: "watch-providers", span: 12, rows: 3 }]);
  });

  it("resetLayout restores the defaults and clears storage", () => {
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.removeWidget("users");
      result.current.resizeWidget("libraries", { span: 12, rows: 6 });
    });
    expect(result.current.entries).not.toEqual(DEFAULT_LAYOUT);

    act(() => {
      result.current.resetLayout();
    });

    expect(result.current.entries).toEqual(DEFAULT_LAYOUT);
    expect(window.localStorage.getItem(storageKey())).toBeNull();
  });

  it("round-trips a customized layout through localStorage", () => {
    const first = renderHook(() => useDashboardLayout());
    act(() => {
      first.result.current.removeWidget("stat-shows");
      first.result.current.resizeWidget("libraries", { span: 12 });
      first.result.current.resizeWidget("users", { span: 8, rows: 6 });
      first.result.current.moveWidget("recent-activity", "now-playing");
    });
    const saved = first.result.current.entries;
    first.unmount();

    const second = renderHook(() => useDashboardLayout());
    expect(second.result.current.entries).toEqual(saved);
    expect(second.result.current.entries).toContainEqual({
      id: "users",
      span: 8,
      rows: 6,
    });
    expect(second.result.current.hiddenWidgets.map((w) => w.id)).toEqual(
      hiddenWidgetIds("stat-shows"),
    );
  });
});

describe("useDashboardLayout server persistence", () => {
  beforeEach(() => {
    window.localStorage.clear();
    mocks.userId = ADMIN_USER_ID;
    mocks.query = { data: undefined, isSuccess: false };
    mocks.save.mockReset();
    mocks.reset.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("adopts the server layout and mirrors it to localStorage", () => {
    writeStored([{ id: "users", span: 5, rows: 4 }]);
    serverLayout([
      { id: "libraries", span: 7, rows: 6 },
      { id: "now-playing", span: 12, rows: 2 },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    const expected = [
      { id: "libraries", span: 7, rows: 6 },
      { id: "now-playing", span: 12, rows: 2 },
    ];
    expect(result.current.entries).toEqual(expected);
    expect(readStored()).toEqual({ version: 1, entries: expected });
    expect(mocks.save).not.toHaveBeenCalled();
  });

  it("sanitizes the adopted server layout", () => {
    serverLayout([
      { id: "not-a-widget", span: 6, rows: 3 },
      { id: "users", span: 99, rows: 99 },
      { id: "users", span: 4, rows: 4 },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([{ id: "users", span: 8, rows: 8 }]);
  });

  it("fills in default rows for a server layout saved before row heights", () => {
    serverLayout([
      { id: "libraries", span: 7 },
      { id: "users", span: 5 },
    ]);

    const { result } = renderHook(() => useDashboardLayout());

    const expected = [
      { id: "libraries", span: 7, rows: 4 },
      { id: "users", span: 5, rows: 4 },
    ];
    expect(result.current.entries).toEqual(expected);
    expect(readStored()).toEqual({ version: 1, entries: expected });
  });

  it("keeps the local layout when the server document is not a v1 layout", () => {
    writeStored([{ id: "users", span: 5, rows: 4 }]);
    mocks.query = {
      data: { layout: { version: 99, entries: [] }, updated_at: "2026-08-26T10:00:00Z" },
      isSuccess: true,
    };

    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual([{ id: "users", span: 5, rows: 4 }]);
  });

  it("migrates a local-only layout to the server exactly once", () => {
    writeStored([{ id: "users", span: 5, rows: 6 }]);
    serverNoLayout();

    const { result, rerender } = renderHook(() => useDashboardLayout());

    expect(mocks.save).toHaveBeenCalledTimes(1);
    expect(mocks.save).toHaveBeenCalledWith({
      version: 1,
      entries: [{ id: "users", span: 5, rows: 6 }],
    });

    rerender();
    expect(mocks.save).toHaveBeenCalledTimes(1);
    expect(result.current.entries).toEqual([{ id: "users", span: 5, rows: 6 }]);
  });

  it("does not migrate when the browser has no stored layout", () => {
    serverNoLayout();

    const { result } = renderHook(() => useDashboardLayout());

    expect(mocks.save).not.toHaveBeenCalled();
    expect(result.current.entries).toEqual(DEFAULT_LAYOUT);
  });

  it("does not adopt a server layout over an edit made while the query was in flight", () => {
    const { result, rerender } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.removeWidget("users");
    });
    const edited = result.current.entries;

    serverLayout([{ id: "libraries", span: 7, rows: 4 }]);
    rerender();

    expect(result.current.entries).toEqual(edited);
  });

  it("debounces mutations into a single save of the full layout", () => {
    vi.useFakeTimers();
    writeStored([
      { id: "libraries", span: 7, rows: 4 },
      { id: "users", span: 5, rows: 4 },
    ]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.resizeWidget("users", { span: 4 });
      result.current.resizeWidget("users", { span: 6 });
      result.current.resizeWidget("users", { rows: 3 });
      result.current.removeWidget("libraries");
    });

    expect(mocks.save).not.toHaveBeenCalled();

    act(() => {
      vi.advanceTimersByTime(DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS);
    });

    expect(mocks.save).toHaveBeenCalledTimes(1);
    expect(mocks.save).toHaveBeenCalledWith({
      version: 1,
      entries: [{ id: "users", span: 6, rows: 3 }],
    });
  });

  it("flushes a queued save when the dashboard unmounts", () => {
    vi.useFakeTimers();
    writeStored([{ id: "users", span: 5, rows: 4 }]);
    const { result, unmount } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.resizeWidget("users", { span: 6, rows: 5 });
    });
    expect(mocks.save).not.toHaveBeenCalled();

    unmount();

    expect(mocks.save).toHaveBeenCalledTimes(1);
    expect(mocks.save).toHaveBeenCalledWith({
      version: 1,
      entries: [{ id: "users", span: 6, rows: 5 }],
    });
  });

  it("resetLayout deletes the server layout and drops the queued save", () => {
    vi.useFakeTimers();
    writeStored([{ id: "users", span: 5, rows: 4 }]);
    const { result } = renderHook(() => useDashboardLayout());

    act(() => {
      result.current.resizeWidget("users", { span: 6 });
      result.current.resetLayout();
    });

    act(() => {
      vi.advanceTimersByTime(DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS * 2);
    });

    expect(mocks.reset).toHaveBeenCalledTimes(1);
    expect(mocks.save).not.toHaveBeenCalled();
    expect(result.current.entries).toEqual(DEFAULT_LAYOUT);
    expect(window.localStorage.getItem(storageKey())).toBeNull();
  });
});

describe("useDashboardLayout account scoping", () => {
  const OTHER_ADMIN_USER_ID = 2;

  beforeEach(() => {
    window.localStorage.clear();
    mocks.userId = ADMIN_USER_ID;
    mocks.query = { data: undefined, isSuccess: false };
    mocks.save.mockReset();
    mocks.reset.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("does not show one admin's cached layout to another on the same browser", () => {
    writeStored([{ id: "users", span: 5, rows: 6 }], ADMIN_USER_ID);

    mocks.userId = OTHER_ADMIN_USER_ID;
    const { result } = renderHook(() => useDashboardLayout());

    expect(result.current.entries).toEqual(DEFAULT_LAYOUT);
  });

  it("writes each account's edits under its own key", () => {
    const first = renderHook(() => useDashboardLayout());
    act(() => {
      first.result.current.resizeWidget("users", { span: 8 });
    });
    first.unmount();

    mocks.userId = OTHER_ADMIN_USER_ID;
    const second = renderHook(() => useDashboardLayout());
    act(() => {
      second.result.current.removeWidget("users");
    });

    expect(readStored(ADMIN_USER_ID).entries.find((entry) => entry.id === "users")).toMatchObject({
      span: 8,
    });
    expect(readStored(OTHER_ADMIN_USER_ID).entries.some((entry) => entry.id === "users")).toBe(
      false,
    );
  });

  // Impersonation can swap the account while the dashboard stays mounted; the
  // hook must drop the old account's state — including any queued save, which
  // would otherwise upload the old arrangement under the new account — and
  // become adoptable again for the new account's server layout.
  it("resets to the new account's layout when the user changes while mounted", () => {
    vi.useFakeTimers();
    writeStored([{ id: "users", span: 4, rows: 4 }], OTHER_ADMIN_USER_ID);

    const { result, rerender } = renderHook(() => useDashboardLayout());
    act(() => {
      result.current.resizeWidget("users", { span: 8 });
    });

    mocks.userId = OTHER_ADMIN_USER_ID;
    rerender();

    expect(result.current.entries.find((entry) => entry.id === "users")).toMatchObject({
      span: 4,
      rows: 4,
    });
    act(() => {
      vi.advanceTimersByTime(DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS + 1);
    });
    expect(mocks.save).not.toHaveBeenCalled();

    // The new account's server layout is still adoptable after the switch.
    serverLayout([{ id: "users", span: 6, rows: 5 }]);
    rerender();
    expect(result.current.entries.find((entry) => entry.id === "users")).toMatchObject({
      span: 6,
      rows: 5,
    });
  });

  // The unscoped key cannot be attributed to an account, so it is deleted
  // rather than adopted — and above all never uploaded as someone's layout.
  it("deletes the pre-scoping cache without migrating it to the server", () => {
    window.localStorage.setItem(
      LEGACY_DASHBOARD_LAYOUT_STORAGE_KEY,
      JSON.stringify({ version: 1, entries: [{ id: "users", span: 5, rows: 6 }] }),
    );
    serverNoLayout();

    const { result } = renderHook(() => useDashboardLayout());

    expect(window.localStorage.getItem(LEGACY_DASHBOARD_LAYOUT_STORAGE_KEY)).toBeNull();
    expect(mocks.save).not.toHaveBeenCalled();
    expect(result.current.entries).toEqual(DEFAULT_LAYOUT);
  });
});
