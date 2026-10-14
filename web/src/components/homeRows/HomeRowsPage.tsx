import { useEffect, useRef, type KeyboardEvent, type ReactNode } from "react";
import { ArrowDownToLine, ArrowUpToLine, House, Plus } from "lucide-react";
import {
  ActionMenu,
  type ActionMenuAction,
  type ActionMenuItem,
} from "@/components/calm/ActionMenu";
import { CalmPage } from "@/components/calm/CalmPage";
import { PageMoreMenu, type PageMoreMenuItem } from "@/components/calm/PageMoreMenu";
import { PillSwitcher } from "@/components/calm/PillSwitcher";
import { SelectAllHeader } from "@/components/calm/SelectModeBar";
import { Button } from "@/components/ui/button";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { describeRow, type DescribeContext } from "@/lib/homeRows/describe";
import { pageLabel, pageParam } from "@/lib/homeRows/pages";
import type { HomeRow, HomeRowsAdapter } from "@/lib/homeRows/types";
import { cn } from "@/lib/utils";
import { MobileDockBar } from "./MobileDockBar";
import { ConflictBanner, LibraryPageNote, ReorderHint } from "./notes";
import { RowLine } from "./RowLine";
import { RowList } from "./RowList";
import { MAX_SELECTED_ROWS } from "./SelectModeBar";
import type { RowFocus } from "./useRowFocus";

/** Select mode. While it is on, rows carry checkboxes instead of grips and cannot be dragged. */
export interface HomeRowsSelection {
  selectedIds: ReadonlySet<string>;
  onChange: (rowId: string, checked: boolean, extendRange: boolean) => void;
  onSelectAll: (checked: boolean) => void;
  /** Leaves select mode (Done or Escape). */
  onExit: () => void;
  label: (row: HomeRow) => string;
  /** The floating bar with what to do to the selected rows. */
  bar: ReactNode;
}

/** Under this width the page's buttons move to a bar docked at the bottom. */
const NARROW_QUERY = "(max-width: 1023px)";

/** Menu items every surface offers; the surface decides where they go. */
export interface SharedRowMenuItems {
  moveToTop: ActionMenuAction;
  moveToBottom: ActionMenuAction;
}

/**
 * The Home rows page shell shared by admin Home rows and Settings > Home
 * Screen. Everything surface-specific comes from the adapter and the props;
 * nothing here reads the account role.
 */
