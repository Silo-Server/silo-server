import { useEffect, useState } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Pencil, SquareCheckBig } from "lucide-react";
import { TouchSensor } from "@dnd-kit/core";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HomeRow, HomeRowsAdapter, PageRef, Surface } from "@/lib/homeRows/types";
import { HomeRowsPage } from "./HomeRowsPage";
import { SelectModeBar } from "./SelectModeBar";
import { useRowFocus } from "./useRowFocus";

const dnd = vi.hoisted(() => ({ sensorOptions: vi.fn() }));
vi.mock("@dnd-kit/core", async () => {
  const actual = await vi.importActual<typeof import("@dnd-kit/core")>("@dnd-kit/core");
  return {
    ...actual,
    useSensor: ((sensor, options) => {
      dnd.sensorOptions(sensor, options);
      return actual.useSensor(sensor, options);
    }) as typeof actual.useSensor,
  };
});

function makeRow(id: string, overrides: Partial<HomeRow> = {}): HomeRow {
  return {
    id,
    title: `Row ${id.toUpperCase()}`,
    sectionType: "recently_added",
    config: {},
    itemLimit: 20,
    hero: false,
    shown: true,
    own: false,
    legacyTrakt: false,
    ...overrides,
  };
}

function makeHarness() {
  return {
    reorder: vi.fn<(ids: string[], token: unknown, movedId?: string) => void>(),
    setPage: vi.fn<(ref: PageRef) => void>(),
    reload: vi.fn<() => Promise<void>>(async () => {}),
    onExit: vi.fn<() => void>(),
    addRow: vi.fn<() => void>(),
    turnOn: vi.fn<() => void>(),
    onSelect: vi.fn<(id: string, checked: boolean, extendRange: boolean) => void>(),
    settle: () => {},
    setOrderToken: (_token: string) => {},
  };
}

let harness: ReturnType<typeof makeHarness>;

function FakePage({
  initialRows,
  surface = "admin",
  page = { kind: "home" },
  pending: initialPending = false,
  conflict = null,
  selectable = false,
}: {
  initialRows: HomeRow[];
  surface?: Surface;
  page?: PageRef;
  pending?: boolean;
  conflict?: HomeRowsAdapter["conflict"];
  /** Opens in select mode with Row A selected. */
  selectable?: boolean;
}) {
  const [rows, setRows] = useState(initialRows);
  const [pending, setPending] = useState(initialPending);
  const [selectMode, setSelectMode] = useState(selectable);
  const [selected, setSelected] = useState<Set<string>>(new Set(selectable ? ["a"] : []));
  const [orderToken, setOrderToken] = useState("token-1");
  useEffect(() => {
    harness.settle = () => setPending(false);
    harness.setOrderToken = (token) => setOrderToken(token);
  });
  const adapter: HomeRowsAdapter = {
    surface,
    page,
    pages: [
      { ref: { kind: "home" }, label: "Home" },
      { ref: { kind: "library", libraryId: 7 }, label: "Movies" },
    ],
    setPage: harness.setPage,
    status: "ready",
    error: null,
    canEdit: true,
    rows,
    pending,
    conflict,
    reload: harness.reload,
    canReorder: !pending,
    orderToken,
    reorder: async (ids, token, movedId) => {
      harness.reorder(ids, token, movedId);
      setPending(true);
      setRows((current) => ids.map((id) => current.find((row) => row.id === id)!));
    },
    setShown: async (id, shown) =>
      setRows((current) => current.map((row) => (row.id === id ? { ...row, shown } : row))),
    setHero: async () => {},
    capabilities: { draftPreview: false, libraryCopies: false },
    create: async () => ({ newIds: [] }),
    openEdit: async () => {
      throw new Error("not used");
    },
    reloadEdit: async () => {
      throw new Error("not used");
    },
    save: async () => {},
  };
  const focus = useRowFocus(rows, pending);
  return (
    <HomeRowsPage
      adapter={adapter}
      title="Home rows"
      subtitle="Subtitle"
      addRow={{ onClick: harness.addRow }}
      moreItems={[
        {
          key: "select",
          label: "Select rows",
          help: "Turn several rows on or off, or delete them together.",
          icon: SquareCheckBig,
          returnFocus: false,
          disabled: selectMode,
          onSelect: () => setSelectMode(true),
        },
      ]}
      focus={focus}
      selection={
        selectMode
          ? {
              selectedIds: selected,
              onChange: (id, checked, extendRange) => {
                harness.onSelect(id, checked, extendRange);
                setSelected((current) => {
                  const next = new Set(current);
                  if (checked) next.add(id);
                  else next.delete(id);
                  return next;
                });
              },
              onSelectAll: (checked) =>
                setSelected(new Set(checked ? rows.map((row) => row.id) : [])),
              onExit: () => {
                harness.onExit();
                setSelectMode(false);
                setSelected(new Set());
              },
              label: (row) => `Select ${row.title}`,
              bar: (
                <SelectModeBar
                  count={selected.size}
                  onTurnOn={harness.turnOn}
                  onTurnOff={vi.fn()}
                  onDelete={vi.fn()}
                />
              ),
            }
          : undefined
      }
      rowMenuItems={(_row, shared) => [
        { key: "edit", label: "Edit row…", icon: Pencil, onSelect: () => {} },
        shared.moveToTop,
        shared.moveToBottom,
      ]}
    />
  );
}

