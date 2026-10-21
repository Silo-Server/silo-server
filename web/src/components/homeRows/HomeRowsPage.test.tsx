import { useEffect, useState } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Pencil, SquareCheckBig } from "lucide-react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HomeRow, HomeRowsAdapter, PageRef, Surface } from "@/lib/homeRows/types";
import { HomeRowsPage } from "./HomeRowsPage";
import { SelectModeBar } from "./SelectModeBar";
import { useRowFocus } from "./useRowFocus";

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
});