export function HomeRowsPage({
  adapter,
  title,
  subtitle,
  moreItems,
  addRow,
  notices,
  rowMenuItems,
  onOpenRow,
  collection,
  selection,
  focus,
  highlightRowId,
  children,
}: {
  adapter: HomeRowsAdapter;
  title: string;
  subtitle: string;
  /** The More menu's items. */
  moreItems: PageMoreMenuItem[];
  addRow: { onClick: () => void; disabled?: boolean };
  notices?: ReactNode;
  rowMenuItems: (row: HomeRow, shared: SharedRowMenuItems) => ActionMenuItem[];
  onOpenRow?: (row: HomeRow) => void;
  collection?: DescribeContext["collection"];
  /** Present while select mode is on. */
  selection?: HomeRowsSelection;
  focus: RowFocus;
  /** A row just added: scrolled into view and briefly highlighted. */
  highlightRowId?: string | null;
  children?: ReactNode;
}) {
  const dragging = useRef(false);
  const list = useRef<HTMLDivElement>(null);
  const narrow = useMediaQuery(NARROW_QUERY);
  const moreTrigger = useRef<HTMLButtonElement>(null);
  const selectAll = useRef<HTMLButtonElement>(null);
  const selectMode = selection !== undefined;
  const wasSelectMode = useRef(selectMode);

  // Entering select mode puts focus on Select all; leaving it, on More, since
  // the Done button or checkbox that had focus is gone.
  useEffect(() => {
    if (wasSelectMode.current === selectMode) return;
    wasSelectMode.current = selectMode;
    (selectMode ? selectAll : moreTrigger).current?.focus();
  }, [selectMode]);
  const highlightShown = Boolean(
    highlightRowId && adapter.rows.some((row) => row.id === highlightRowId),
  );

  useEffect(() => {
    if (!highlightShown || !highlightRowId) return;
    list.current
      ?.querySelector(`[data-row-id=${JSON.stringify(highlightRowId)}]`)
      ?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [highlightRowId, highlightShown]);
  const { rows, surface, page } = adapter;
  const label = pageLabel(page, adapter.pages);
  const shownCount = rows.filter((row) => row.shown).length;
  const summary =
    adapter.status === "ready"
      ? `${rows.length} ${rows.length === 1 ? "row" : "rows"} · ${shownCount} ${surface === "admin" ? "on" : "shown"}`
      : undefined;
  const describeContext: DescribeContext = { pageKind: page.kind, surface, collection };

  function move(row: HomeRow, to: "top" | "bottom") {
    const others = rows.map((entry) => entry.id).filter((id) => id !== row.id);
    void adapter.reorder(
      to === "top" ? [row.id, ...others] : [...others, row.id],
      undefined,
      row.id,
    );
    focus.afterMove(row.id);
  }

  function sharedItems(row: HomeRow, index: number): SharedRowMenuItems {
    return {
      moveToTop: {
        key: "move-top",
        label: "Move to top",
        icon: ArrowUpToLine,
        group: true,
        disabled: !adapter.canReorder || index === 0,
        onSelect: () => move(row, "top"),
      },
      moveToBottom: {
        key: "move-bottom",
        label: "Move to bottom",
        icon: ArrowDownToLine,
        disabled: !adapter.canReorder || index === rows.length - 1,
        onSelect: () => move(row, "bottom"),
      },
    };
  }

  // Escape leaves select mode, but only for keys pressed inside the list or
  // its bar: menus and dialogs render in portals outside them, and an Escape
  // that cancels a keyboard drag must not also leave select mode.
  function handleListKeyDown(event: KeyboardEvent<HTMLElement>) {
    if (
      event.key !== "Escape" ||
      event.defaultPrevented ||
      dragging.current ||
      !selection ||
      !event.currentTarget.contains(event.target as Node)
    )
      return;
    selection.onExit();
  }

  const { attachAddButton } = focus;
  const selectedCount = rows.filter((row) => selection?.selectedIds.has(row.id)).length;
  const more = <PageMoreMenu items={moreItems} compact={narrow} triggerRef={moreTrigger} />;
  const addButton = (
    <Button
      ref={attachAddButton}
      size={narrow ? "lg" : "sm"}
      disabled={addRow.disabled}
      onClick={addRow.onClick}
      className={cn(narrow && "h-12 rounded-[14px] text-[15px]")}
    >
      <Plus /> Add row
    </Button>
  );

  return (
    <CalmPage
      // Settings > Home Screen sits under the Settings page's own heading.
      heading={surface === "admin" ? "page" : "section"}
      title={title}
      subtitle={subtitle}
      actions={
        narrow ? null : (
          <>
            {more}
            {addButton}
          </>
        )
      }
      padBottom={narrow || selectMode}
    >
      {notices}

      <PillSwitcher
        label="Page"
        options={adapter.pages.map((option, index) => ({
          value: pageParam(option.ref),
          label: option.label,
          icon: option.ref.kind === "home" ? House : undefined,
          separated: index === 1,
        }))}
        value={pageParam(page)}
        onChange={(value) => {
          const next = adapter.pages.find((option) => pageParam(option.ref) === value);
          if (next) adapter.setPage(next.ref);
        }}
        disabled={adapter.pending}
        summary={summary}
      />
      {page.kind === "library" ? <LibraryPageNote libraryName={label} /> : null}
      {adapter.conflict ? (
        <ConflictBanner busy={adapter.pending} onReload={() => void adapter.reload()} />
      ) : null}

      {adapter.status === "error" ? (
        <div role="alert" className="surface-panel space-y-3 rounded-2xl p-6">
          <p>{adapter.error ?? "Could not load these rows."}</p>
          <Button variant="outline" onClick={() => void adapter.reload()}>
            Reload rows
          </Button>
        </div>
      ) : adapter.status === "loading" ? (
        <p role="status" className="text-muted-foreground px-1 text-sm">
          Loading rows…
        </p>
      ) : (
        <div className="grid gap-4" onKeyDown={handleListKeyDown}>
          <div ref={list} className="surface-panel rounded-[26px] p-1.5">
            {selection ? (
              <SelectAllHeader
                ref={selectAll}
                count={rows.length}
                selectedCount={selectedCount}
                limit={MAX_SELECTED_ROWS}
                noun="rows"
                onSelectAll={selection.onSelectAll}
                onDone={selection.onExit}
              />
            ) : null}
            {rows.length === 0 ? (
              <p className="text-muted-foreground px-[18px] py-8 text-center text-sm">
                No rows on {label} yet.
              </p>
            ) : (
              <>
                <RowList
                  rows={rows}
                  canReorder={adapter.canReorder && !selectMode}
                  orderToken={adapter.orderToken}
                  label={`Rows on ${label}`}
                  onReorder={(ids, token, movedId) => void adapter.reorder(ids, token, movedId)}
                  onDragActiveChange={(active) => {
                    dragging.current = active;
                  }}
                >
                  {(row, sortable) => (
                    <RowLine
                      key={row.id}
                      {...sortable}
                      row={row}
                      highlighted={row.id === highlightRowId}
                      surface={surface}
                      pageLabel={label}
                      description={describeRow(row, describeContext)}
                      peek={adapter.peek?.(row) ?? null}
                      selection={
                        selection
                          ? {
                              selected: selection.selectedIds.has(row.id),
                              label: selection.label(row),
                              onChange: (checked, extend) =>
                                selection.onChange(row.id, checked, extend),
                            }
                          : undefined
                      }
                      switchDisabled={!adapter.canEdit || (row.legacyTrakt && !row.shown)}
                      onShownChange={(shown) => void adapter.setShown(row.id, shown)}
                      onOpen={onOpenRow ? () => onOpenRow(row) : undefined}
                      menu={
                        <ActionMenu
                          label={`More for ${row.title}`}
                          triggerRef={focus.attachMenuTrigger(row.id)}
                          items={rowMenuItems(row, sharedItems(row, rows.indexOf(row)))}
                        />
                      }
                    />
                  )}
                </RowList>
                {selectMode ? null : <ReorderHint />}
              </>
            )}
          </div>
          {selection?.bar}
        </div>
      )}
      {narrow && !selectMode ? <MobileDockBar more={more} addRow={addButton} /> : null}
      {children}
    </CalmPage>
  );
}