beforeEach(() => {
  harness = makeHarness();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function openMenu(title: string) {
  await userEvent.click(screen.getByRole("button", { name: `More for ${title}` }));
  return screen.findByRole("menu");
}

describe("HomeRowsPage", () => {
  it("shows one line per row with its sentence, count and hero tag", () => {
    render(
      <FakePage
        initialRows={[
          makeRow("a", { hero: true }),
          makeRow("b", { sectionType: "trending_on_server", config: { window: "7d" } }),
          makeRow("c", { shown: false }),
        ]}
      />,
    );
    const list = screen.getByRole("list", { name: "Rows on Home" });
    // Only the list carries the name, so a screen reader announces it once.
    expect(screen.queryByRole("region", { name: "Rows on Home" })).toBeNull();
    const [first, second, third] = within(list).getAllByRole("listitem");
    expect(first).toHaveTextContent("Row AHero banner");
    expect(first).toHaveTextContent("Newest movies and episodes from all libraries·20 titles");
    expect(second).toHaveTextContent("Most played on this server in the last 7 days");
    expect(third).toHaveTextContent("Row C is off. Nobody sees this row.");
    expect(screen.getByText("3 rows · 2 on")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Row A is on for everyone" })).toBeChecked();
    expect(screen.getByRole("switch", { name: "Row C is off for everyone" })).not.toBeChecked();
    expect(screen.getByText(/Drag a row to move it/)).toBeInTheDocument();
  });

  it("keeps focus on the switch while a row collapses and expands", async () => {
    render(<FakePage initialRows={[makeRow("a"), makeRow("b")]} />);
    const toggle = () => screen.getByRole("switch", { name: /^Row A is/ });
    toggle().focus();
    await userEvent.keyboard(" ");
    expect(screen.getByText(/is off\. Nobody sees this row\./)).toBeInTheDocument();
    expect(document.activeElement).toBe(toggle());
    await userEvent.keyboard(" ");
    expect(screen.queryByText(/is off\. Nobody sees this row\./)).not.toBeInTheDocument();
    expect(document.activeElement).toBe(toggle());
    await userEvent.keyboard(" ");
    expect(document.activeElement).toBe(toggle());
    expect(toggle()).not.toBeChecked();
  });

  it("does not let a legacy Trakt row be turned back on", () => {
    render(<FakePage initialRows={[makeRow("a", { shown: false, legacyTrakt: true })]} />);
    expect(screen.getByRole("switch", { name: "Row A is off for everyone" })).toBeDisabled();
  });

  it("marks the current page and disables the switcher while a write is pending", async () => {
    const { unmount } = render(<FakePage initialRows={[makeRow("a")]} />);
    const group = screen.getByRole("group", { name: "Page" });
    expect(within(group).getByRole("button", { name: "Home" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await userEvent.click(within(group).getByRole("button", { name: "Movies" }));
    expect(harness.setPage).toHaveBeenCalledWith({ kind: "library", libraryId: 7 });
    unmount();

    render(<FakePage initialRows={[makeRow("a")]} pending />);
    expect(screen.getByRole("button", { name: "Movies" })).toBeDisabled();
  });

  it("explains library pages", () => {
    render(<FakePage initialRows={[makeRow("a")]} page={{ kind: "library", libraryId: 7 }} />);
    expect(screen.getByText("These rows show above the full Movies grid.")).toBeInTheDocument();
    expect(screen.getByText("Newest additions to this library", { exact: false })).toBeVisible();
  });

  it("offers Reload rows when the rows changed elsewhere", async () => {
    render(<FakePage initialRows={[makeRow("a")]} conflict={{ scope: "page" }} />);
    const banner = screen.getByRole("alert");
    expect(banner).toHaveTextContent("These rows changed since you opened this page.");
    await userEvent.click(within(banner).getByRole("button", { name: "Reload rows" }));
    expect(harness.reload).toHaveBeenCalled();
  });

  it("moves a row to the bottom from its menu and focuses its menu once the move settles", async () => {
    render(<FakePage initialRows={[makeRow("a"), makeRow("b"), makeRow("c")]} />);
    const menu = await openMenu("Row A");
    expect(within(menu).getByRole("menuitem", { name: "Move to top" })).toHaveAttribute(
      "data-disabled",
    );
    await userEvent.click(within(menu).getByRole("menuitem", { name: "Move to bottom" }));
    expect(harness.reorder).toHaveBeenCalledWith(["b", "c", "a"], undefined, "a");
    act(() => harness.settle());
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("button", { name: "More for Row A" })),
    );
  });

  it("drags only by the grip, which does not scroll the page on touch", () => {
    render(<FakePage initialRows={[makeRow("a")]} />);
    const grip = screen.getByRole("button", { name: "Move Row A" });
    expect(grip).toHaveClass("touch-none");
    expect(grip).toHaveAttribute("aria-roledescription", "sortable");
    // A touch drag starts only after a press and hold, so a swipe still scrolls.
    expect(dnd.sensorOptions).toHaveBeenCalledWith(TouchSensor, {
      activationConstraint: { delay: 200, tolerance: 5 },
    });
  });

  it("reorders with the keyboard against the version it picked up, and announces rows by title", async () => {
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      const item = this.closest("li[data-row-id]");
      const index = item ? Array.from(item.parentElement!.children).indexOf(item) : 0;
      const top = item ? index * 60 : 0;
      const height = item ? 60 : 0;
      return {
        x: 0,
        y: top,
        top,
        left: 0,
        right: 800,
        bottom: top + height,
        width: 800,
        height,
        toJSON: () => ({}),
      } as DOMRect;
    });
    Element.prototype.scrollIntoView = vi.fn();
    render(<FakePage initialRows={[makeRow("a"), makeRow("b"), makeRow("c")]} />);
    const grip = screen.getByRole("button", { name: "Move Row A" });
    grip.focus();
    fireEvent.keyDown(grip, { code: "Space", key: " " });
    await screen.findByText("Picked up Row A, position 1 of 3.");
    // A refetch while the row is in the air brings a newer version.
    act(() => harness.setOrderToken("token-2"));
    fireEvent.keyDown(grip, { code: "ArrowDown", key: "ArrowDown" });
    await screen.findByText("Row A is now at position 2 of 3.");
    fireEvent.keyDown(grip, { code: "Space", key: " " });
    await waitFor(() =>
      expect(harness.reorder).toHaveBeenCalledWith(["b", "a", "c"], "token-1", "a"),
    );
  });

  it("enters select mode from More: checkboxes replace the grips and focus lands on Select all", async () => {
    render(<FakePage initialRows={[makeRow("a"), makeRow("b")]} />);
    expect(screen.getByRole("button", { name: "Move Row A" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "More" }));
    const item = await screen.findByRole("menuitem", { name: "Select rows" });
    expect(item).toHaveAccessibleDescription(
      "Turn several rows on or off, or delete them together.",
    );
    await userEvent.click(item);
    const selectAll = await screen.findByRole("checkbox", { name: "Select all" });
    await waitFor(() => expect(document.activeElement).toBe(selectAll));
    expect(screen.getByText("Up to 100 rows at a time")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Move Row/ })).not.toBeInTheDocument();
    // Tab order inside a row: checkbox → switch → ⋯.
    const rowA = screen.getAllByRole("listitem")[0]!;
    // The checkbox, switch and ⋯ are all buttons, in DOM (and so tab) order.
    const focusable = Array.from(rowA.querySelectorAll("button")).map((element) =>
      element.getAttribute("aria-label"),
    );
    expect(focusable).toEqual(["Select Row A", "Row A is on for everyone", "More for Row A"]);
    expect(screen.getByRole("group", { name: "Selected rows" })).toHaveTextContent("0 selected");
  });

  it("selects every row with Select all and shows how many are selected", async () => {
    render(<FakePage initialRows={[makeRow("a"), makeRow("b")]} selectable />);
    const selectAll = screen.getByRole("checkbox", { name: "Select all" });
    expect(selectAll).toHaveAttribute("aria-checked", "mixed");
    await userEvent.click(selectAll);
    expect(screen.getByRole("checkbox", { name: "Select Row B" })).toBeChecked();
    expect(selectAll).toBeChecked();
    expect(
      within(screen.getByRole("group", { name: "Selected rows" })).getByRole("status"),
    ).toHaveTextContent("2 selected");
    await userEvent.click(screen.getByRole("button", { name: "Turn on" }));
    expect(harness.turnOn).toHaveBeenCalledTimes(1);
    await userEvent.click(selectAll);
    expect(screen.getByRole("checkbox", { name: "Select Row A" })).not.toBeChecked();
    expect(screen.getByRole("button", { name: "Turn on" })).toBeDisabled();
  });

  it("pins the selection bar to the bottom of the screen and keeps the last row clear of it", () => {
    render(<FakePage initialRows={[makeRow("a")]} selectable />);
    // The bar is fixed to the viewport, not sticky: an ancestor that clips
    // overflow would leave a sticky bar at the end of a long list.
    const bar = screen.getByRole("group", { name: "Selected rows" });
    expect(bar.closest(".fixed")).not.toBeNull();
    expect(bar.closest(".sticky")).toBeNull();
    expect(screen.getByRole("banner").parentElement).toHaveClass("pb-24");
  });

  it("refuses to act on more than 100 rows at a time", () => {
    render(<SelectModeBar count={101} onTurnOn={vi.fn()} onTurnOff={vi.fn()} onDelete={vi.fn()} />);
    expect(screen.getByRole("status")).toHaveTextContent("Select up to 100 rows at a time.");
    for (const name of ["Turn on", "Turn off", "Delete…"])
      expect(screen.getByRole("button", { name })).toBeDisabled();
  });

  it("leaves select mode on Escape inside the list, not from an open row menu", async () => {
    render(<FakePage initialRows={[makeRow("a"), makeRow("b")]} selectable />);
    const menu = await openMenu("Row B");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(menu).not.toBeInTheDocument());
    expect(harness.onExit).not.toHaveBeenCalled();
    expect(screen.getByRole("checkbox", { name: "Select Row A" })).toBeInTheDocument();
    screen.getByRole("checkbox", { name: "Select Row A" }).focus();
    await userEvent.keyboard("{Escape}");
    expect(harness.onExit).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("checkbox", { name: "Select Row A" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Move Row A" })).toBeInTheDocument();
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("button", { name: "More" })),
    );
  });

  it("leaves select mode with Done", async () => {
    render(<FakePage initialRows={[makeRow("a")]} selectable />);
    await userEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(harness.onExit).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("group", { name: "Selected rows" })).not.toBeInTheDocument();
  });

  it("asks for a range when a row's checkbox is shift-clicked or Shift+Space is pressed", async () => {
    render(<FakePage initialRows={[makeRow("a"), makeRow("b"), makeRow("c")]} selectable />);
    fireEvent.click(screen.getByRole("checkbox", { name: "Select Row B" }));
    expect(harness.onSelect).toHaveBeenLastCalledWith("b", true, false);
    fireEvent.click(screen.getByRole("checkbox", { name: "Select Row B" }), { shiftKey: true });
    expect(harness.onSelect).toHaveBeenLastCalledWith("b", false, true);
    screen.getByRole("checkbox", { name: "Select Row C" }).focus();
    await userEvent.keyboard("{Shift>} {/Shift}");
    expect(harness.onSelect).toHaveBeenLastCalledWith("c", true, true);
    await userEvent.keyboard(" ");
    expect(harness.onSelect).toHaveBeenLastCalledWith("c", false, false);
    // A browser's keyboard click may not carry Shift, so Shift+Space is read
    // from the key down.
    const rowA = screen.getByRole("checkbox", { name: "Select Row A" });
    fireEvent.keyDown(rowA, { key: " ", shiftKey: true });
    fireEvent.click(rowA);
    expect(harness.onSelect).toHaveBeenLastCalledWith("a", false, true);
    // The Shift state is used once; a plain click after it does not extend.
    fireEvent.click(rowA);
    expect(harness.onSelect).toHaveBeenLastCalledWith("a", true, false);
  });

  it("does not start a drag in select mode", () => {
    render(<FakePage initialRows={[makeRow("a"), makeRow("b")]} selectable />);
    expect(screen.queryByRole("button", { name: /^Move Row/ })).not.toBeInTheDocument();
    expect(screen.queryByText(/Drag a row to move it/)).not.toBeInTheDocument();
  });

  it("docks More and a full-width Add row at the bottom on narrow screens", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(max-width: 1023px)",
      media: query,
      addEventListener() {},
      removeEventListener() {},
    }));
    render(<FakePage initialRows={[makeRow("a")]} />);
    const header = screen.getByRole("banner");
    expect(within(header).queryByRole("button")).not.toBeInTheDocument();
    const dock = screen.getByRole("region", { name: "Page actions" });
    const more = within(dock).getByRole("button", { name: "More" });
    expect(more).toHaveClass("size-12");
    await userEvent.click(within(dock).getByRole("button", { name: "Add row" }));
    expect(harness.addRow).toHaveBeenCalledTimes(1);
    // Row controls get 44px targets on touch screens.
    expect(screen.getByRole("button", { name: "More for Row A" })).toHaveClass("max-lg:size-11");
    expect(screen.getByRole("switch", { name: "Row A is on for everyone" })).toHaveClass(
      "max-lg:after:-inset-[13px]",
    );
  });

  it("hides the dock in select mode, where the selection bar takes its place", () => {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: query === "(max-width: 1023px)",
      media: query,
      addEventListener() {},
      removeEventListener() {},
    }));
    render(<FakePage initialRows={[makeRow("a")]} selectable />);
    expect(screen.queryByRole("region", { name: "Page actions" })).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Selected rows" })).toHaveTextContent("1 selected");
  });

  it("uses profile wording on the profile surface", () => {
    render(
      <FakePage
        surface="profile"
        page={{ kind: "library", libraryId: 7 }}
        initialRows={[makeRow("a", { own: true }), makeRow("b", { shown: false })]}
      />,
    );
    expect(screen.getByRole("switch", { name: "Show Row A on my Movies page" })).toBeChecked();
    expect(screen.getByText("Yours")).toBeInTheDocument();
    expect(screen.getByText(/is hidden on your Movies page\./)).toBeInTheDocument();
    expect(screen.getByText("2 rows · 1 shown")).toBeInTheDocument();
  });
});
